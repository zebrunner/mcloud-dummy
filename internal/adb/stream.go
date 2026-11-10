package adb

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Stream is one adb socket opened by the host (OPEN): a service of the device reads and writes it like a net.Conn.
// The flow control follows adbd without delayed_ack: a received WRTE is acknowledged (OKAY) once the service has
// read its data, and a WRTE is sent only after the previous one was acknowledged by the host.
type Stream struct {
	t        *transport
	localID  uint32
	remoteID uint32
	Service  string

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	cond         *sync.Cond
	buf          []byte
	ackPending   bool
	canWrite     bool
	localClosed  bool
	remoteClosed bool
	readDeadline time.Time
	deadlineTmr  *time.Timer
}

func newStream(t *transport, localID, remoteID uint32, service string) *Stream {
	s := &Stream{t: t, localID: localID, remoteID: remoteID, Service: service, canWrite: true}
	s.cond = sync.NewCond(&s.mu)
	s.ctx, s.cancel = context.WithCancel(t.ctx)
	return s
}

// Context is cancelled when the stream or its transport is closed.
func (s *Stream) Context() context.Context { return s.ctx }

func (s *Stream) push(data []byte) {
	s.mu.Lock()
	if s.localClosed {
		s.mu.Unlock()
		return
	}
	s.buf = append(s.buf, data...)
	s.ackPending = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *Stream) okay() {
	s.mu.Lock()
	s.canWrite = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *Stream) remoteClose() {
	s.mu.Lock()
	s.remoteClosed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
}

func (s *Stream) deadlineExceeded() bool {
	return !s.readDeadline.IsZero() && !time.Now().Before(s.readDeadline)
}

func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	for len(s.buf) == 0 && !s.remoteClosed && !s.localClosed && !s.deadlineExceeded() {
		s.cond.Wait()
	}
	if len(s.buf) == 0 {
		defer s.mu.Unlock()
		if s.localClosed {
			return 0, net.ErrClosed
		}
		if s.remoteClosed {
			return 0, io.EOF
		}
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	sendAck := false
	if len(s.buf) == 0 {
		s.buf = nil
		if s.ackPending {
			s.ackPending = false
			sendAck = !s.remoteClosed
		}
	}
	s.mu.Unlock()
	if sendAck {
		s.t.send(&packet{command: cmdOKAY, arg0: s.localID, arg1: s.remoteID})
	}
	return n, nil
}

func (s *Stream) Write(p []byte) (int, error) {
	written := 0
	max := s.t.maxPayload()
	for written < len(p) {
		chunk := p[written:]
		if len(chunk) > max {
			chunk = chunk[:max]
		}
		s.mu.Lock()
		for !s.canWrite && !s.remoteClosed && !s.localClosed {
			s.cond.Wait()
		}
		if s.remoteClosed || s.localClosed {
			s.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		s.canWrite = false
		s.mu.Unlock()
		data := make([]byte, len(chunk))
		copy(data, chunk)
		if err := s.t.send(&packet{command: cmdWRTE, arg0: s.localID, arg1: s.remoteID, data: data}); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	return written, nil
}

// Close sends CLSE to the host once.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.localClosed {
		s.mu.Unlock()
		return nil
	}
	s.localClosed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
	s.t.removeStream(s.localID)
	return s.t.send(&packet{command: cmdCLSE, arg0: s.localID, arg1: s.remoteID})
}

// WaitWritten waits until the host has acknowledged the last WRTE (or the stream is closed), so that a CLSE after it
// does not race with a slow reader on the host.
func (s *Stream) WaitWritten(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	t := time.AfterFunc(timeout, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer t.Stop()
	s.mu.Lock()
	for !s.canWrite && !s.remoteClosed && !s.localClosed && time.Now().Before(deadline) {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

type streamAddr string

func (a streamAddr) Network() string { return "adb" }
func (a streamAddr) String() string  { return string(a) }

func (s *Stream) LocalAddr() net.Addr  { return streamAddr(s.Service) }
func (s *Stream) RemoteAddr() net.Addr { return streamAddr(s.t.conn.RemoteAddr().String()) }

func (s *Stream) SetDeadline(t time.Time) error {
	return s.SetReadDeadline(t)
}

func (s *Stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	if s.deadlineTmr != nil {
		s.deadlineTmr.Stop()
		s.deadlineTmr = nil
	}
	if !t.IsZero() {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		s.deadlineTmr = time.AfterFunc(d, func() {
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		})
	}
	s.cond.Broadcast()
	return nil
}

func (s *Stream) SetWriteDeadline(time.Time) error { return nil }
