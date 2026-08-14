package websocket

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// serverFrame builds an unmasked frame the way a conforming server sends one.
func serverFrame(fin bool, opcode byte, payload []byte) []byte {
	head := []byte{opcode, 0}
	if fin {
		head[0] |= 0x80
	}
	switch {
	case len(payload) < 126:
		head[1] = byte(len(payload))
	case len(payload) <= 0xFFFF:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(len(payload)))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(len(payload)))
	}
	return append(head, payload...)
}

func TestReadFrame(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    frame
		wantErr string
	}{
		{
			name:  "short text",
			input: serverFrame(true, opText, []byte("hello")),
			want:  frame{fin: true, opcode: opText, payload: []byte("hello")},
		},
		{
			name:  "empty payload",
			input: serverFrame(true, opText, nil),
			want:  frame{fin: true, opcode: opText, payload: []byte{}},
		},
		{
			name:  "non final fragment",
			input: serverFrame(false, opText, []byte("he")),
			want:  frame{fin: false, opcode: opText, payload: []byte("he")},
		},
		{
			name:  "extended 16 bit length",
			input: serverFrame(true, opBinary, bytes.Repeat([]byte("x"), 200)),
			want:  frame{fin: true, opcode: opBinary, payload: bytes.Repeat([]byte("x"), 200)},
		},
		{
			name:  "extended 64 bit length",
			input: serverFrame(true, opBinary, bytes.Repeat([]byte("x"), 70000)),
			want:  frame{fin: true, opcode: opBinary, payload: bytes.Repeat([]byte("x"), 70000)},
		},
		{
			name:    "masked server frame is rejected",
			input:   []byte{0x81, 0x80, 0, 0, 0, 0},
			wantErr: "masked",
		},
		{
			name:    "reserved bits are rejected",
			input:   []byte{0xC1, 0x00},
			wantErr: "reserved bits",
		},
		{
			name:    "fragmented control frame is rejected",
			input:   serverFrame(false, opPing, nil),
			wantErr: "fragmented control frame",
		},
		{
			name:    "oversized control frame is rejected",
			input:   serverFrame(true, opPing, bytes.Repeat([]byte("x"), 126)),
			wantErr: "control frame payload",
		},
		{
			name:    "truncated header",
			input:   []byte{0x81},
			wantErr: "EOF",
		},
		{
			name:    "truncated payload",
			input:   []byte{0x81, 0x05, 'h', 'i'},
			wantErr: "EOF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			have, err := readFrame(bytes.NewReader(tt.input), defaultReadLimit)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("readFrame() = %+v, want error containing %q", have, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("readFrame() error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if have.fin != tt.want.fin || have.opcode != tt.want.opcode {
				t.Errorf("readFrame() fin=%t opcode=%#x, want fin=%t opcode=%#x",
					have.fin, have.opcode, tt.want.fin, tt.want.opcode)
			}
			if !bytes.Equal(have.payload, tt.want.payload) {
				t.Errorf("readFrame() payload length %d, want %d", len(have.payload), len(tt.want.payload))
			}
		})
	}
}

func TestReadFrameRejectsPayloadOverLimit(t *testing.T) {
	input := serverFrame(true, opText, bytes.Repeat([]byte("x"), 200))

	if _, err := readFrame(bytes.NewReader(input), 100); err == nil {
		t.Fatal("readFrame() = nil error, want the read limit to be enforced")
	}
}

func TestWriteFrameIsMaskedAndDecodable(t *testing.T) {
	payloads := map[string][]byte{
		"empty":    nil,
		"short":    []byte("hello"),
		"extended": bytes.Repeat([]byte("y"), 300),
		"large":    bytes.Repeat([]byte("z"), 70000),
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeFrame(&buf, opText, payload); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			raw := buf.Bytes()
			if raw[0] != 0x80|opText {
				t.Errorf("first byte = %#x, want FIN set with a text opcode", raw[0])
			}
			if raw[1]&0x80 == 0 {
				t.Error("mask bit is not set; RFC 6455 requires client frames to be masked")
			}

			have := decodeClientFrame(t, raw)
			if !bytes.Equal(have, payload) && !(len(have) == 0 && len(payload) == 0) {
				t.Errorf("decoded payload length %d, want %d", len(have), len(payload))
			}
		})
	}
}

func TestWriteFrameUsesAFreshMaskKey(t *testing.T) {
	payload := []byte("repeated payload")

	var first, second bytes.Buffer
	for _, buf := range []*bytes.Buffer{&first, &second} {
		if err := writeFrame(buf, opText, payload); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if first.String() == second.String() {
		t.Error("two writes of the same payload produced identical bytes; the mask key is not fresh")
	}
	if have := decodeClientFrame(t, second.Bytes()); !bytes.Equal(have, payload) {
		t.Errorf("decoded payload = %q, want %q", have, payload)
	}
}

// decodeClientFrame reverses writeFrame the way a server would.
func decodeClientFrame(t *testing.T, raw []byte) []byte {
	t.Helper()

	length := int(raw[1] & 0x7F)
	offset := 2
	switch length {
	case 126:
		length = int(binary.BigEndian.Uint16(raw[2:4]))
		offset = 4
	case 127:
		length = int(binary.BigEndian.Uint64(raw[2:10]))
		offset = 10
	}

	var key [4]byte
	offset += copy(key[:], raw[offset:offset+4])

	payload := bytes.Clone(raw[offset:])
	if len(payload) != length {
		t.Fatalf("payload length %d, want the %d announced in the header", len(payload), length)
	}
	mask(payload, key)
	return payload
}

func TestAcceptKey(t *testing.T) {
	// The worked example from RFC 6455 section 1.3.
	const (
		challenge = "dGhlIHNhbXBsZSBub25jZQ=="
		want      = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	)

	if have := acceptKey(challenge); have != want {
		t.Errorf("acceptKey(%q) = %q, want %q", challenge, have, want)
	}
}

func TestHeaderContainsToken(t *testing.T) {
	tests := []struct {
		value string
		token string
		want  bool
	}{
		{"Upgrade", "upgrade", true},
		{"keep-alive, Upgrade", "upgrade", true},
		{"UPGRADE", "upgrade", true},
		{"keep-alive", "upgrade", false},
		{"", "upgrade", false},
		{"upgrader", "upgrade", false},
	}

	for _, tt := range tests {
		if have := headerContainsToken(tt.value, tt.token); have != tt.want {
			t.Errorf("headerContainsToken(%q, %q) = %t, want %t", tt.value, tt.token, have, tt.want)
		}
	}
}
