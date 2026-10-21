package services

import (
	"context"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// An SFTP stream owns one channel, not the shared transport. Cancellation
// releases local readers/writers immediately; late SSH operations are drained
// in the background, with the same cleanup watchdog as command sessions.
type sshContextStream struct {
	ctx     context.Context
	cancel  context.CancelFunc
	reader  io.Reader
	writer  io.Writer
	client  *ssh.Client
	session *ssh.Session
	mu      sync.Mutex
	closed  bool
	active  sync.WaitGroup
}

func (s *sshContextStream) operation(run func() (int, error)) (int, error) {
	type result struct {
		n   int
		err error
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return 0, context.Canceled
	}
	s.active.Add(1)
	s.mu.Unlock()
	done := make(chan result, 1)
	go func() { defer s.active.Done(); n, err := run(); done <- result{n, err} }()
	select {
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	case value := <-done:
		return value.n, value.err
	}
}

func (s *sshContextStream) Read(p []byte) (int, error) {
	// A canceled raw read can finish later; it must not touch caller memory.
	buffer := make([]byte, len(p))
	n, err := s.operation(func() (int, error) { return s.reader.Read(buffer) })
	copy(p, buffer[:n])
	return n, err
}
func (s *sshContextStream) Write(p []byte) (int, error) {
	buffer := append([]byte(nil), p...)
	return s.operation(func() (int, error) { return s.writer.Write(buffer) })
}
func (s *sshContextStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	cleanupSSHOperation(s.client, func() { _ = s.session.Close(); s.active.Wait() })
	return nil
}

func openSFTPStream(ctx context.Context, client *ssh.Client) (*sshContextStream, error) {
	session, err := newSSHSession(ctx, client)
	if err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.Stderr = io.Discard
	streamCtx, cancel := context.WithCancel(ctx)
	stream := &sshContextStream{ctx: streamCtx, cancel: cancel, reader: output, writer: input, client: client, session: session}
	context.AfterFunc(streamCtx, func() { _ = stream.Close() })
	_, err = stream.operation(func() (int, error) { return 0, session.RequestSubsystem("sftp") })
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}
