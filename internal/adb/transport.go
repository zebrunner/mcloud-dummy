package adb

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// AuthMode is how the device answers the adb authentication of the host.
type AuthMode string

const (
	// AuthNone answers CNXN right away, as adbd with ro.adb.secure=0 (emulators).
	AuthNone AuthMode = "none"
	// AuthAccept asks the host to authenticate and accepts any signature or public key.
	AuthAccept AuthMode = "accept"
	// AuthDeny asks the host to authenticate and never accepts: the device stays "unauthorized".
	AuthDeny AuthMode = "deny"
)

// Handler serves one opened service. The stream is closed when it returns.
type Handler func(s *Stream)

// Device is what the transport needs from the emulated device.
type Device interface {
	// Banner is the payload of CNXN: "device::ro.product.name=...;ro.product.model=...;ro.product.device=...;features=...".
	Banner() string
	// Open resolves a service ("shell:...", "sync:", "localabstract:...", ...); false refuses it (CLSE).
	Open(service string) (Handler, bool)
	// Auth is the current authentication mode.
	Auth() AuthMode
	// Delay is an artificial latency before a service is opened (fault injection).
	Delay() time.Duration
}

type transport struct {
	conn net.Conn
	dev  Device
	log  *slog.Logger
	ctx  context.Context
	stop context.CancelFunc

	wmu sync.Mutex

	mu         sync.Mutex
	streams    map[uint32]*Stream
	nextID     uint32
	peerMax    int
	connected  bool
	authTokens int
}

func newTransport(ctx context.Context, conn net.Conn, dev Device, log *slog.Logger) *transport {
	t := &transport{conn: conn, dev: dev, log: log, streams: map[uint32]*Stream{}, nextID: 1, peerMax: MaxPayload}
	t.ctx, t.stop = context.WithCancel(ctx)
	return t
}

func (t *transport) maxPayload() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.peerMax < MaxPayload {
		return t.peerMax
	}
	return MaxPayload
}

func (t *transport) send(p *packet) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.conn.Write(encodePacket(p))
	return err
}

func (t *transport) removeStream(id uint32) {
	t.mu.Lock()
	delete(t.streams, id)
	t.mu.Unlock()
}

func (t *transport) stream(id uint32) *Stream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streams[id]
}

func (t *transport) close() {
	t.stop()
	t.conn.Close()
	t.mu.Lock()
	streams := make([]*Stream, 0, len(t.streams))
	for _, s := range t.streams {
		streams = append(streams, s)
	}
	t.mu.Unlock()
	for _, s := range streams {
		s.remoteClose()
	}
}

func (t *transport) sendCNXN() error {
	t.mu.Lock()
	t.connected = true
	t.mu.Unlock()
	return t.send(&packet{command: cmdCNXN, arg0: Version, arg1: MaxPayload, data: []byte(t.dev.Banner())})
}

func (t *transport) sendAuthToken() error {
	token := make([]byte, 20)
	_, _ = rand.Read(token)
	t.authTokens++
	return t.send(&packet{command: cmdAUTH, arg0: authToken, data: token})
}

func (t *transport) serve() {
	defer t.close()
	for {
		p, err := readPacket(t.conn)
		if err != nil {
			t.log.Debug("adb transport ended", "err", err)
			return
		}
		if err := t.handle(p); err != nil {
			t.log.Debug("adb transport failed", "err", err)
			return
		}
	}
}

func (t *transport) handle(p *packet) error {
	switch p.command {
	case cmdCNXN:
		t.mu.Lock()
		if int(p.arg1) > 0 {
			t.peerMax = int(p.arg1)
		}
		t.mu.Unlock()
		t.log.Debug("adb host connected", "banner", strings.TrimRight(string(p.data), "\x00"))
		if t.dev.Auth() == AuthNone {
			return t.sendCNXN()
		}
		return t.sendAuthToken()
	case cmdAUTH:
		switch t.dev.Auth() {
		case AuthNone, AuthAccept:
			return t.sendCNXN()
		default:
			// The host tries its keys (SIGNATURE) and finally offers its public key, then it waits for the user to
			// confirm the dialog on the device: never answering keeps it "unauthorized".
			if p.arg0 == authSignature {
				return t.sendAuthToken()
			}
			return nil
		}
	case cmdSTLS:
		return nil
	case cmdOPEN:
		t.mu.Lock()
		connected := t.connected
		t.mu.Unlock()
		if !connected {
			return nil
		}
		service := strings.TrimRight(string(p.data), "\x00")
		remoteID := p.arg0
		go t.open(service, remoteID)
		return nil
	case cmdOKAY:
		if s := t.stream(p.arg1); s != nil {
			s.okay()
		}
		return nil
	case cmdWRTE:
		s := t.stream(p.arg1)
		if s == nil {
			return t.send(&packet{command: cmdCLSE, arg0: 0, arg1: p.arg0})
		}
		s.push(p.data)
		return nil
	case cmdCLSE:
		if s := t.stream(p.arg1); s != nil {
			t.removeStream(s.localID)
			s.remoteClose()
		}
		return nil
	case cmdSYNC:
		return nil
	}
	t.log.Debug("adb unknown packet", "packet", p.String())
	return nil
}

func (t *transport) open(service string, remoteID uint32) {
	if d := t.dev.Delay(); d > 0 {
		select {
		case <-time.After(d):
		case <-t.ctx.Done():
			return
		}
	}
	h, ok := t.dev.Open(service)
	if !ok {
		t.log.Debug("adb service refused", "service", service)
		_ = t.send(&packet{command: cmdCLSE, arg0: 0, arg1: remoteID})
		return
	}
	t.mu.Lock()
	id := t.nextID
	t.nextID++
	s := newStream(t, id, remoteID, service)
	t.streams[id] = s
	t.mu.Unlock()
	if err := t.send(&packet{command: cmdOKAY, arg0: id, arg1: remoteID}); err != nil {
		t.removeStream(id)
		return
	}
	t.log.Debug("adb service opened", "service", service, "id", id)
	defer func() {
		if r := recover(); r != nil {
			t.log.Error("adb service panicked", "service", service, "panic", r)
		}
		s.WaitWritten(5 * time.Second)
		s.Close()
	}()
	h(s)
}

// Server accepts the adb server connections of one device.
type Server struct {
	Addr string
	Dev  Device
	Log  *slog.Logger

	mu         sync.Mutex
	ln         net.Listener
	transports map[*transport]struct{}
	ctx        context.Context
	cancel     context.CancelFunc
}

// Start listens on Addr; Stop closes the listener and every connection (the device "disappears").
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if s.transports == nil {
		s.transports = map[*transport]struct{}{}
	}
	go s.accept(ln, s.ctx)
	return nil
}

func (s *Server) accept(ln net.Listener, ctx context.Context) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
		}
		t := newTransport(ctx, conn, s.Dev, s.Log)
		s.mu.Lock()
		s.transports[t] = struct{}{}
		s.mu.Unlock()
		go func() {
			t.serve()
			s.mu.Lock()
			delete(s.transports, t)
			s.mu.Unlock()
		}()
	}
}

// Stop closes the listener and drops every transport.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	ts := make([]*transport, 0, len(s.transports))
	for t := range s.transports {
		ts = append(ts, t)
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, t := range ts {
		t.close()
	}
}

// Disconnect drops the current transports but keeps listening: the host sees the device offline and reconnects.
func (s *Server) Disconnect() {
	s.mu.Lock()
	ts := make([]*transport, 0, len(s.transports))
	for t := range s.transports {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	for _, t := range ts {
		t.close()
	}
}

// Connections is the number of the connected adb servers.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.transports)
}
