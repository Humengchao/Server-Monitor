package services

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type TerminalSession struct {
	conn      *websocket.Conn
	client    *ssh.Client
	session   *ssh.Session
	stdin     io.WriteCloser
	stdout    io.Reader
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
}

// controlMsg is a client -> server control message. The client sends it on the
// websocket prefixed with a 0x01 byte; regular keystrokes are sent raw.
type controlMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
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
	// stdout + stderr -> websocket; signal Done when the SSH session ends
	// (e.g. the user types "exit") so the handler can tear everything down.
	go func() {
		go io.Copy(ts, stdout)
		io.Copy(ts, stderr)
		sess.Wait()
		ts.closeDone()
	}()
	return ts, nil
}

func (ts *TerminalSession) closeDone() {
	ts.closeOnce.Do(func() { close(ts.done) })
}

// Write sends SSH output to the websocket (io.Copy destination).
func (ts *TerminalSession) Write(data []byte) (int, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(data), ts.conn.WriteMessage(websocket.TextMessage, data)
}

// PumpStdin reads websocket messages and forwards them to the SSH session's
// stdin. Messages prefixed with 0x01 carry a JSON control payload (currently
// only terminal resize). Blocks until the websocket closes or errors, then
// signals Done.
func (ts *TerminalSession) PumpStdin() {
	defer ts.closeDone()
	for {
		_, msg, err := ts.conn.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		if msg[0] == 0x01 {
			var cm controlMsg
			if err := json.Unmarshal(msg[1:], &cm); err == nil &&
				cm.Type == "resize" && cm.Cols > 0 && cm.Rows > 0 {
				ts.Resize(cm.Rows, cm.Cols)
			}
			continue
		}
		if _, err := ts.stdin.Write(msg); err != nil {
			return
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
