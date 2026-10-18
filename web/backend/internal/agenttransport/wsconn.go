package agenttransport

import (
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn is an ordered binary byte stream over WebSockets. One stream owns
// one WebSocket, so setting a collection deadline cannot kill a terminal or
// file transfer. A deadline is terminal, as required by Gorilla after an I/O
// timeout; callers must redial instead of reusing the timed-out connection.
type WSConn struct {
	ws                          *websocket.Conn
	readMu, writeMu             sync.Mutex
	reader                      io.Reader
	mu                          sync.Mutex
	readDeadline, writeDeadline time.Time
	readTimer, writeTimer       *time.Timer
	closed, timedOut            bool
	onClose                     func()
	done                        chan struct{}
}

func NewWSConn(ws *websocket.Conn) *WSConn {
	return newWSConn(ws, nil)
}

func newWSConn(ws *websocket.Conn, onClose func()) *WSConn {
	ws.SetReadLimit(1 << 20)
	conn := &WSConn{ws: ws, done: make(chan struct{}), onClose: onClose}
	// A terminal can be idle for hours while its control connection remains
	// healthy. Keep each data WebSocket alive through reverse proxy timeouts.
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-conn.done:
				return
			case <-ticker.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(controlWriteTimeout)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return conn
}

func (c *WSConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if c.reader == nil {
			kind, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, c.ioError(err)
			}
			if kind != websocket.BinaryMessage {
				_ = c.Close()
				return 0, io.ErrUnexpectedEOF
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, c.ioError(err)
		}
		return n, nil
	}
}

func (c *WSConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		n := min(len(p), 64*1024)
		if err := c.ws.WriteMessage(websocket.BinaryMessage, p[:n]); err != nil {
			return written, c.ioError(err)
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *WSConn) ioError(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timedOut {
		return os.ErrDeadlineExceeded
	}
	if c.closed {
		return net.ErrClosed
	}
	return err
}

func (c *WSConn) Close() error {
	c.mu.Lock()
	callback, changed := c.closeLocked()
	c.mu.Unlock()
	if !changed {
		return nil
	}
	err := c.ws.Close()
	if callback != nil {
		callback()
	}
	return err
}

func (c *WSConn) closeLocked() (func(), bool) {
	if c.closed {
		return nil, false
	}
	c.closed = true
	close(c.done)
	if c.readTimer != nil {
		c.readTimer.Stop()
	}
	if c.writeTimer != nil {
		c.writeTimer.Stop()
	}
	return c.onClose, true
}

func (c *WSConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *WSConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }
func (c *WSConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *WSConn) SetReadDeadline(t time.Time) error  { return c.setDeadline(t, true) }
func (c *WSConn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t, false) }
func (c *WSConn) setDeadline(t time.Time, read bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	deadline, timer := &c.writeDeadline, &c.writeTimer
	if read {
		deadline, timer = &c.readDeadline, &c.readTimer
	}
	*deadline = t
	if *timer != nil {
		(*timer).Stop()
	}
	if t.IsZero() {
		*timer = nil
		return nil
	}
	*timer = time.AfterFunc(time.Until(t), func() {
		c.mu.Lock()
		expired := !c.closed && !deadline.IsZero() && !time.Now().Before(*deadline)
		var callback func()
		if expired {
			c.timedOut = true
			callback, _ = c.closeLocked()
		}
		c.mu.Unlock()
		if expired {
			_ = c.ws.Close()
			if callback != nil {
				callback()
			}
		}
	})
	return nil
}
