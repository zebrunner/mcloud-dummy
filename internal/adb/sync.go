package adb

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// FileInfo is what the sync service reports about a path. Mode holds the unix type bits (S_IFREG, S_IFDIR...).
type FileInfo struct {
	Name  string
	Mode  uint32
	Size  int64
	MTime time.Time
}

// Unix file type bits.
const (
	SIFMT  = 0o170000
	SIFDIR = 0o040000
	SIFREG = 0o100000
	SIFLNK = 0o120000
)

// FS is the file system of the device served by the sync service.
type FS interface {
	Stat(path string) (FileInfo, error)
	List(path string) ([]FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, mode uint32, data []byte, mtime time.Time) error
}

const (
	errnoENOENT  = 2
	errnoEACCES  = 13
	errnoENOTDIR = 20
	syncDataMax  = 64 * 1024
)

func syncID(s string) uint32 { return binary.LittleEndian.Uint32([]byte(s)) }

var (
	idSTAT = syncID("STAT")
	idSTA2 = syncID("STA2")
	idLST2 = syncID("LST2")
	idLIST = syncID("LIST")
	idLIS2 = syncID("LIS2")
	idSEND = syncID("SEND")
	idSND2 = syncID("SND2")
	idRECV = syncID("RECV")
	idRCV2 = syncID("RCV2")
	idDATA = syncID("DATA")
	idDONE = syncID("DONE")
	idOKAY = syncID("OKAY")
	idFAIL = syncID("FAIL")
	idQUIT = syncID("QUIT")
	idDENT = syncID("DENT")
	idDNT2 = syncID("DNT2")
)

func errno(err error) uint32 {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, fs.ErrNotExist):
		return errnoENOENT
	case errors.Is(err, fs.ErrPermission):
		return errnoEACCES
	}
	return errnoENOTDIR
}

type syncConn struct {
	rw io.ReadWriter
}

func (c *syncConn) le32(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

func (c *syncConn) write(parts ...[]byte) error {
	var buf []byte
	for _, p := range parts {
		buf = append(buf, p...)
	}
	_, err := c.rw.Write(buf)
	return err
}

func (c *syncConn) fail(msg string) error {
	return c.write(c.le32(idFAIL, uint32(len(msg))), []byte(msg))
}

func stat2(id uint32, fi FileInfo, err error) []byte {
	b := make([]byte, 72)
	binary.LittleEndian.PutUint32(b[0:], id)
	binary.LittleEndian.PutUint32(b[4:], errno(err))
	if err == nil {
		binary.LittleEndian.PutUint64(b[8:], 0x803)    // dev
		binary.LittleEndian.PutUint64(b[16:], 1)       // ino
		binary.LittleEndian.PutUint32(b[24:], fi.Mode) // mode
		binary.LittleEndian.PutUint32(b[28:], 1)       // nlink
		binary.LittleEndian.PutUint32(b[32:], 2000)    // uid (shell)
		binary.LittleEndian.PutUint32(b[36:], 2000)    // gid
		binary.LittleEndian.PutUint64(b[40:], uint64(fi.Size))
		t := uint64(fi.MTime.Unix())
		binary.LittleEndian.PutUint64(b[48:], t) // atime
		binary.LittleEndian.PutUint64(b[56:], t) // mtime
		binary.LittleEndian.PutUint64(b[64:], t) // ctime
	}
	return b
}

// ServeSync serves the "sync:" service: STAT/STA2/LST2, LIST/LIS2, SEND, RECV and QUIT.
func ServeSync(s io.ReadWriter, fsys FS) {
	c := &syncConn{rw: s}
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(s, hdr); err != nil {
			return
		}
		id := binary.LittleEndian.Uint32(hdr)
		n := binary.LittleEndian.Uint32(hdr[4:])
		if n > 64*1024 {
			return
		}
		arg := make([]byte, n)
		if _, err := io.ReadFull(s, arg); err != nil {
			return
		}
		path := string(arg)
		var err error
		switch id {
		case idSTAT:
			fi, serr := fsys.Stat(path)
			if serr != nil {
				err = c.write(c.le32(idSTAT, 0, 0, 0))
			} else {
				err = c.write(c.le32(idSTAT, fi.Mode, uint32(fi.Size), uint32(fi.MTime.Unix())))
			}
		case idSTA2, idLST2:
			fi, serr := fsys.Stat(path)
			err = c.write(stat2(id, fi, serr))
		case idLIST:
			entries, _ := fsys.List(path)
			for _, e := range entries {
				if err = c.write(c.le32(idDENT, e.Mode, uint32(e.Size), uint32(e.MTime.Unix()), uint32(len(e.Name))), []byte(e.Name)); err != nil {
					return
				}
			}
			err = c.write(c.le32(idDONE, 0, 0, 0, 0))
		case idLIS2:
			entries, _ := fsys.List(path)
			for _, e := range entries {
				b := stat2(idDNT2, e, nil)
				if err = c.write(b, c.le32(uint32(len(e.Name))), []byte(e.Name)); err != nil {
					return
				}
			}
			done := stat2(idDONE, FileInfo{}, nil)
			err = c.write(done, c.le32(0))
		case idSEND:
			err = c.receiveFile(fsys, path)
		case idSND2:
			err = c.fail("SND2 is not supported")
		case idRECV:
			err = c.sendFile(fsys, path)
		case idRCV2:
			err = c.fail("RCV2 is not supported")
		case idQUIT:
			return
		default:
			_ = c.fail("unknown sync request")
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *syncConn) receiveFile(fsys FS, spec string) error {
	path, modeStr := spec, "0644"
	if i := strings.LastIndexByte(spec, ','); i >= 0 {
		path, modeStr = spec[:i], spec[i+1:]
	}
	mode, perr := strconv.ParseUint(modeStr, 10, 32)
	if perr != nil {
		mode = SIFREG | 0o644
	}
	if mode&SIFMT == 0 {
		mode |= SIFREG
	}
	var data []byte
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(c.rw, hdr); err != nil {
			return err
		}
		id := binary.LittleEndian.Uint32(hdr)
		n := binary.LittleEndian.Uint32(hdr[4:])
		switch id {
		case idDATA:
			if n > 1024*1024 {
				return errors.New("sync: chunk too large")
			}
			chunk := make([]byte, n)
			if _, err := io.ReadFull(c.rw, chunk); err != nil {
				return err
			}
			data = append(data, chunk...)
		case idDONE:
			mtime := time.Unix(int64(n), 0)
			if n == 0 {
				mtime = time.Now()
			}
			if err := fsys.WriteFile(path, uint32(mode), data, mtime); err != nil {
				return c.fail(err.Error())
			}
			return c.write(c.le32(idOKAY, 0))
		default:
			return c.fail("unexpected sync packet while receiving a file")
		}
	}
}

func (c *syncConn) sendFile(fsys FS, path string) error {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return c.fail(err.Error())
	}
	for off := 0; off < len(data); off += syncDataMax {
		end := min(off+syncDataMax, len(data))
		if err := c.write(c.le32(idDATA, uint32(end-off)), data[off:end]); err != nil {
			return err
		}
	}
	return c.write(c.le32(idDONE, 0))
}
