package agenttransport

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SSHServer runs only inside an authenticated outbound data tunnel. Every
// command, PTY and SFTP operation runs as this process's OS user: no implicit
// sudo, impersonation, forwarding or extra filesystem sandbox is introduced.
type SSHServer struct {
	Signer           ssh.Signer
	Shell            string
	WorkingDirectory string
	Environment      []string
}

func (s *SSHServer) Serve(ctx context.Context, conn net.Conn, secret string) error {
	defer conn.Close()
	if s.Signer == nil || len(secret) < 32 {
		return errors.New("missing tunnel SSH credentials")
	}
	config := &ssh.ServerConfig{MaxAuthTries: 1, PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if subtle.ConstantTimeCompare(password, []byte(secret)) != 1 {
			return nil, errors.New("invalid tunnel credentials")
		}
		return nil, nil
	}}
	config.AddHostKey(s.Signer)
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return err
	}
	defer server.Close()
	_ = conn.SetDeadline(time.Time{})
	go func() {
		for request := range requests {
			_ = request.Reply(request.Type == "keepalive@openssh.com", nil)
		}
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 16)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "only SSH sessions are supported")
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = incoming.Reject(ssh.ResourceShortage, "session limit reached")
			continue
		}
		channel, reqs, err := incoming.Accept()
		if err != nil {
			<-slots
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-slots }(); s.session(ctx, channel, reqs) }()
	}
	return server.Wait()
}

type ptyRequest struct {
	Term                         string
	Columns, Rows, Width, Height uint32
	Modes                        string
}
type windowRequest struct{ Columns, Rows, Width, Height uint32 }

type runningProcess struct {
	cmd    *exec.Cmd
	pty    *os.File
	stdin  io.WriteCloser
	done   <-chan uint32
	native nativeProcess
}

type nativeProcess interface {
	Close()
	Resize(windowRequest) error
	Signal(string) error
}

func (p *runningProcess) close() {
	if p == nil {
		return
	}
	if p.native != nil {
		p.native.Close()
		return
	}
	killProcess(p.cmd)
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.pty != nil {
		_ = p.pty.Close()
	}
}

func (s *SSHServer) session(ctx context.Context, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	var terminal *ptyRequest
	var process *runningProcess
	var finished <-chan uint32
	var subsystem *sftp.Server
	environment := make(map[string]string)
	defer func() {
		_ = channel.Close()
		process.close()
		if subsystem != nil {
			_ = subsystem.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case status := <-finished:
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			_ = channel.CloseWrite()
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			switch request.Type {
			case "env":
				var env struct{ Name, Value string }
				valid := process == nil && subsystem == nil && ssh.Unmarshal(request.Payload, &env) == nil && len(env.Value) <= 4096 &&
					!strings.ContainsRune(env.Value, 0) && (env.Name == "LANG" || env.Name == "TERM" || env.Name == "COLORTERM" || strings.HasPrefix(env.Name, "LC_")) && len(environment) < 32
				if valid {
					environment[env.Name] = env.Value
				}
				_ = request.Reply(valid, nil)
			case "pty-req":
				var next ptyRequest
				valid := terminal == nil && process == nil && subsystem == nil && ssh.Unmarshal(request.Payload, &next) == nil && validSize(next.Columns, next.Rows) && len(next.Term) <= 128 && !strings.ContainsAny(next.Term, "\x00\r\n") && len(next.Modes) <= 4096
				if valid {
					terminal = &next
				}
				_ = request.Reply(valid, nil)
			case "window-change":
				var size windowRequest
				valid := terminal != nil && ssh.Unmarshal(request.Payload, &size) == nil && validSize(size.Columns, size.Rows)
				if valid {
					terminal.Columns, terminal.Rows = size.Columns, size.Rows
					if process != nil && process.pty != nil {
						valid = resizePTY(process.pty, size) == nil
					}
					if process != nil && process.native != nil {
						valid = process.native.Resize(size) == nil
					}
				}
				_ = request.Reply(valid, nil)
			case "exec", "shell":
				if process != nil || subsystem != nil {
					_ = request.Reply(false, nil)
					continue
				}
				command := ""
				if request.Type == "exec" {
					var payload struct{ Command string }
					if ssh.Unmarshal(request.Payload, &payload) != nil || len(payload.Command) > 256*1024 || strings.ContainsRune(payload.Command, 0) {
						_ = request.Reply(false, nil)
						continue
					}
					command = payload.Command
				} else if len(request.Payload) != 0 {
					_ = request.Reply(false, nil)
					continue
				}
				var err error
				process, err = s.startProcess(channel, terminal, environment, command, request.Type == "shell")
				if err != nil {
					_, _ = fmt.Fprintln(channel.Stderr(), "agent: could not start the local shell")
					_ = request.Reply(false, nil)
					return
				}
				finished = process.done
				_ = request.Reply(true, nil)
			case "subsystem":
				var payload struct{ Name string }
				if process != nil || subsystem != nil || terminal != nil || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
					_ = request.Reply(false, nil)
					continue
				}
				var err error
				options := []sftp.ServerOption{sftp.WithServerWorkingDirectory(s.workDir())}
				if runtime.GOOS == "windows" {
					// OpenSSH clients send both C:/... and /C:/...; a POSIX
					// working-directory prefix must not be joined to a drive path.
					options = []sftp.ServerOption{sftp.WindowsRootEnumeratesDrives()}
				}
				subsystem, err = sftp.NewServer(channel, options...)
				if err != nil {
					_ = request.Reply(false, nil)
					return
				}
				_ = request.Reply(true, nil)
				done := make(chan uint32, 1)
				finished = done
				go func(server *sftp.Server) {
					err := server.Serve()
					if err != nil && !errors.Is(err, io.EOF) {
						done <- 1
					} else {
						done <- 0
					}
				}(subsystem)
			case "signal":
				var payload struct{ Signal string }
				valid := process != nil && ssh.Unmarshal(request.Payload, &payload) == nil
				if valid {
					if process.native != nil {
						valid = process.native.Signal(payload.Signal) == nil
					} else {
						valid = signalProcess(process.cmd, process.pty, payload.Signal) == nil
					}
				}
				_ = request.Reply(valid, nil)
			default:
				_ = request.Reply(false, nil)
			}
		}
	}
}

func validSize(columns, rows uint32) bool {
	return columns > 0 && rows > 0 && columns <= 4096 && rows <= 4096
}

func (s *SSHServer) workDir() string {
	if s.WorkingDirectory != "" {
		return s.WorkingDirectory
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

func (s *SSHServer) startProcess(channel ssh.Channel, terminal *ptyRequest, environment map[string]string, command string, shell bool) (*runningProcess, error) {
	if runtime.GOOS == "windows" {
		return s.startWindowsProcess(channel, terminal, environment, command, shell)
	}
	executable := s.Shell
	if executable == "" {
		executable = "/bin/sh"
	}
	args := []string{"-c", command}
	if shell {
		args = []string{"-l"}
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = s.workDir()
	cmd.Env = append([]string{}, s.Environment...)
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}
	for key, value := range environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.WaitDelay = 2 * time.Second
	done := make(chan uint32, 1)
	process := &runningProcess{cmd: cmd, done: done}
	var outputDone <-chan struct{}
	if terminal != nil {
		cmd.Env = append(cmd.Env, "TERM="+terminal.Term)
		file, err := startPTY(cmd, *terminal)
		if err != nil {
			return nil, err
		}
		process.pty = file
		copied := make(chan struct{})
		outputDone = copied
		go func() { _, _ = io.Copy(channel, file); close(copied) }()
		go func() { _, _ = io.Copy(file, channel) }()
	} else {
		prepareProcess(cmd)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		process.stdin = stdin
		cmd.Stdout, cmd.Stderr = channel, channel.Stderr()
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			return nil, err
		}
		go func() { _, _ = io.Copy(stdin, channel); _ = stdin.Close() }()
	}
	go func() {
		err := cmd.Wait()
		status := uint32(0)
		if err != nil {
			status = 1
			if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() >= 0 {
				status = uint32(exit.ExitCode())
			}
		}
		if outputDone != nil {
			select {
			case <-outputDone:
			case <-time.After(2 * time.Second):
				_ = process.pty.Close()
			}
		}
		done <- status
	}()
	return process, nil
}
