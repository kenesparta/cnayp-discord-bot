package websocket

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pipeConn wires a Conn to an in-memory peer that speaks raw frames.
func pipeConn(t *testing.T) (*Conn, net.Conn) {
	t.Helper()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return &Conn{conn: client, br: bufio.NewReader(client), readLimit: defaultReadLimit}, server
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestConnReadSingleMessage(t *testing.T) {
	conn, peer := pipeConn(t)

	go func() { _, _ = peer.Write(serverFrame(true, opText, []byte(`{"op":10}`))) }()

	have, err := conn.Read(testContext(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `{"op":10}`; string(have) != want {
		t.Errorf("Read() = %q, want %q", have, want)
	}
}

func TestConnReadReassemblesFragments(t *testing.T) {
	conn, peer := pipeConn(t)

	go func() {
		_, _ = peer.Write(serverFrame(false, opText, []byte("frag")))
		_, _ = peer.Write(serverFrame(false, opContinuation, []byte("ment")))
		_, _ = peer.Write(serverFrame(true, opContinuation, []byte("ed")))
	}()

	have, err := conn.Read(testContext(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "fragmented"; string(have) != want {
		t.Errorf("Read() = %q, want %q", have, want)
	}
}

func TestConnReadAnswersPingWithPong(t *testing.T) {
	conn, peer := pipeConn(t)

	pong := make(chan []byte, 1)
	go func() {
		_, _ = peer.Write(serverFrame(true, opPing, []byte("keepalive")))

		// The pong is written by Read before it moves on to the data frame.
		raw := make([]byte, 64)
		n, _ := peer.Read(raw)
		pong <- raw[:n]

		_, _ = peer.Write(serverFrame(true, opText, []byte("data")))
	}()

	have, err := conn.Read(testContext(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "data"; string(have) != want {
		t.Errorf("Read() = %q, want %q", have, want)
	}

	raw := <-pong
	if raw[0] != 0x80|opPong {
		t.Errorf("control frame opcode byte = %#x, want a final pong", raw[0])
	}
	if payload := decodeClientFrame(t, raw); !bytes.Equal(payload, []byte("keepalive")) {
		t.Errorf("pong payload = %q, want the ping payload echoed back", payload)
	}
}

func TestConnReadReportsClose(t *testing.T) {
	conn, peer := pipeConn(t)

	go func() {
		payload := append([]byte{0x0F, 0xA4}, "authentication failed"...) // 4004
		_, _ = peer.Write(serverFrame(true, opClose, payload))
		_ = peer.Close()
	}()

	_, err := conn.Read(testContext(t))

	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Read() error = %v, want a *CloseError", err)
	}
	if closeErr.Code != 4004 {
		t.Errorf("close code = %d, want 4004", closeErr.Code)
	}
	if want := "authentication failed"; closeErr.Reason != want {
		t.Errorf("close reason = %q, want %q", closeErr.Reason, want)
	}
}

func TestConnReadUnblocksOnContextCancel(t *testing.T) {
	conn, _ := pipeConn(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(ctx)
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Read() = nil error, want the cancelled context to end the read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read() did not return after the context was cancelled")
	}
}

func TestConnWriteIsReadableByThePeer(t *testing.T) {
	conn, peer := pipeConn(t)

	received := make(chan []byte, 1)
	go func() {
		raw := make([]byte, 128)
		n, _ := peer.Read(raw)
		received <- raw[:n]
	}()

	if err := conn.Write(testContext(t), []byte(`{"op":1,"d":42}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if have := decodeClientFrame(t, <-received); string(have) != `{"op":1,"d":42}` {
		t.Errorf("peer received %q, want the payload written", have)
	}
}

func TestConnCloseIsIdempotent(t *testing.T) {
	conn, peer := pipeConn(t)

	go func() {
		raw := make([]byte, 64)
		_, _ = peer.Read(raw)
	}()

	if err := conn.Close(StatusNormalClosure, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := conn.Close(StatusNormalClosure, ""); err != nil {
		t.Fatalf("second Close() returned %v, want the first result replayed", err)
	}
}

// handshakeServer accepts one connection, replies to the upgrade with respond,
// and then hands the raw connection to after.
func handshakeServer(t *testing.T, respond func(key string) string, after func(net.Conn)) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		if _, err := conn.Write([]byte(respond(req.Header.Get("Sec-WebSocket-Key")))); err != nil {
			return
		}
		if after != nil {
			after(conn)
		}
	}()

	return "ws://" + listener.Addr().String() + "/?v=10&encoding=json"
}

func acceptResponse(key string) string {
	return "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
}

func TestDialCompletesHandshakeAndReads(t *testing.T) {
	served := make(chan struct{})
	url := handshakeServer(t, acceptResponse, func(conn net.Conn) {
		_, _ = conn.Write(serverFrame(true, opText, []byte(`{"op":10,"d":{"heartbeat_interval":41250}}`)))
		<-served
	})

	ctx := testContext(t)
	conn, err := Dial(ctx, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		close(served)
		_ = conn.Close(StatusNormalClosure, "")
	})

	have, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `{"op":10,"d":{"heartbeat_interval":41250}}`; string(have) != want {
		t.Errorf("Read() = %q, want %q", have, want)
	}
}

func TestDialRejectsBadHandshake(t *testing.T) {
	tests := []struct {
		name    string
		respond func(key string) string
		wantErr string
	}{
		{
			name:    "wrong status",
			respond: func(string) string { return "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n" },
			wantErr: "handshake rejected",
		},
		{
			name: "wrong accept key",
			respond: func(string) string {
				return acceptResponse("some other key")
			},
			wantErr: "invalid Sec-WebSocket-Accept",
		},
		{
			name: "missing upgrade header",
			respond: func(key string) string {
				return "HTTP/1.1 101 Switching Protocols\r\n" +
					"Connection: Upgrade\r\n" +
					"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
			},
			wantErr: "unexpected Upgrade header",
		},
		{
			name: "unsupported extension",
			respond: func(key string) string {
				return "HTTP/1.1 101 Switching Protocols\r\n" +
					"Upgrade: websocket\r\n" +
					"Connection: Upgrade\r\n" +
					"Sec-WebSocket-Extensions: permessage-deflate\r\n" +
					"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
			},
			wantErr: "unsupported extension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := handshakeServer(t, tt.respond, nil)

			conn, err := Dial(testContext(t), url)
			if err == nil {
				_ = conn.Close(StatusNormalClosure, "")
				t.Fatal("Dial() = nil error, want the handshake to be rejected")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Dial() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDialRejectsUnsupportedScheme(t *testing.T) {
	if _, err := Dial(testContext(t), "http://example.invalid"); err == nil {
		t.Fatal("Dial() = nil error, want an unsupported scheme to be refused")
	} else if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("Dial() error = %q, want it to mention the scheme", err)
	}
}

// Guard against the frame writer and the RFC diverging on the wire format.
func TestWriteFrameHeaderLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, opClose, []byte{0x03, 0xE8}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw := buf.Bytes()
	if want := 2 + 4 + 2; len(raw) != want {
		t.Fatalf("frame length = %d, want %d (header + mask key + payload)", len(raw), want)
	}
	if raw[1]&0x7F != 2 {
		t.Errorf("announced payload length = %d, want 2", raw[1]&0x7F)
	}
	if have := decodeClientFrame(t, raw); !bytes.Equal(have, []byte{0x03, 0xE8}) {
		t.Errorf("payload = %v, want the 1000 status code", have)
	}
}

func ExampleConn_Read() {
	// Dial and read the first gateway payload.
	conn, err := Dial(context.Background(), "wss://gateway.discord.gg/?v=10&encoding=json")
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer func() { _ = conn.Close(StatusNormalClosure, "") }()

	payload, err := conn.Read(context.Background())
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Println(len(payload) > 0)
}
