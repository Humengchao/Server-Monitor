package services

import (
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type TerminalSession struct {
	conn    *websocket.Conn
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	done    chan struct{}
	mu      sync.Mutex
}

func NewTerminalSession(conn *websocket.Conn, client *ssh.Client) (*TerminalSession, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", 40, 80, modes); err != nil {
		sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, err
	}

	ts := &TerminalSession{
		conn:    conn,
		client:  client,
		session: sess,
		stdin:   stdin,
		stdout:  stdout,
		done:    make(chan struct{}),
	}
	// read stdout + stderr combined
	go func() {
		go io.Copy(ts, stdout)
		io.Copy(ts, stderr)
	}()
	return ts, nil
}

func (ts *TerminalSession) Write(data []byte) (int, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(data), ts.conn.WriteMessage(websocket.TextMessage, data)
}

func (ts *TerminalSession) Read(p []byte) (int, error) {
	mt, msg, err := ts.conn.ReadMessage()
	if err != nil {
		close(ts.done)
		return 0, err
	}
	// Close on close message from client
	if mt == websocket.CloseMessage {
		close(ts.done)
		return 0, io.EOF
	}
	return copy(p, msg), nil
}

func (ts *TerminalSession) Start() {
	// forward user input to ssh stdin
	for {
		buf := make([]byte, 4096)
		n, err := ts.stdout.Read(buf)
		if err != nil {
			close(ts.done)
			return
		}
		if n > 0 {
			ts.Write(buf[:n])
		}
	}
}

func (ts *TerminalSession) Stdin() io.Writer      { return ts.stdin }
func (ts *TerminalSession) Done() <-chan struct{} { return ts.done }

func (ts *TerminalSession) Resize(rows, cols int) {
	if ts.session != nil {
		ts.session.WindowChange(rows, cols)
	}
}

func (ts *TerminalSession) Close() {
	ts.session.Close()
	ts.client.Close()
}

func DialSSH(host string, port int, username, password, key, hostKey string) (*ssh.Client, error) {
	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	if hostKey != "" {
		parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
		if err != nil {
			log.Printf("SSH: failed to parse host key: %v", err)
		} else {
			hostKeyCallback = ssh.FixedHostKey(parsedKey)
		}
	}
	config := &ssh.ClientConfig{
		User:            username,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}
	if password != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else if key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, err
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		log.Printf("SSH: no auth method provided")
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
}
