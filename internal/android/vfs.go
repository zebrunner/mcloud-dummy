package android

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
)

type vnode struct {
	mode  uint32
	data  []byte
	mtime time.Time
}

// VFS is the in-memory file system of a device.
type VFS struct {
	mu    sync.Mutex
	nodes map[string]*vnode
}

func newVFS() *VFS {
	v := &VFS{nodes: map[string]*vnode{}}
	now := time.Now()
	for _, d := range []string{"/", "/data", "/data/local", "/data/local/tmp", "/data/app", "/data/data",
		"/data/data/com.android.shell", "/data/system", "/sdcard", "/sdcard/Download", "/sdcard/DCIM", "/sdcard/Pictures",
		"/sdcard/Movies", "/storage", "/storage/emulated", "/storage/emulated/0", "/system", "/system/bin",
		"/system/app", "/system/priv-app", "/system/etc", "/vendor", "/proc", "/dev", "/mnt", "/mnt/sdcard", "/cache"} {
		v.nodes[d] = &vnode{mode: adb.SIFDIR | 0o771, mtime: now}
	}
	for _, f := range []string{"/system/bin/sh", "/system/bin/app_process", "/system/bin/screencap", "/system/bin/am",
		"/system/bin/pm", "/system/bin/input", "/system/bin/settings", "/system/bin/dumpsys", "/system/bin/getprop",
		"/system/bin/logcat", "/system/bin/toybox", "/system/bin/cmd", "/system/bin/wm", "/system/bin/ime"} {
		v.nodes[f] = &vnode{mode: adb.SIFREG | 0o755, mtime: now}
	}
	v.nodes["/proc/version"] = &vnode{mode: adb.SIFREG | 0o444, mtime: now,
		data: []byte("Linux version 6.1.0-mcloud-dummy (build@mcloud) #1 SMP PREEMPT\n")}
	return v
}

// Clean normalizes a device path (the shell's current directory is /).
func Clean(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if strings.HasPrefix(p, "/sdcard/") || p == "/sdcard" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, "/storage/emulated/0"); ok {
		return "/sdcard" + rest
	}
	if rest, ok := strings.CutPrefix(p, "/mnt/sdcard"); ok && rest != "" {
		return "/sdcard" + rest
	}
	return p
}

func (v *VFS) mkdirAllLocked(p string) {
	for d := p; d != "/" && d != "."; d = path.Dir(d) {
		if _, ok := v.nodes[d]; ok {
			break
		}
		v.nodes[d] = &vnode{mode: adb.SIFDIR | 0o771, mtime: time.Now()}
	}
}

// Stat of a path.
func (v *VFS) Stat(p string) (adb.FileInfo, error) {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		return adb.FileInfo{}, fs.ErrNotExist
	}
	return adb.FileInfo{Name: path.Base(p), Mode: n.mode, Size: int64(len(n.data)), MTime: n.mtime}, nil
}

// List a directory.
func (v *VFS) List(p string) ([]adb.FileInfo, error) {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if n.mode&adb.SIFMT != adb.SIFDIR {
		return []adb.FileInfo{{Name: path.Base(p), Mode: n.mode, Size: int64(len(n.data)), MTime: n.mtime}}, nil
	}
	out := []adb.FileInfo{{Name: ".", Mode: n.mode, MTime: n.mtime}, {Name: "..", Mode: adb.SIFDIR | 0o771, MTime: n.mtime}}
	prefix := strings.TrimSuffix(p, "/") + "/"
	for name, c := range v.nodes {
		if name == p || !strings.HasPrefix(name, prefix) || strings.Contains(name[len(prefix):], "/") {
			continue
		}
		out = append(out, adb.FileInfo{Name: name[len(prefix):], Mode: c.mode, Size: int64(len(c.data)), MTime: c.mtime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadFile returns the content of a file.
func (v *VFS) ReadFile(p string) ([]byte, error) {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if n.mode&adb.SIFMT == adb.SIFDIR {
		return nil, fs.ErrInvalid
	}
	return n.data, nil
}

// WriteFile creates or replaces a file, creating its directories (fixed_push_mkdir).
func (v *VFS) WriteFile(p string, mode uint32, data []byte, mtime time.Time) error {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	if n, ok := v.nodes[p]; ok && n.mode&adb.SIFMT == adb.SIFDIR {
		return fs.ErrExist
	}
	v.mkdirAllLocked(path.Dir(p))
	if mode&adb.SIFMT == 0 {
		mode |= adb.SIFREG
	}
	v.nodes[p] = &vnode{mode: mode, data: data, mtime: mtime}
	return nil
}

// Append adds data to a file, creating it.
func (v *VFS) Append(p string, data []byte) error {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		v.mkdirAllLocked(path.Dir(p))
		n = &vnode{mode: adb.SIFREG | 0o666}
		v.nodes[p] = n
	}
	if n.mode&adb.SIFMT == adb.SIFDIR {
		return fs.ErrInvalid
	}
	n.data = append(n.data, data...)
	n.mtime = time.Now()
	return nil
}

// Mkdir creates a directory (and its parents).
func (v *VFS) Mkdir(p string) {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.mkdirAllLocked(p)
}

// Remove deletes a path, recursively for a directory.
func (v *VFS) Remove(p string, recursive bool) error {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		return fs.ErrNotExist
	}
	if n.mode&adb.SIFMT == adb.SIFDIR {
		if !recursive {
			return fs.ErrInvalid
		}
		prefix := strings.TrimSuffix(p, "/") + "/"
		for name := range v.nodes {
			if strings.HasPrefix(name, prefix) {
				delete(v.nodes, name)
			}
		}
	}
	if p != "/" {
		delete(v.nodes, p)
	}
	return nil
}

// Chmod changes the permission bits.
func (v *VFS) Chmod(p string, perm uint32) error {
	p = Clean(p)
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[p]
	if !ok {
		return fs.ErrNotExist
	}
	n.mode = n.mode&adb.SIFMT | perm&0o7777
	return nil
}

// Exists tells whether the path exists.
func (v *VFS) Exists(p string) bool {
	_, err := v.Stat(p)
	return err == nil
}

// Usage is the total size of the files.
func (v *VFS) Usage() int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	var total int64
	for _, n := range v.nodes {
		total += int64(len(n.data))
	}
	return total
}
