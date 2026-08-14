package websocket

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Frame opcodes, RFC 6455 section 5.2.
const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA
)

// maxControlPayload is the ceiling RFC 6455 section 5.5 puts on control frames.
const maxControlPayload = 125

// A frame is one parsed WebSocket frame. The payload is already unmasked.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// isControl reports whether opcode denotes a control frame, which may interleave
// with a fragmented message and is never fragmented itself.
func isControl(opcode byte) bool { return opcode&0x8 != 0 }

// readFrame reads one frame, rejecting anything a conforming server would not
// send. limit caps the payload so a hostile or broken peer cannot exhaust memory.
func readFrame(r io.Reader, limit int64) (frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frame{}, err
	}

	f := frame{
		fin:    head[0]&0x80 != 0,
		opcode: head[0] & 0x0F,
	}
	if head[0]&0x70 != 0 {
		return frame{}, errors.New("reserved bits set")
	}
	if head[1]&0x80 != 0 {
		return frame{}, errors.New("server frame is masked")
	}

	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		extended := binary.BigEndian.Uint64(ext[:])
		if extended > math.MaxInt64 {
			return frame{}, errors.New("payload length overflows int64")
		}
		length = int64(extended)
	}

	if isControl(f.opcode) {
		if !f.fin {
			return frame{}, errors.New("fragmented control frame")
		}
		if length > maxControlPayload {
			return frame{}, fmt.Errorf("control frame payload %d exceeds %d", length, maxControlPayload)
		}
	}
	if length > limit {
		return frame{}, fmt.Errorf("payload %d exceeds read limit %d", length, limit)
	}

	f.payload = make([]byte, length)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return frame{}, err
	}
	return f, nil
}

// writeFrame writes a single unfragmented frame. RFC 6455 section 5.3 requires
// every client frame to be masked with a fresh key, so payload is copied rather
// than masked in place.
func writeFrame(w io.Writer, opcode byte, payload []byte) error {
	var head [14]byte
	head[0] = 0x80 | opcode // FIN, since nothing here fragments

	n := 2
	switch {
	case len(payload) < 126:
		head[1] = byte(len(payload))
	case len(payload) <= math.MaxUint16:
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:4], uint16(len(payload)))
		n = 4
	default:
		head[1] = 127
		binary.BigEndian.PutUint64(head[2:10], uint64(len(payload)))
		n = 10
	}
	head[1] |= 0x80 // masked

	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return fmt.Errorf("generate mask key: %w", err)
	}
	n += copy(head[n:], key[:])

	// One Write keeps the frame contiguous on the wire.
	buf := make([]byte, 0, n+len(payload))
	buf = append(buf, head[:n]...)
	buf = append(buf, payload...)
	mask(buf[n:], key)

	if _, err := w.Write(buf); err != nil {
		return err
	}
	return nil
}

// mask applies the XOR transform from RFC 6455 section 5.3 in place.
func mask(b []byte, key [4]byte) {
	for i := range b {
		b[i] ^= key[i%4]
	}
}
