package services

import (
	"context"
	"time"

	"golang.org/x/crypto/ssh"
)

// x/crypto/ssh cannot cancel an individual OpenChannel request. Give a late
// reply or channel close a short grace period so other sessions on the shared
// connection keep working. If the peer still does not respond, close the
// transport to release SSH's waiting goroutines; the cache redials on next use.
const sshSessionCleanupGrace = 5 * time.Second

func cleanupSSHOperation(client *ssh.Client, cleanup func()) {
	watchdog := time.AfterFunc(sshSessionCleanupGrace, func() { _ = client.Close() })
	go func() {
		defer watchdog.Stop()
		cleanup()
	}()
}

// openedSessionConn lets ssh.NewSession initialize its normal session state
// around a channel we already opened. Keeping the channel and request stream
// available until ownership transfers to Session lets canceled opens wait for
// the peer's actual channel close, rather than mistaking stdout EOF for close.
// Its OpenChannel is consumed exactly once by the temporary client's NewSession;
// it does not own the transport or start any ssh.NewClient transport loops.
type openedSessionConn struct {
	ssh.Conn
	channel  ssh.Channel
	requests <-chan *ssh.Request
}

func (conn *openedSessionConn) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return conn.channel, conn.requests, nil
}

// newSSHSession includes channel creation in the caller's deadline. On cancel,
// ownership passes to cleanup, which closes any session the peer accepts late.
func newSSHSession(ctx context.Context, client *ssh.Client) (*ssh.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		channel  ssh.Channel
		requests <-chan *ssh.Request
		err      error
	}
	ready := make(chan result, 1)
	go func() {
		channel, requests, err := client.OpenChannel("session", nil)
		ready <- result{channel: channel, requests: requests, err: err}
	}()
	closeOpened := func(opened result) {
		if opened.channel != nil {
			_ = opened.channel.Close()
			// Close only sends a packet. The stream closes when the peer
			// acknowledges channel close (or the watchdog closes transport).
			ssh.DiscardRequests(opened.requests)
		}
	}
	select {
	case opened := <-ready:
		if err := ctx.Err(); err != nil {
			if opened.channel != nil {
				cleanupSSHOperation(client, func() { closeOpened(opened) })
			}
			return nil, err
		}
		if opened.err != nil {
			return nil, opened.err
		}
		openedClient := &ssh.Client{Conn: &openedSessionConn{Conn: client.Conn, channel: opened.channel, requests: opened.requests}}
		return openedClient.NewSession()
	case <-ctx.Done():
		cleanupSSHOperation(client, func() {
			closeOpened(<-ready)
		})
		return nil, ctx.Err()
	}
}

// runCmdContext bounds channel creation, command startup, execution, and local
// cleanup with one context. Cancellation returns immediately; only this
// session is closed unless the peer fails to finish cleanup within the grace.
func runCmdContext(ctx context.Context, client *ssh.Client, command string, limit int) (string, bool, error) {
	session, err := newSSHSession(ctx, client)
	if err != nil {
		return "", false, err
	}
	writer := &syncLimitedWriter{limit: limit}
	session.Stdout, session.Stderr = writer, writer
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		err := ctx.Err()
		if err == nil {
			err = session.Run(command)
		}
		_ = session.Close()
		done <- err
	}()
	select {
	case err := <-done:
		output, truncated := writer.result()
		if ctx.Err() != nil {
			return output, truncated, ctx.Err()
		}
		return output, truncated, err
	case <-ctx.Done():
		cleanupSSHOperation(client, func() {
			_ = session.Close()
			<-finished
		})
		output, truncated := writer.result()
		return output, truncated, ctx.Err()
	}
}
