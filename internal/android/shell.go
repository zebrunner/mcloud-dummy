package android

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
)

// Cmd is the context of one command run by the shell.
type Cmd struct {
	d      *Device
	ctx    context.Context
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    expand.Environ
	Dir    string
}

// Getenv reads a variable of the shell.
func (c *Cmd) Getenv(name string) string { return c.Env.Get(name).String() }

func (c *Cmd) printf(format string, args ...any) { _, _ = fmt.Fprintf(c.Stdout, format, args...) }

func (c *Cmd) errorf(format string, args ...any) { _, _ = fmt.Fprintf(c.Stderr, format, args...) }

func (c *Cmd) abs(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = path.Join(c.Dir, p)
	}
	return Clean(p)
}

type command func(c *Cmd) int

var commands map[string]command

func init() {
	commands = map[string]command{
		"sleep": cmdSleep, "id": cmdID, "whoami": cmdWhoami, "uname": cmdUname, "date": cmdDate,
		"ls": cmdLs, "cat": cmdCat, "rm": cmdRm, "mkdir": cmdMkdir, "chmod": cmdChmod, "touch": cmdTouch,
		"cp": cmdCp, "mv": cmdMv, "df": cmdDf, "du": cmdDu, "which": cmdWhich, "grep": cmdGrep, "head": cmdHead,
		"tail": cmdTail, "wc": cmdWc, "md5sum": cmdSum, "sha1sum": cmdSum, "sha256sum": cmdSum, "chown": cmdNop,
		"sync": cmdNop, "getenforce": cmdGetenforce, "setenforce": cmdNop, "env": cmdEnv, "printenv": cmdEnv,
		"getprop": cmdGetprop, "setprop": cmdSetprop, "settings": cmdSettings, "pm": cmdPm, "cmd": cmdCmd,
		"am": cmdAm, "dumpsys": cmdDumpsys, "wm": cmdWm, "input": cmdInput, "screencap": cmdScreencap,
		"ime": cmdIme, "appops": cmdNop, "svc": cmdSvc, "content": cmdContent, "service": cmdService,
		"logcat": cmdLogcat, "log": cmdLog, "monkey": cmdMonkey, "uiautomator": cmdUiautomator,
		"app_process": cmdAppProcess, "ps": cmdPs, "pidof": cmdPidof, "pgrep": cmdPgrep, "kill": cmdKill,
		"killall": cmdKillall, "reboot": cmdReboot, "toybox": cmdToybox, "toolbox": cmdToybox, "sh": cmdSh,
		"stat": cmdStat, "readlink": cmdReadlink, "realpath": cmdReadlink, "basename": cmdBasename,
		"dirname": cmdDirname, "nproc": cmdNproc, "getconf": cmdGetconf, "cut": cmdCut, "tr": cmdTr,
		"sort": cmdSortCmd, "uptime": cmdUptime, "free": cmdFree, "netstat": cmdNop, "ip": cmdIP,
		"ifconfig": cmdIP, "ping": cmdPing, "stty": cmdNop, "base64": cmdBase64, "xargs": cmdXargs,
		"am_wait": cmdNop, "cmd_wait": cmdNop, "find": cmdFind, "tee": cmdTee, "seq": cmdSeq,
		"dd": cmdNop, "mount": cmdNop, "umount": cmdNop, "su": cmdSu, "run-as": cmdRunAs, "bmgr": cmdNop,
		"atrace": cmdNop, "screenrecord": cmdScreenrecord, "ln": cmdNop, "unzip": cmdNop, "gzip": cmdNop,
		"wait": cmdNop, "pwd": cmdPwd, "start": cmdNop, "stop": cmdNop, "locksettings": cmdLocksettings,
		"vdc": cmdNop, "sm": cmdNop, "umask": cmdNop, "jobs": cmdNop, "bg": cmdNop, "fg": cmdNop,
	}
}

// binaries pushed by STF and started by path.
var binaries = map[string]command{
	"minicap":   cmdMinicap,
	"minitouch": cmdMinitouch,
	"minirev":   cmdMinirev,
}

func (d *Device) runShell(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) int {
	if strings.TrimSpace(command) == "" {
		return d.interactive(ctx, stdin, stdout, stderr)
	}
	start := time.Now()
	code := d.runScript(ctx, command, stdin, stdout, stderr, nil)
	d.recordCommand(command, code, time.Since(start))
	return code
}

// CommandRecord is a shell command run by the host, for the control API (what Appium and STF ask the device).
type CommandRecord struct {
	At       time.Time     `json:"at"`
	Command  string        `json:"command"`
	Exit     int           `json:"exit"`
	Duration time.Duration `json:"duration"`
}

const maxCommands = 500

func (d *Device) recordCommand(command string, code int, dur time.Duration) {
	d.Log.Debug("shell", "command", command, "exit", code, "duration", dur)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands = append(d.commands, CommandRecord{At: time.Now(), Command: command, Exit: code, Duration: dur})
	if len(d.commands) > maxCommands {
		d.commands = d.commands[len(d.commands)-maxCommands:]
	}
}

// Commands returns the recent shell commands, the newest last.
func (d *Device) Commands() []CommandRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]CommandRecord(nil), d.commands...)
}

func (d *Device) env(extra []string) expand.Environ {
	vars := []string{
		"PATH=/product/bin:/apex/com.android.runtime/bin:/apex/com.android.art/bin:/system_ext/bin:/system/bin:/system/xbin:/odm/bin:/vendor/bin:/vendor/xbin",
		"HOME=/", "USER=shell", "LOGNAME=shell", "SHELL=/system/bin/sh", "HOSTNAME=" + d.Cfg.Device,
		"TMPDIR=/data/local/tmp", "EXTERNAL_STORAGE=/sdcard", "ANDROID_ROOT=/system", "ANDROID_DATA=/data",
		"ANDROID_STORAGE=/storage", "BOOTCLASSPATH=/apex/com.android.art/javalib/core-oj.jar",
	}
	return expand.ListEnviron(append(vars, extra...)...)
}

func (d *Device) runScript(ctx context.Context, script string, stdin io.Reader, stdout, stderr io.Writer, params []string) int {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		fmt.Fprintf(stderr, "/system/bin/sh: syntax error: %v\n", err)
		return 2
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	opts := []interp.RunnerOption{
		interp.StdIO(stdin, stdout, stderr),
		interp.Env(d.env(nil)),
		interp.Dir("/"),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc { return d.exec }),
		interp.OpenHandler(d.open),
		interp.StatHandler(d.stat),
		interp.ReadDirHandler2(d.readDir),
		interp.AccessHandler(d.access),
		interp.CallHandler(overrideBuiltins),
	}
	if len(params) > 0 {
		opts = append(opts, interp.Params(append([]string{"--"}, params...)...))
	}
	r, err := interp.New(opts...)
	if err != nil {
		fmt.Fprintf(stderr, "/system/bin/sh: %v\n", err)
		return 1
	}
	err = r.Run(ctx, file)
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	if ctx.Err() != nil {
		return 130
	}
	fmt.Fprintf(stderr, "/system/bin/sh: %v\n", err)
	return 1
}

func (d *Device) interactive(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	prompt := fmt.Sprintf("%s:/ $ ", d.Cfg.Device)
	fmt.Fprint(stdout, prompt)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "exit" {
			return 0
		}
		if strings.TrimSpace(line) != "" {
			d.runScript(ctx, line, strings.NewReader(""), stdout, stderr, nil)
		}
		fmt.Fprint(stdout, prompt)
	}
	return 0
}

func (d *Device) exec(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	c := &Cmd{d: d, ctx: ctx, Args: args, Stdin: hc.Stdin, Stdout: hc.Stdout, Stderr: hc.Stderr, Env: hc.Env, Dir: hc.Dir}
	code := d.dispatch(c)
	if code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

func (d *Device) dispatch(c *Cmd) int {
	c.Args[0] = strings.TrimPrefix(c.Args[0], builtinPrefix)
	name := c.Args[0]
	base := path.Base(name)
	if strings.Contains(name, "/") && !strings.HasPrefix(name, "/system/") && !strings.HasPrefix(name, "/vendor/") &&
		!strings.HasPrefix(name, "/apex/") && !strings.HasPrefix(name, "/product/") {
		fi, err := d.fs.Stat(c.abs(name))
		if err != nil {
			c.errorf("/system/bin/sh: %s: inaccessible or not found\n", name)
			return 127
		}
		if fi.Mode&0o111 == 0 {
			c.errorf("/system/bin/sh: %s: can't execute: Permission denied\n", name)
			return 126
		}
		if b, ok := binaries[base]; ok {
			return b(c)
		}
		if data, err := d.fs.ReadFile(c.abs(name)); err == nil && bytes.HasPrefix(data, []byte("#!")) {
			return d.runScript(c.ctx, string(data), c.Stdin, c.Stdout, c.Stderr, c.Args[1:])
		}
		c.errorf("/system/bin/sh: %s: not executable: 64-bit ELF file\n", name)
		return 126
	}
	if cmd, ok := commands[base]; ok {
		return cmd(c)
	}
	d.App.Log("shell", "unknown", "%s", strings.Join(c.Args, " "))
	c.errorf("/system/bin/sh: %s: inaccessible or not found\n", name)
	return 127
}

// builtinPrefix renames the builtins the interpreter knows but does not implement (kill, umask...): the call reaches
// the exec handler, which runs the command of the device.
const builtinPrefix = "\x00dummy:"

var overridden = map[string]bool{"kill": true, "umask": true, "jobs": true, "bg": true, "fg": true}

func overrideBuiltins(ctx context.Context, args []string) ([]string, error) {
	if overridden[args[0]] {
		out := append([]string{builtinPrefix + args[0]}, args[1:]...)
		return out, nil
	}
	return args, nil
}

// open is the redirection handler of the shell: /dev/null and the files of the VFS.
func (d *Device) open(ctx context.Context, p string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	switch p {
	case "/dev/null":
		return devNull{}, nil
	case "/dev/zero", "/dev/random", "/dev/urandom":
		return devNull{}, nil
	}
	hc := interp.HandlerCtx(ctx)
	if !strings.HasPrefix(p, "/") {
		p = path.Join(hc.Dir, p)
	}
	p = Clean(p)
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		if flag&os.O_TRUNC != 0 || !d.fs.Exists(p) {
			if err := d.fs.WriteFile(p, adb.SIFREG|0o666, nil, time.Now()); err != nil {
				return nil, &fs.PathError{Op: "open", Path: p, Err: err}
			}
		}
		return &vfsWriter{fs: d.fs, path: p}, nil
	}
	data, err := d.fs.ReadFile(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return &vfsReader{Reader: bytes.NewReader(data)}, nil
}

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

type vfsWriter struct {
	fs   *VFS
	path string
}

func (w *vfsWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (w *vfsWriter) Write(p []byte) (int, error) {
	if err := w.fs.Append(w.path, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *vfsWriter) Close() error { return nil }

type vfsReader struct{ *bytes.Reader }

func (r *vfsReader) Write([]byte) (int, error) { return 0, fs.ErrPermission }
func (r *vfsReader) Close() error              { return nil }

type fileInfo struct{ fi adb.FileInfo }

func (f fileInfo) Name() string       { return f.fi.Name }
func (f fileInfo) Size() int64        { return f.fi.Size }
func (f fileInfo) ModTime() time.Time { return f.fi.MTime }
func (f fileInfo) IsDir() bool        { return f.fi.Mode&adb.SIFMT == adb.SIFDIR }
func (f fileInfo) Sys() any           { return nil }
func (f fileInfo) Mode() fs.FileMode {
	m := fs.FileMode(f.fi.Mode & 0o777)
	if f.IsDir() {
		m |= fs.ModeDir
	}
	return m
}

func (d *Device) stat(ctx context.Context, name string, _ bool) (fs.FileInfo, error) {
	if !strings.HasPrefix(name, "/") {
		name = path.Join(interp.HandlerCtx(ctx).Dir, name)
	}
	if name == "/dev/null" {
		return fileInfo{adb.FileInfo{Name: "null", Mode: 0o20666}}, nil
	}
	fi, err := d.fs.Stat(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fileInfo{fi}, nil
}

// access checks the permission bits of the VFS for the test operators -r, -w and -x.
func (d *Device) access(ctx context.Context, name string, mode interp.AccessMode) error {
	fi, err := d.fs.Stat(name)
	if err != nil {
		return &fs.PathError{Op: "access", Path: name, Err: fs.ErrNotExist}
	}
	if mode&interp.AccessExec != 0 && fi.Mode&0o111 == 0 {
		return &fs.PathError{Op: "access", Path: name, Err: fs.ErrPermission}
	}
	return nil
}

type dirEntry struct{ fileInfo }

func (e dirEntry) Type() fs.FileMode          { return e.Mode().Type() }
func (e dirEntry) Info() (fs.FileInfo, error) { return e.fileInfo, nil }

func (d *Device) readDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if !strings.HasPrefix(name, "/") {
		name = path.Join(interp.HandlerCtx(ctx).Dir, name)
	}
	list, err := d.fs.List(name)
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	for _, fi := range list {
		if fi.Name == "." || fi.Name == ".." {
			continue
		}
		out = append(out, dirEntry{fileInfo{fi}})
	}
	return out, nil
}

// waitDone blocks until the context of the command or the process ends; it returns the exit code.
func waitDone(c *Cmd, p *Proc) int {
	select {
	case <-c.ctx.Done():
		return 130
	case <-p.Done():
		return p.ExitCode()
	}
}

// cmdSvc is svc wifi|data enable|disable.
func cmdSvc(c *Cmd) int {
	if len(c.Args) < 3 {
		return 0
	}
	on := c.Args[2] == "enable"
	app := c.d.App
	switch c.Args[1] {
	case "wifi":
		setWifi(app, "svc", on)
	case "data":
		switch {
		case on && app.Network() == "none":
			app.SetNetwork("svc", "cellular")
		case !on && app.Network() == "cellular":
			app.SetNetwork("svc", "none")
		}
	}
	return 0
}
