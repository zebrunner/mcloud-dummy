package adb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// memFS is a tiny FS for the sync tests.
type memFS struct {
	mu    sync.Mutex
	files map[string]FileInfo
	data  map[string][]byte
}

func newMemFS() *memFS { return &memFS{files: map[string]FileInfo{}, data: map[string][]byte{}} }

func (m *memFS) Stat(p string) (FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fi, ok := m.files[p]
	if !ok {
		return FileInfo{}, fs.ErrNotExist
	}
	return fi, nil
}

func (m *memFS) List(p string) ([]FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FileInfo
	for name, fi := range m.files {
		if strings.HasPrefix(name, p+"/") {
			fi.Name = name[len(p)+1:]
			out = append(out, fi)
		}
	}
	return out, nil
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return d, nil
}

func (m *memFS) WriteFile(p string, mode uint32, data []byte, mtime time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = FileInfo{Name: p, Mode: mode, Size: int64(len(data)), MTime: mtime}
	m.data[p] = data
	return nil
}

// host is a minimal adb host: what the adb server speaks to a device.
type host struct {
	t    *testing.T
	conn net.Conn
	next uint32
}

func (h *host) send(cmd, a0, a1 uint32, data []byte) {
	h.t.Helper()
	if _, err := h.conn.Write(encodePacket(&packet{command: cmd, arg0: a0, arg1: a1, data: data})); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) recv() *packet {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	p, err := readPacket(h.conn)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return p
}

// open opens a service and returns the remote id.
func (h *host) open(service string) (local, remote uint32) {
	h.t.Helper()
	h.next++
	h.send(cmdOPEN, h.next, 0, append([]byte(service), 0))
	p := h.recv()
	if p.command != cmdOKAY {
		h.t.Fatalf("open %s: got %s", service, p)
	}
	return h.next, p.arg0
}

// readAll reads the WRTE packets of a stream until CLSE, acknowledging each.
func (h *host) readAll(local, remote uint32) []byte {
	h.t.Helper()
	var out []byte
	for {
		p := h.recv()
		switch p.command {
		case cmdWRTE:
			out = append(out, p.data...)
			h.send(cmdOKAY, local, remote, nil)
		case cmdCLSE:
			return out
		case cmdOKAY:
		default:
			h.t.Fatalf("unexpected %s", p)
		}
	}
}

func startServer(t *testing.T, dev Device) *host {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	srv := &Server{Addr: addr, Dev: dev, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &host{t: t, conn: conn}
}

func connect(h *host) *packet {
	h.send(cmdCNXN, Version, MaxPayload, []byte("host::features=shell_v2\x00"))
	return h.recv()
}

type shellFunc func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int

func shellDevice(f shellFunc, fsys *memFS) *funcDevice {
	return &funcDevice{shell: f, fs: fsys, auth: AuthNone}
}

type funcDevice struct {
	shell shellFunc
	fs    *memFS
	auth  AuthMode
}

func (d *funcDevice) Banner() string       { return "device::ro.product.model=Test;features=shell_v2,cmd" }
func (d *funcDevice) Auth() AuthMode       { return d.auth }
func (d *funcDevice) Delay() time.Duration { return 0 }
func (d *funcDevice) Open(service string) (Handler, bool) {
	if req, ok := ParseShellService(service); ok {
		return func(s *Stream) {
			ServeShell(s, req, func(_ context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
				return d.shell(cmd, stdin, stdout, stderr)
			})
		}, true
	}
	if service == "sync:" {
		return func(s *Stream) { ServeSync(s, d.fs) }, true
	}
	return nil, false
}

func TestHandshakeWithoutAuth(t *testing.T) {
	h := startServer(t, shellDevice(nil, newMemFS()))
	p := connect(h)
	if p.command != cmdCNXN || p.arg0 != Version || !strings.HasPrefix(string(p.data), "device::") {
		t.Fatalf("expected CNXN with the banner, got %s %q", p, p.data)
	}
}

func TestAuthDenyStaysUnauthorized(t *testing.T) {
	d := shellDevice(nil, newMemFS())
	d.auth = AuthDeny
	h := startServer(t, d)
	if p := connect(h); p.command != cmdAUTH || p.arg0 != authToken {
		t.Fatalf("expected AUTH TOKEN, got %s", p)
	}
	h.send(cmdAUTH, authSignature, 0, make([]byte, 256))
	if p := h.recv(); p.command != cmdAUTH || p.arg0 != authToken {
		t.Fatalf("expected another AUTH TOKEN after a signature, got %s", p)
	}
	h.send(cmdAUTH, authRSAPublicKey, 0, []byte("key\x00"))
	_ = h.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if p, err := readPacket(h.conn); err == nil {
		t.Fatalf("expected no answer to the public key (the user never confirms), got %s", p)
	}
}

func TestAuthAccept(t *testing.T) {
	d := shellDevice(nil, newMemFS())
	d.auth = AuthAccept
	h := startServer(t, d)
	if p := connect(h); p.command != cmdAUTH {
		t.Fatalf("expected AUTH, got %s", p)
	}
	h.send(cmdAUTH, authSignature, 0, make([]byte, 256))
	if p := h.recv(); p.command != cmdCNXN {
		t.Fatalf("expected CNXN after the signature, got %s", p)
	}
}

func TestShellV1MergesOutput(t *testing.T) {
	h := startServer(t, shellDevice(func(cmd string, _ io.Reader, out, errw io.Writer) int {
		fmt.Fprintf(out, "out:%s\n", cmd)
		fmt.Fprint(errw, "err\n")
		return 3
	}, newMemFS()))
	connect(h)
	l, r := h.open("shell:getprop x")
	if got := string(h.readAll(l, r)); got != "out:getprop x\nerr\n" {
		t.Fatalf("got %q", got)
	}
}

func TestShellV2ExitCodeAndLargeOutput(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789"), 70_000) // larger than MaxPayload: several WRTE with flow control
	h := startServer(t, shellDevice(func(cmd string, _ io.Reader, out, errw io.Writer) int {
		out.Write(big)
		fmt.Fprint(errw, "warn")
		return 42
	}, newMemFS()))
	connect(h)
	l, r := h.open("shell,v2,raw:cat big")
	data := h.readAll(l, r)
	var stdout, stderr []byte
	exit := -1
	for len(data) >= 5 {
		id, n := data[0], binary.LittleEndian.Uint32(data[1:])
		payload := data[5 : 5+n]
		switch id {
		case shellStdout:
			stdout = append(stdout, payload...)
		case shellStderr:
			stderr = append(stderr, payload...)
		case shellExit:
			exit = int(payload[0])
		}
		data = data[5+n:]
	}
	if !bytes.Equal(stdout, big) || string(stderr) != "warn" || exit != 42 {
		t.Fatalf("stdout %d bytes (want %d), stderr %q, exit %d", len(stdout), len(big), stderr, exit)
	}
}

func TestShellV2Stdin(t *testing.T) {
	h := startServer(t, shellDevice(func(cmd string, in io.Reader, out, _ io.Writer) int {
		data, _ := io.ReadAll(in)
		fmt.Fprintf(out, "%d", len(data))
		return 0
	}, newMemFS()))
	connect(h)
	l, r := h.open("shell,v2,raw:wc -c")
	payload := bytes.Repeat([]byte("x"), 300_000)
	for off := 0; off < len(payload); off += 100_000 {
		chunk := payload[off : off+100_000]
		pkt := append([]byte{shellStdin, 0, 0, 0, 0}, chunk...)
		binary.LittleEndian.PutUint32(pkt[1:], uint32(len(chunk)))
		h.send(cmdWRTE, l, r, pkt)
		if p := h.recv(); p.command != cmdOKAY {
			t.Fatalf("expected OKAY for the stdin packet, got %s", p)
		}
	}
	h.send(cmdWRTE, l, r, []byte{shellCloseStdin, 0, 0, 0, 0})
	h.recv()
	out := h.readAll(l, r)
	if !bytes.Contains(out, []byte("300000")) {
		t.Fatalf("got %q", out)
	}
}

func TestSyncPushStatPull(t *testing.T) {
	fsys := newMemFS()
	h := startServer(t, shellDevice(nil, fsys))
	connect(h)
	l, r := h.open("sync:")
	le := func(vs ...uint32) []byte {
		b := make([]byte, 4*len(vs))
		for i, v := range vs {
			binary.LittleEndian.PutUint32(b[4*i:], v)
		}
		return b
	}
	write := func(data []byte) {
		h.send(cmdWRTE, l, r, data)
		if p := h.recv(); p.command != cmdOKAY {
			t.Fatalf("expected OKAY, got %s", p)
		}
	}
	var pending []byte
	readN := func(n int) []byte {
		for len(pending) < n {
			p := h.recv()
			if p.command == cmdWRTE {
				pending = append(pending, p.data...)
				h.send(cmdOKAY, l, r, nil)
			}
		}
		out := pending[:n]
		pending = pending[n:]
		return out
	}
	path := "/data/local/tmp/minitouch"
	spec := fmt.Sprintf("%s,%d", path, SIFREG|0o755)
	content := bytes.Repeat([]byte{0x7f, 'E', 'L', 'F'}, 50_000)
	write(append(le(idSEND, uint32(len(spec))), spec...))
	for off := 0; off < len(content); off += 64 * 1024 {
		chunk := content[off:min(off+64*1024, len(content))]
		write(append(le(idDATA, uint32(len(chunk))), chunk...))
	}
	write(le(idDONE, 1700000000))
	if got := readN(8); binary.LittleEndian.Uint32(got) != idOKAY {
		t.Fatalf("push: %q", got)
	}
	write(append(le(idSTAT, uint32(len(path))), path...))
	st := readN(16)
	if mode := binary.LittleEndian.Uint32(st[4:]); mode&0o100 == 0 || binary.LittleEndian.Uint32(st[8:]) != uint32(len(content)) {
		t.Fatalf("stat: mode %o size %d", mode, binary.LittleEndian.Uint32(st[8:]))
	}
	write(append(le(idRECV, uint32(len(path))), path...))
	var pulled []byte
	for {
		hdr := readN(8)
		id, n := binary.LittleEndian.Uint32(hdr), binary.LittleEndian.Uint32(hdr[4:])
		if id == idDONE {
			break
		}
		pulled = append(pulled, readN(int(n))...)
	}
	if !bytes.Equal(pulled, content) {
		t.Fatalf("pulled %d bytes, want %d", len(pulled), len(content))
	}
}

func TestRefusedServiceIsClosed(t *testing.T) {
	h := startServer(t, shellDevice(nil, newMemFS()))
	connect(h)
	h.send(cmdOPEN, 7, 0, []byte("localabstract:minicap\x00"))
	if p := h.recv(); p.command != cmdCLSE || p.arg1 != 7 {
		t.Fatalf("expected CLSE for an unknown socket, got %s", p)
	}
}

func TestParseShellService(t *testing.T) {
	for _, c := range []struct {
		in   string
		want ShellRequest
	}{
		{"shell:ls", ShellRequest{Command: "ls"}},
		{"shell:", ShellRequest{PTY: true}},
		{"shell,v2,raw:getprop", ShellRequest{Command: "getprop", V2: true}},
		{"shell,v2,TERM=xterm,pty:top", ShellRequest{Command: "top", V2: true, PTY: true}},
		{"exec:cmd package install -S 10", ShellRequest{Command: "cmd package install -S 10", Exec: true}},
	} {
		got, ok := ParseShellService(c.in)
		if !ok || got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
	if _, ok := ParseShellService("sync:"); ok {
		t.Error("sync: is not a shell service")
	}
}

// pendingListener is a listener whose pending Accept returns its connection after Close, as a TCP listener does when
// a connection arrives while it is being closed.
type pendingListener struct{ conns chan net.Conn }

func (l *pendingListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *pendingListener) Close() error   { return nil }
func (l *pendingListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestConnectionAcceptedDuringStopIsClosed(t *testing.T) {
	srv := &Server{Dev: shellDevice(nil, newMemFS()), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: map[*transport]struct{}{}}
	ln := &pendingListener{conns: make(chan net.Conn)}
	srv.ln = ln
	srv.ctx, srv.cancel = context.WithCancel(context.Background())
	accepted := make(chan struct{})
	go func() {
		srv.accept(ln, srv.ctx)
		close(accepted)
	}()
	srv.Stop()
	client, conn := net.Pipe()
	defer client.Close()
	ln.conns <- conn
	close(ln.conns)
	<-accepted
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the connection accepted during Stop is closed: %v", err)
	}
	if n := srv.Connections(); n != 0 {
		t.Fatalf("a stopped device has no connections: %d", n)
	}
}
