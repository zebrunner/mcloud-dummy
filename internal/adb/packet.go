// Package adb implements the device side of the adb protocol (what adbd speaks to the adb server) over TCP.
package adb

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Commands of the adb transport, see packages/modules/adb/protocol.txt of AOSP.
const (
	cmdSYNC = 0x434e5953
	cmdCNXN = 0x4e584e43
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4e45504f
	cmdOKAY = 0x59414b4f
	cmdCLSE = 0x45534c43
	cmdWRTE = 0x45545257
	cmdSTLS = 0x534c5453
)

const (
	// Version is A_VERSION of adbd: the checksum of the payload is not checked since 0x01000001.
	Version = 0x01000001
	// MaxPayload of the packets sent by the device (the adb server uses the smaller of both sides).
	MaxPayload = 256 * 1024
	// maxIncomingPayload guards against a broken peer.
	maxIncomingPayload = 1024 * 1024

	authToken        = 1
	authSignature    = 2
	authRSAPublicKey = 3
)

type packet struct {
	command uint32
	arg0    uint32
	arg1    uint32
	data    []byte
}

func commandName(c uint32) string {
	switch c {
	case cmdSYNC:
		return "SYNC"
	case cmdCNXN:
		return "CNXN"
	case cmdAUTH:
		return "AUTH"
	case cmdOPEN:
		return "OPEN"
	case cmdOKAY:
		return "OKAY"
	case cmdCLSE:
		return "CLSE"
	case cmdWRTE:
		return "WRTE"
	case cmdSTLS:
		return "STLS"
	}
	return fmt.Sprintf("0x%08x", c)
}

func (p *packet) String() string {
	return fmt.Sprintf("%s(%d, %d, %d bytes)", commandName(p.command), p.arg0, p.arg1, len(p.data))
}

func checksum(data []byte) uint32 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return sum
}

func readPacket(r io.Reader) (*packet, error) {
	var hdr [24]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	p := &packet{
		command: binary.LittleEndian.Uint32(hdr[0:]),
		arg0:    binary.LittleEndian.Uint32(hdr[4:]),
		arg1:    binary.LittleEndian.Uint32(hdr[8:]),
	}
	length := binary.LittleEndian.Uint32(hdr[12:])
	magic := binary.LittleEndian.Uint32(hdr[20:])
	if magic != p.command^0xffffffff {
		return nil, fmt.Errorf("adb: bad magic of packet %s", commandName(p.command))
	}
	if length > maxIncomingPayload {
		return nil, fmt.Errorf("adb: payload of %d bytes is too large", length)
	}
	if length > 0 {
		p.data = make([]byte, length)
		if _, err := io.ReadFull(r, p.data); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func encodePacket(p *packet) []byte {
	buf := make([]byte, 24+len(p.data))
	binary.LittleEndian.PutUint32(buf[0:], p.command)
	binary.LittleEndian.PutUint32(buf[4:], p.arg0)
	binary.LittleEndian.PutUint32(buf[8:], p.arg1)
	binary.LittleEndian.PutUint32(buf[12:], uint32(len(p.data)))
	binary.LittleEndian.PutUint32(buf[16:], checksum(p.data))
	binary.LittleEndian.PutUint32(buf[20:], p.command^0xffffffff)
	copy(buf[24:], p.data)
	return buf
}
