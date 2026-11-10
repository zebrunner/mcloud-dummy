package android

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// A minimal protobuf codec for the STFService wire (proto2, varint-delimited).

func backgroundCtx() context.Context { return context.Background() }

func appendVarint(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func pbKey(field int, wire int) []byte { return appendVarint(nil, uint64(field)<<3|uint64(wire)) }

func pbVarint(field int, v uint64) []byte { return appendVarint(pbKey(field, 0), v) }

func pbBool(field int, v bool) []byte {
	if v {
		return pbVarint(field, 1)
	}
	return pbVarint(field, 0)
}

func pbBytes(field int, v []byte) []byte {
	b := appendVarint(pbKey(field, 2), uint64(len(v)))
	return append(b, v...)
}

func pbString(field int, v string) []byte { return pbBytes(field, []byte(v)) }

func pbFloat(field int, v float32) []byte {
	return binary.LittleEndian.AppendUint32(pbKey(field, 5), math.Float32bits(v))
}

func pbDouble(field int, v float64) []byte {
	return binary.LittleEndian.AppendUint64(pbKey(field, 1), math.Float64bits(v))
}

func pbMsg(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

type envelope struct {
	id      uint64
	hasID   bool
	typ     int
	message []byte
}

func encodeEnvelope(id uint64, hasID bool, typ int, message []byte) []byte {
	var b []byte
	if hasID {
		b = append(b, pbVarint(1, id)...)
	}
	b = append(b, pbVarint(2, uint64(typ))...)
	return append(b, pbBytes(3, message)...)
}

func decodeEnvelope(data []byte) envelope {
	f := decodeFields(data)
	env := envelope{typ: int(f.uint(2)), message: f.bytes(3)}
	if v, ok := f[1]; ok && len(v) > 0 {
		env.id, env.hasID = v[0].u, true
	}
	return env
}

type pbValue struct {
	u uint64
	b []byte
}

type pbFields map[int][]pbValue

func (f pbFields) uint(n int) uint64 {
	if v := f[n]; len(v) > 0 {
		return v[0].u
	}
	return 0
}

func (f pbFields) bytes(n int) []byte {
	if v := f[n]; len(v) > 0 {
		return v[0].b
	}
	return nil
}

func (f pbFields) str(n int) string { return string(f.bytes(n)) }

func (f pbFields) strs(n int) []string {
	var out []string
	for _, v := range f[n] {
		out = append(out, string(v.b))
	}
	return out
}

func decodeFields(data []byte) pbFields {
	f := pbFields{}
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return f
		}
		data = data[n:]
		field, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return f
			}
			data = data[n:]
			f[field] = append(f[field], pbValue{u: v})
		case 1:
			if len(data) < 8 {
				return f
			}
			f[field] = append(f[field], pbValue{u: binary.LittleEndian.Uint64(data)})
			data = data[8:]
		case 2:
			l, n := binary.Uvarint(data)
			if n <= 0 || uint64(len(data)-n) < l {
				return f
			}
			data = data[n:]
			f[field] = append(f[field], pbValue{b: data[:l]})
			data = data[l:]
		case 5:
			if len(data) < 4 {
				return f
			}
			f[field] = append(f[field], pbValue{u: uint64(binary.LittleEndian.Uint32(data))})
			data = data[4:]
		default:
			return f
		}
	}
	return f
}

func appendDelimited(b, msg []byte) []byte {
	b = appendVarint(b, uint64(len(msg)))
	return append(b, msg...)
}

func readDelimited(r *bufio.Reader) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > 16*1024*1024 {
		return nil, errors.New("protobuf message too large")
	}
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return buf, err
}
