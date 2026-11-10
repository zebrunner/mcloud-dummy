package android

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Log priorities of android.util.Log.
const (
	prioVerbose = 2
	prioDebug   = 3
	prioInfo    = 4
	prioWarn    = 5
	prioError   = 6
)

type logEntry struct {
	at   time.Time
	pid  int
	prio byte
	tag  string
	msg  string
}

// logBuffer is the logcat ring buffer of a device.
type logBuffer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	entries []logEntry
	seq     int // number of entries ever added
}

func newLogBuffer() *logBuffer {
	b := &logBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

const maxLogEntries = 2000

func (b *logBuffer) add(prio byte, tag, msg string) {
	b.mu.Lock()
	b.entries = append(b.entries, logEntry{at: time.Now(), pid: 450, prio: prio, tag: tag, msg: msg})
	if len(b.entries) > maxLogEntries {
		b.entries = b.entries[len(b.entries)-maxLogEntries:]
	}
	b.seq++
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *logBuffer) clear() {
	b.mu.Lock()
	b.entries = nil
	b.mu.Unlock()
}

// since returns the entries after the sequence number and the new sequence number.
func (b *logBuffer) since(seq int) ([]logEntry, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.seq - seq
	if n > len(b.entries) {
		n = len(b.entries)
	}
	if n <= 0 {
		return nil, b.seq
	}
	return append([]logEntry(nil), b.entries[len(b.entries)-n:]...), b.seq
}

func (b *logBuffer) wait(seq int, timeout time.Duration) {
	t := time.AfterFunc(timeout, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer t.Stop()
	b.mu.Lock()
	if b.seq == seq {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

func (e logEntry) binary() []byte {
	payload := append([]byte{e.prio}, []byte(e.tag)...)
	payload = append(payload, 0)
	payload = append(payload, []byte(e.msg)...)
	payload = append(payload, 0)
	buf := make([]byte, 24+len(payload))
	binary.LittleEndian.PutUint16(buf[0:], uint16(len(payload)))
	binary.LittleEndian.PutUint16(buf[2:], 24)
	binary.LittleEndian.PutUint32(buf[4:], uint32(e.pid))
	binary.LittleEndian.PutUint32(buf[8:], uint32(e.pid))
	binary.LittleEndian.PutUint32(buf[12:], uint32(e.at.Unix()))
	binary.LittleEndian.PutUint32(buf[16:], uint32(e.at.Nanosecond()))
	binary.LittleEndian.PutUint32(buf[20:], 0) // log id: main
	copy(buf[24:], payload)
	return buf
}

func (e logEntry) text() string {
	return fmt.Sprintf("%s %5d %5d %c %-8s: %s\n", e.at.Format("01-02 15:04:05.000"), e.pid, e.pid,
		"??VDIWEF"[min(int(e.prio), 7)], e.tag, e.msg)
}

// produceLogs writes a heartbeat line every few seconds and the events of the app.
func (d *Device) produceLogs() {
	d.logs.add(prioInfo, "MCloudDummy", fmt.Sprintf("Booting %s (%s, Android %s)", d.Cfg.Serial, d.Cfg.Model, d.Cfg.OSVersion))
	seen := 0
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for range tick.C {
		events := d.App.Events()
		if len(events) < seen {
			seen = 0
		}
		for _, e := range events[seen:] {
			d.logs.add(prioInfo, "MCloudDummy", fmt.Sprintf("%s %s %s", e.Source, e.Kind, e.Detail))
		}
		seen = len(events)
		d.logs.add(prioDebug, "MCloudDummy", fmt.Sprintf("heartbeat taps=%d", d.App.Taps()))
	}
}

func cmdLog(c *Cmd) int {
	tag, prio := "log", byte(prioInfo)
	var msg []string
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			if i+1 < len(args) {
				i++
				tag = args[i]
			}
		case "-p":
			if i+1 < len(args) {
				i++
				prio = map[string]byte{"v": prioVerbose, "d": prioDebug, "i": prioInfo, "w": prioWarn, "e": prioError}[strings.ToLower(args[i][:1])]
			}
		default:
			msg = append(msg, args[i])
		}
	}
	c.d.logs.add(prio, tag, strings.Join(msg, " "))
	return 0
}

func cmdLogcat(c *Cmd) int {
	binaryOut, dump, clear := false, false, false
	tail := -1
	args := c.Args[1:]
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-B" || a == "--binary":
			binaryOut = true
		case a == "-d":
			dump = true
		case a == "-c":
			clear = true
		case a == "-t" || a == "-T":
			if i+1 < len(args) {
				i++
				fmt.Sscan(args[i], &tail)
			}
			if a == "-t" {
				dump = true
			}
		case a == "-v" || a == "-b" || a == "-s" || a == "-f" || a == "-r" || a == "-n" || a == "-g" || a == "-G" || a == "--pid":
			if a != "-s" && a != "-g" && i+1 < len(args) {
				i++
			}
			if a == "-g" {
				c.printf("main: ring buffer is 256 KiB (100 KiB consumed), max entry is 5120 B, max payload is 4068 B\n")
				return 0
			}
		}
	}
	if clear {
		c.d.logs.clear()
		return 0
	}
	write := func(entries []logEntry) bool {
		var out []byte
		for _, e := range entries {
			if binaryOut {
				out = append(out, e.binary()...)
			} else {
				out = append(out, e.text()...)
			}
		}
		if len(out) == 0 {
			return true
		}
		_, err := c.Stdout.Write(out)
		return err == nil
	}
	entries, seq := c.d.logs.since(0)
	if tail >= 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}
	if !write(entries) || dump {
		return 0
	}
	for c.ctx.Err() == nil {
		c.d.logs.wait(seq, time.Second)
		var next []logEntry
		next, seq = c.d.logs.since(seq)
		if !write(next) {
			return 0
		}
	}
	return 130
}
