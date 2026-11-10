package adb

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"sync"
)

// Shell protocol v2 packet ids (packages/modules/adb/shell_protocol.h).
const (
	shellStdin      = 0
	shellStdout     = 1
	shellStderr     = 2
	shellExit       = 3
	shellCloseStdin = 4
	shellWindowSize = 5
)

// ShellRequest is a parsed "shell[,args]:command" or "exec:command" service.
type ShellRequest struct {
	Command string
	V2      bool
	PTY     bool
	Exec    bool
}

// ParseShellService parses the shell and exec services; ok is false for other services.
func ParseShellService(service string) (ShellRequest, bool) {
	if cmd, ok := strings.CutPrefix(service, "exec:"); ok {
		return ShellRequest{Command: cmd, Exec: true}, true
	}
	if !strings.HasPrefix(service, "shell") {
		return ShellRequest{}, false
	}
	head, cmd, found := strings.Cut(service, ":")
	if !found {
		return ShellRequest{}, false
	}
	req := ShellRequest{Command: cmd}
	args := strings.Split(head, ",")
	if args[0] != "shell" {
		return ShellRequest{}, false
	}
	typ := ""
	for _, a := range args[1:] {
		switch {
		case a == "v2":
			req.V2 = true
		case a == "raw", a == "pty":
			typ = a
		}
	}
	// Without an explicit type adbd uses a pty for the interactive shell only.
	req.PTY = typ == "pty" || (typ == "" && cmd == "")
	return req, true
}

// ShellRunner runs a command line; it returns the exit status.
type ShellRunner func(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) int

// ServeShell runs a shell or exec service over the stream with the protocol the host asked for.
func ServeShell(s *Stream, req ShellRequest, run ShellRunner) {
	ctx := s.Context()
	if !req.V2 {
		w := &lockedWriter{w: s}
		var out io.Writer = w
		if req.PTY {
			out = &crlfWriter{w: w}
		}
		run(ctx, req.Command, s, out, out)
		return
	}
	sw := &shellV2Writer{s: s}
	stdinR, stdinW := io.Pipe()
	go func() {
		defer stdinW.Close()
		hdr := make([]byte, 5)
		for {
			if _, err := io.ReadFull(s, hdr); err != nil {
				return
			}
			n := binary.LittleEndian.Uint32(hdr[1:])
			data := make([]byte, n)
			if _, err := io.ReadFull(s, data); err != nil {
				return
			}
			switch hdr[0] {
			case shellStdin:
				if _, err := stdinW.Write(data); err != nil {
					return
				}
			case shellCloseStdin:
				stdinW.Close()
			}
		}
	}()
	var stdout, stderr io.Writer = sw.id(shellStdout), sw.id(shellStderr)
	if req.PTY {
		stdout, stderr = &crlfWriter{w: stdout}, &crlfWriter{w: stdout}
	}
	code := run(ctx, req.Command, stdinR, stdout, stderr)
	stdinR.Close()
	sw.packet(shellExit, []byte{byte(code)})
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type crlfWriter struct{ w io.Writer }

func (c *crlfWriter) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte{'\n'}) {
		return c.w.Write(p)
	}
	if _, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

type shellV2Writer struct {
	mu sync.Mutex
	s  *Stream
}

func (w *shellV2Writer) packet(id byte, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	buf := make([]byte, 5+len(data))
	buf[0] = id
	binary.LittleEndian.PutUint32(buf[1:], uint32(len(data)))
	copy(buf[5:], data)
	_, err := w.s.Write(buf)
	return err
}

type shellV2Stream struct {
	w  *shellV2Writer
	id byte
}

func (w *shellV2Writer) id(id byte) io.Writer { return &shellV2Stream{w: w, id: id} }

func (s *shellV2Stream) Write(p []byte) (int, error) {
	const max = 64 * 1024
	for off := 0; off < len(p); off += max {
		end := min(off+max, len(p))
		if err := s.w.packet(s.id, p[off:end]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}
