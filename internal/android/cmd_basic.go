package android

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
)

// splitFlags separates the "-x" options from the operands.
func splitFlags(args []string) (flags map[string]bool, rest []string) {
	flags = map[string]bool{}
	for _, a := range args {
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			flags[a] = true
		} else if strings.HasPrefix(a, "-") && len(a) > 1 {
			for _, ch := range a[1:] {
				flags[string(ch)] = true
			}
		} else {
			rest = append(rest, a)
		}
	}
	return flags, rest
}

func cmdNop(c *Cmd) int { return 0 }

func cmdSleep(c *Cmd) int {
	if len(c.Args) < 2 {
		return 1
	}
	secs, err := strconv.ParseFloat(strings.TrimSuffix(c.Args[1], "s"), 64)
	if err != nil {
		c.errorf("sleep: invalid number '%s'\n", c.Args[1])
		return 1
	}
	select {
	case <-time.After(time.Duration(secs * float64(time.Second))):
		return 0
	case <-c.ctx.Done():
		return 130
	}
}

func cmdID(c *Cmd) int {
	flags, _ := splitFlags(c.Args[1:])
	switch {
	case flags["u"]:
		c.printf("2000\n")
	case flags["g"]:
		c.printf("2000\n")
	case flags["n"]:
		c.printf("shell\n")
	default:
		c.printf("uid=2000(shell) gid=2000(shell) groups=2000(shell),1004(input),1007(log),1011(adb),1015(sdcard_rw),1028(sdcard_r),3001(net_bt_admin),3002(net_bt),3003(inet),3006(net_bw_stats),3009(readproc),3011(uhid) context=u:r:shell:s0\n")
	}
	return 0
}

func cmdWhoami(c *Cmd) int { c.printf("shell\n"); return 0 }

func cmdPwd(c *Cmd) int { c.printf("%s\n", c.Dir); return 0 }

func cmdUname(c *Cmd) int {
	flags, _ := splitFlags(c.Args[1:])
	arch := "x86_64"
	if strings.HasPrefix(c.d.Cfg.ABI, "arm64") {
		arch = "aarch64"
	}
	switch {
	case flags["a"]:
		c.printf("Linux localhost 6.1.0-mcloud-dummy #1 SMP PREEMPT %s %s Toybox\n", time.Now().Format("Mon Jan 2 2006"), arch)
	case flags["r"]:
		c.printf("6.1.0-mcloud-dummy\n")
	case flags["m"]:
		c.printf("%s\n", arch)
	default:
		c.printf("Linux\n")
	}
	return 0
}

func cmdDate(c *Cmd) int {
	now := time.Now()
	for _, a := range c.Args[1:] {
		if strings.HasPrefix(a, "+") {
			f := a[1:]
			r := strings.NewReplacer("%s", strconv.FormatInt(now.Unix(), 10), "%Y", now.Format("2006"), "%m", now.Format("01"),
				"%d", now.Format("02"), "%H", now.Format("15"), "%M", now.Format("04"), "%S", now.Format("05"),
				"%z", now.Format("-0700"), "%Z", now.Format("MST"), "%N", fmt.Sprintf("%09d", now.Nanosecond()),
				"%T", now.Format("15:04:05"), "%F", now.Format("2006-01-02"))
			c.printf("%s\n", r.Replace(f))
			return 0
		}
	}
	c.printf("%s\n", now.Format("Mon Jan  2 15:04:05 MST 2006"))
	return 0
}

func modeString(m uint32) string {
	var b strings.Builder
	switch m & adb.SIFMT {
	case adb.SIFDIR:
		b.WriteByte('d')
	case adb.SIFLNK:
		b.WriteByte('l')
	default:
		b.WriteByte('-')
	}
	for i := 8; i >= 0; i-- {
		if m&(1<<uint(i)) != 0 {
			b.WriteByte("rwxrwxrwx"[8-i])
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func cmdLs(c *Cmd) int {
	flags, paths := splitFlags(c.Args[1:])
	if len(paths) == 0 {
		paths = []string{c.Dir}
	}
	code := 0
	long := flags["l"] || flags["n"]
	for _, p := range paths {
		fi, err := c.d.fs.Stat(c.abs(p))
		if err != nil {
			c.errorf("ls: %s: No such file or directory\n", p)
			code = 1
			continue
		}
		entries := []adb.FileInfo{fi}
		if fi.Mode&adb.SIFMT == adb.SIFDIR && !flags["d"] {
			entries, _ = c.d.fs.List(c.abs(p))
			if !flags["a"] {
				var keep []adb.FileInfo
				for _, e := range entries {
					if !strings.HasPrefix(e.Name, ".") {
						keep = append(keep, e)
					}
				}
				entries = keep
			}
		} else {
			entries[0].Name = p
		}
		if long && fi.Mode&adb.SIFMT == adb.SIFDIR && !flags["d"] {
			c.printf("total %d\n", len(entries))
		}
		for _, e := range entries {
			if long {
				c.printf("%s 1 shell shell %8d %s %s\n", modeString(e.Mode), e.Size, e.MTime.Format("2006-01-02 15:04"), e.Name)
			} else {
				c.printf("%s\n", e.Name)
			}
		}
	}
	return code
}

func cmdCat(c *Cmd) int {
	_, paths := splitFlags(c.Args[1:])
	if len(paths) == 0 {
		_, _ = io.Copy(c.Stdout, c.Stdin)
		return 0
	}
	code := 0
	for _, p := range paths {
		if p == "/proc/meminfo" {
			c.printf("MemTotal:        3915448 kB\nMemFree:         1532120 kB\nMemAvailable:    2615448 kB\n")
			continue
		}
		if p == "/proc/cpuinfo" {
			c.printf("processor\t: 0\nprocessor\t: 1\nprocessor\t: 2\nprocessor\t: 3\nHardware\t: dummy\n")
			continue
		}
		data, err := c.d.fs.ReadFile(c.abs(p))
		if err != nil {
			c.errorf("cat: %s: No such file or directory\n", p)
			code = 1
			continue
		}
		_, _ = c.Stdout.Write(data)
	}
	return code
}

func cmdRm(c *Cmd) int {
	flags, paths := splitFlags(c.Args[1:])
	code := 0
	for _, p := range paths {
		matches := c.d.glob(c.abs(p))
		if len(matches) == 0 && !flags["f"] {
			c.errorf("rm: %s: No such file or directory\n", p)
			code = 1
		}
		for _, m := range matches {
			if err := c.d.fs.Remove(m, flags["r"] || flags["R"]); err != nil && !flags["f"] {
				c.errorf("rm: %s: %v\n", p, err)
				code = 1
			}
		}
	}
	return code
}

// glob expands a pattern of the last path element (the shell already expands the unquoted ones).
func (d *Device) glob(p string) []string {
	if !strings.ContainsAny(path.Base(p), "*?[") {
		if d.fs.Exists(p) {
			return []string{p}
		}
		return nil
	}
	list, err := d.fs.List(path.Dir(p))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range list {
		if ok, _ := path.Match(path.Base(p), e.Name); ok && e.Name != "." && e.Name != ".." {
			out = append(out, path.Join(path.Dir(p), e.Name))
		}
	}
	return out
}

func cmdMkdir(c *Cmd) int {
	_, paths := splitFlags(c.Args[1:])
	for _, p := range paths {
		c.d.fs.Mkdir(c.abs(p))
	}
	return 0
}

func cmdChmod(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	if len(rest) < 2 {
		c.errorf("chmod: Need 2 arguments\n")
		return 1
	}
	mode, err := strconv.ParseUint(rest[0], 8, 32)
	if err != nil {
		mode = 0o755
		if strings.Contains(rest[0], "-x") {
			mode = 0o644
		}
	}
	code := 0
	for _, p := range rest[1:] {
		if err := c.d.fs.Chmod(c.abs(p), uint32(mode)); err != nil {
			c.errorf("chmod: %s: No such file or directory\n", p)
			code = 1
		}
	}
	return code
}

func cmdTouch(c *Cmd) int {
	_, paths := splitFlags(c.Args[1:])
	for _, p := range paths {
		if !c.d.fs.Exists(c.abs(p)) {
			_ = c.d.fs.WriteFile(c.abs(p), adb.SIFREG|0o666, nil, time.Now())
		}
	}
	return 0
}

func cmdCp(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	if len(rest) != 2 {
		c.errorf("cp: Need 2 arguments\n")
		return 1
	}
	src, dst := c.abs(rest[0]), c.abs(rest[1])
	fi, err := c.d.fs.Stat(src)
	if err != nil {
		c.errorf("cp: %s: No such file or directory\n", rest[0])
		return 1
	}
	data, _ := c.d.fs.ReadFile(src)
	if dfi, err := c.d.fs.Stat(dst); err == nil && dfi.Mode&adb.SIFMT == adb.SIFDIR {
		dst = path.Join(dst, path.Base(src))
	}
	_ = c.d.fs.WriteFile(dst, fi.Mode, append([]byte(nil), data...), time.Now())
	return 0
}

func cmdMv(c *Cmd) int {
	if code := cmdCp(c); code != 0 {
		return code
	}
	_, rest := splitFlags(c.Args[1:])
	_ = c.d.fs.Remove(c.abs(rest[0]), true)
	return 0
}

func cmdDf(c *Cmd) int {
	used := c.d.fs.Usage()/1024 + 4096000
	c.printf("Filesystem       1K-blocks    Used Available Use%% Mounted on\n")
	c.printf("/dev/block/dm-0    4096000 3900000    196000  96%% /\n")
	c.printf("tmpfs              1957724    1200   1956524   1%% /dev\n")
	c.printf("/dev/block/dm-5   57000000 %7d  %8d  %2d%% /data\n", used, 57000000-used, used*100/57000000)
	c.printf("/dev/fuse         57000000 %7d  %8d  %2d%% /storage/emulated\n", used, 57000000-used, used*100/57000000)
	return 0
}

func cmdDu(c *Cmd) int {
	_, paths := splitFlags(c.Args[1:])
	for _, p := range paths {
		fi, err := c.d.fs.Stat(c.abs(p))
		if err != nil {
			c.errorf("du: %s: No such file or directory\n", p)
			return 1
		}
		c.printf("%dK\t%s\n", (fi.Size+1023)/1024, p)
	}
	return 0
}

func cmdWhich(c *Cmd) int {
	code := 0
	for _, a := range c.Args[1:] {
		if _, ok := commands[a]; ok {
			c.printf("/system/bin/%s\n", a)
		} else {
			code = 1
		}
	}
	return code
}

func cmdGetenforce(c *Cmd) int { c.printf("Enforcing\n"); return 0 }

func cmdEnv(c *Cmd) int {
	c.Env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported {
			c.printf("%s=%s\n", name, vr.String())
		}
		return true
	})
	return 0
}

func readInput(c *Cmd, files []string) []byte {
	if len(files) == 0 {
		data, _ := io.ReadAll(c.Stdin)
		return data
	}
	var out []byte
	for _, f := range files {
		data, _ := c.d.fs.ReadFile(c.abs(f))
		out = append(out, data...)
	}
	return out
}

func cmdGrep(c *Cmd) int {
	var pattern string
	var files []string
	ignore, invert, count, quiet, fixed, only := false, false, false, false, false, false
	maxCount, after, before := -1, 0, 0
	args := c.Args[1:]
	num := func(i *int) int {
		if *i+1 < len(args) {
			*i++
			n, _ := strconv.Atoi(args[*i])
			return n
		}
		return 0
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-e" && i+1 < len(args):
			i++
			pattern = args[i]
		case a == "-m":
			maxCount = num(&i)
		case a == "-A":
			after = num(&i)
		case a == "-B":
			before = num(&i)
		case a == "-C":
			after = num(&i)
			before = after
		case strings.HasPrefix(a, "-") && len(a) > 1 && pattern == "":
			for _, ch := range a[1:] {
				switch ch {
				case 'i':
					ignore = true
				case 'v':
					invert = true
				case 'c':
					count = true
				case 'q':
					quiet = true
				case 'F':
					fixed = true
				case 'o':
					only = true
				}
			}
		case pattern == "":
			pattern = a
		default:
			files = append(files, a)
		}
	}
	expr := pattern
	if fixed {
		expr = regexp.QuoteMeta(pattern)
	} else {
		expr = strings.NewReplacer(`\|`, `|`, `\(`, `(`, `\)`, `)`).Replace(expr)
	}
	if ignore {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	lines := strings.Split(strings.TrimSuffix(string(readInput(c, files)), "\n"), "\n")
	n := 0
	printed := -1
	for i, line := range lines {
		if re.MatchString(line) == invert {
			continue
		}
		n++
		if !quiet && !count {
			if only && !invert {
				for _, m := range re.FindAllString(line, -1) {
					c.printf("%s\n", m)
				}
			} else {
				for j := max(i-before, printed+1); j <= min(i+after, len(lines)-1); j++ {
					c.printf("%s\n", lines[j])
					printed = j
				}
			}
		}
		if maxCount >= 0 && n >= maxCount {
			break
		}
	}
	if count {
		c.printf("%d\n", n)
	}
	if n == 0 {
		return 1
	}
	return 0
}

func lineCount(args []string) (int, []string) {
	n := 10
	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" && i+1 < len(args):
			i++
			n, _ = strconv.Atoi(strings.TrimPrefix(args[i], "+"))
		case strings.HasPrefix(a, "-n"):
			n, _ = strconv.Atoi(a[2:])
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if v, err := strconv.Atoi(a[1:]); err == nil {
				n = v
			}
		default:
			files = append(files, a)
		}
	}
	return n, files
}

func cmdHead(c *Cmd) int {
	n, files := lineCount(c.Args[1:])
	lines := strings.SplitAfter(string(readInput(c, files)), "\n")
	for i := 0; i < n && i < len(lines); i++ {
		c.printf("%s", lines[i])
	}
	return 0
}

func cmdTail(c *Cmd) int {
	n, files := lineCount(c.Args[1:])
	text := strings.TrimSuffix(string(readInput(c, files)), "\n")
	if text == "" {
		return 0
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	c.printf("%s\n", strings.Join(lines, "\n"))
	return 0
}

func cmdWc(c *Cmd) int {
	flags, files := splitFlags(c.Args[1:])
	data := readInput(c, files)
	lines := bytes.Count(data, []byte("\n"))
	switch {
	case flags["l"]:
		c.printf("%d\n", lines)
	case flags["c"]:
		c.printf("%d\n", len(data))
	case flags["w"]:
		c.printf("%d\n", len(strings.Fields(string(data))))
	default:
		c.printf("%d %d %d\n", lines, len(strings.Fields(string(data))), len(data))
	}
	return 0
}

func cmdSum(c *Cmd) int {
	var h func() hash.Hash
	switch path.Base(c.Args[0]) {
	case "md5sum":
		h = md5.New
	case "sha1sum":
		h = sha1.New
	default:
		h = sha256.New
	}
	_, files := splitFlags(c.Args[1:])
	code := 0
	for _, f := range files {
		data, err := c.d.fs.ReadFile(c.abs(f))
		if err != nil {
			c.errorf("%s: %s: No such file or directory\n", c.Args[0], f)
			code = 1
			continue
		}
		sum := h()
		sum.Write(data)
		c.printf("%s  %s\n", hex.EncodeToString(sum.Sum(nil)), f)
	}
	return code
}

func cmdStat(c *Cmd) int {
	var format string
	var files []string
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		if (args[i] == "-c" || args[i] == "--format") && i+1 < len(args) {
			i++
			format = args[i]
		} else if !strings.HasPrefix(args[i], "-") {
			files = append(files, args[i])
		}
	}
	code := 0
	for _, f := range files {
		fi, err := c.d.fs.Stat(c.abs(f))
		if err != nil {
			c.errorf("stat: '%s': No such file or directory\n", f)
			code = 1
			continue
		}
		if format != "" {
			r := strings.NewReplacer("%s", strconv.FormatInt(fi.Size, 10), "%Y", strconv.FormatInt(fi.MTime.Unix(), 10),
				"%n", f, "%a", strconv.FormatUint(uint64(fi.Mode&0o777), 8), "%A", modeString(fi.Mode),
				"%F", map[bool]string{true: "directory", false: "regular file"}[fi.Mode&adb.SIFMT == adb.SIFDIR])
			c.printf("%s\n", r.Replace(format))
			continue
		}
		c.printf("  File: %s\n  Size: %d\n Access: (%04o/%s)\nModify: %s\n", f, fi.Size, fi.Mode&0o777, modeString(fi.Mode),
			fi.MTime.Format("2006-01-02 15:04:05.000000000 -0700"))
	}
	return code
}

func cmdReadlink(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	for _, p := range rest {
		c.printf("%s\n", c.abs(p))
	}
	return 0
}

func cmdBasename(c *Cmd) int {
	if len(c.Args) > 1 {
		c.printf("%s\n", path.Base(c.Args[1]))
	}
	return 0
}

func cmdDirname(c *Cmd) int {
	if len(c.Args) > 1 {
		c.printf("%s\n", path.Dir(c.Args[1]))
	}
	return 0
}

func cmdNproc(c *Cmd) int { c.printf("8\n"); return 0 }

func cmdGetconf(c *Cmd) int {
	if len(c.Args) > 1 && c.Args[1] == "PAGESIZE" {
		c.printf("4096\n")
		return 0
	}
	c.printf("8\n")
	return 0
}

func cmdCut(c *Cmd) int {
	delim, field := "\t", 1
	var files []string
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" && i+1 < len(args):
			i++
			delim = args[i]
		case strings.HasPrefix(a, "-d"):
			delim = a[2:]
		case a == "-f" && i+1 < len(args):
			i++
			field, _ = strconv.Atoi(strings.Split(args[i], ",")[0])
		case strings.HasPrefix(a, "-f"):
			field, _ = strconv.Atoi(strings.Split(a[2:], ",")[0])
		default:
			files = append(files, a)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(readInput(c, files)), "\n"), "\n") {
		parts := strings.Split(line, delim)
		if field >= 1 && field <= len(parts) {
			c.printf("%s\n", parts[field-1])
		} else {
			c.printf("%s\n", line)
		}
	}
	return 0
}

func cmdTr(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	data := string(readInput(c, nil))
	flags, _ := splitFlags(c.Args[1:])
	if flags["d"] && len(rest) >= 1 {
		for _, ch := range rest[0] {
			data = strings.ReplaceAll(data, string(ch), "")
		}
	} else if len(rest) >= 2 {
		from, to := []rune(rest[0]), []rune(rest[1])
		for i, ch := range from {
			if i < len(to) {
				data = strings.ReplaceAll(data, string(ch), string(to[i]))
			}
		}
	}
	c.printf("%s", data)
	return 0
}

func cmdSortCmd(c *Cmd) int {
	_, files := splitFlags(c.Args[1:])
	lines := strings.Split(strings.TrimSuffix(string(readInput(c, files)), "\n"), "\n")
	sort.Strings(lines)
	c.printf("%s\n", strings.Join(lines, "\n"))
	return 0
}

func cmdUptime(c *Cmd) int {
	up := time.Since(c.d.started)
	c.printf(" %s up %d min,  0 users,  load average: 0.10, 0.20, 0.30\n", time.Now().Format("15:04:05"), int(up.Minutes()))
	return 0
}

func cmdFree(c *Cmd) int {
	c.printf("\t\ttotal        used        free      shared     buffers\nMem:         3915448     2383328     1532120       12000      100000\nSwap:        2097148       10000     2087148\n")
	return 0
}

func cmdIP(c *Cmd) int {
	c.printf("1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536\n    inet 127.0.0.1/8 scope host lo\n2: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n    inet 10.0.2.16/24 brd 10.0.2.255 scope global wlan0\n")
	return 0
}

func cmdPing(c *Cmd) int {
	c.printf("PING localhost (127.0.0.1) 56(84) bytes of data.\n64 bytes from localhost (127.0.0.1): icmp_seq=1 ttl=64 time=0.05 ms\n")
	return 0
}

func cmdBase64(c *Cmd) int {
	flags, files := splitFlags(c.Args[1:])
	data := readInput(c, files)
	if flags["d"] {
		out, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			c.errorf("base64: bad input\n")
			return 1
		}
		_, _ = c.Stdout.Write(out)
		return 0
	}
	c.printf("%s\n", base64.StdEncoding.EncodeToString(data))
	return 0
}

func cmdXargs(c *Cmd) int {
	_, rest := splitFlags(c.Args[1:])
	if len(rest) == 0 {
		rest = []string{"echo"}
	}
	items := strings.Fields(string(readInput(c, nil)))
	if len(items) == 0 {
		return 0
	}
	sub := *c
	sub.Args = append(append([]string{}, rest...), items...)
	return c.d.dispatch(&sub)
}

func cmdFind(c *Cmd) int {
	root := c.Dir
	name := ""
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-name" && i+1 < len(args):
			i++
			name = args[i]
		case !strings.HasPrefix(args[i], "-"):
			root = args[i]
		}
	}
	var walk func(p string)
	walk = func(p string) {
		fi, err := c.d.fs.Stat(p)
		if err != nil {
			return
		}
		if ok, _ := path.Match(name, path.Base(p)); name == "" || ok {
			c.printf("%s\n", p)
		}
		if fi.Mode&adb.SIFMT == adb.SIFDIR {
			list, _ := c.d.fs.List(p)
			for _, e := range list {
				if e.Name != "." && e.Name != ".." {
					walk(path.Join(p, e.Name))
				}
			}
		}
	}
	walk(c.abs(root))
	return 0
}

func cmdTee(c *Cmd) int {
	_, files := splitFlags(c.Args[1:])
	data, _ := io.ReadAll(c.Stdin)
	for _, f := range files {
		_ = c.d.fs.WriteFile(c.abs(f), adb.SIFREG|0o666, data, time.Now())
	}
	_, _ = c.Stdout.Write(data)
	return 0
}

func cmdSeq(c *Cmd) int {
	first, last := 1, 1
	switch len(c.Args) {
	case 2:
		last, _ = strconv.Atoi(c.Args[1])
	case 3:
		first, _ = strconv.Atoi(c.Args[1])
		last, _ = strconv.Atoi(c.Args[2])
	}
	for i := first; i <= last; i++ {
		c.printf("%d\n", i)
	}
	return 0
}

func cmdSu(c *Cmd) int {
	c.errorf("su: not found\n")
	return 127
}

func cmdRunAs(c *Cmd) int {
	if len(c.Args) < 2 {
		return 1
	}
	if c.d.pkgs.Get(c.Args[1]) == nil {
		c.errorf("run-as: unknown package: %s\n", c.Args[1])
		return 1
	}
	if len(c.Args) == 2 {
		return 0
	}
	sub := *c
	sub.Args = c.Args[2:]
	return c.d.dispatch(&sub)
}

func cmdToybox(c *Cmd) int {
	if len(c.Args) < 2 {
		c.printf("toybox 0.8.9-android\n")
		return 0
	}
	sub := *c
	sub.Args = c.Args[1:]
	return c.d.dispatch(&sub)
}

func cmdSh(c *Cmd) int {
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" && i+1 < len(args) {
			return c.d.runScript(c.ctx, args[i+1], c.Stdin, c.Stdout, c.Stderr, args[i+2:])
		}
		if !strings.HasPrefix(args[i], "-") {
			data, err := c.d.fs.ReadFile(c.abs(args[i]))
			if err != nil {
				c.errorf("sh: %s: No such file or directory\n", args[i])
				return 127
			}
			return c.d.runScript(c.ctx, string(data), c.Stdin, c.Stdout, c.Stderr, args[i+1:])
		}
	}
	data, _ := io.ReadAll(c.Stdin)
	return c.d.runScript(c.ctx, string(data), nil, c.Stdout, c.Stderr, nil)
}

func cmdReboot(c *Cmd) int {
	c.d.App.Log("shell", "reboot", "%s", strings.Join(c.Args[1:], " "))
	go c.d.Reboot()
	return 0
}

func cmdLocksettings(c *Cmd) int {
	if len(c.Args) > 1 && c.Args[1] == "get-disabled" {
		c.printf("false\n")
	}
	return 0
}

func cmdScreenrecord(c *Cmd) int {
	limit := 180 * time.Second
	args := c.Args[1:]
	var file string
	for i := 0; i < len(args); i++ {
		if args[i] == "--time-limit" && i+1 < len(args) {
			i++
			if s, err := strconv.Atoi(args[i]); err == nil {
				limit = time.Duration(s) * time.Second
			}
		} else if !strings.HasPrefix(args[i], "-") && strings.Contains(args[i], "/") {
			file = args[i]
		}
	}
	p, done := c.d.procs.Start(c.ctx, "shell", "screenrecord", strings.Join(c.Args, " "))
	defer done()
	select {
	case <-time.After(limit):
	case <-p.Done():
	case <-c.ctx.Done():
	}
	if file != "" {
		w, h := c.d.App.Size()
		_ = c.d.fs.WriteFile(c.abs(file), adb.SIFREG|0o666, c.d.Screen.JPEG(w/2, h/2, 60), time.Now())
	}
	return 0
}
