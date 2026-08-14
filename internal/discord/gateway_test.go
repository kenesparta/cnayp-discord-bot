package discord

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/websocket"
)

// The tests below stand up a fake gateway that speaks the server half of the
// protocol, so the session is exercised over a real socket rather than a mock.

// fakeGateway is a minimal WebSocket server that plays Discord.
type fakeGateway struct {
	t        *testing.T
	listener net.Listener

	// heartbeatInterval is what the Hello payload advertises.
	heartbeatInterval time.Duration

	// identified receives the first identify or resume payload the client sends.
	identified chan incoming

	// closeWith, when non-zero, makes the gateway hang up with that status
	// instead of completing a session.
	closeWith int
}

func newFakeGateway(t *testing.T, heartbeatInterval time.Duration) *fakeGateway {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	return &fakeGateway{
		t:                 t,
		listener:          listener,
		heartbeatInterval: heartbeatInterval,
		identified:        make(chan incoming, 4),
	}
}

// url is the address a Session should dial, in the form GatewayURL returns.
func (g *fakeGateway) url() string { return "ws://" + g.listener.Addr().String() }

// serve accepts connections until the listener closes. onReady runs once the
// client has identified, with the connection to push events through.
func (g *fakeGateway) serve(onReady func(conn *serverConn)) {
	go func() {
		for {
			raw, err := g.listener.Accept()
			if err != nil {
				return
			}
			go g.session(raw, onReady)
		}
	}()
}

func (g *fakeGateway) session(raw net.Conn, onReady func(conn *serverConn)) {
	defer func() { _ = raw.Close() }()

	br := bufio.NewReader(raw)
	if err := serverHandshake(br, raw); err != nil {
		return
	}
	conn := &serverConn{raw: raw, br: br}

	if g.closeWith != 0 {
		payload := binary.BigEndian.AppendUint16(nil, uint16(g.closeWith))
		_ = conn.write(opCloseFrame, append(payload, "authentication failed"...))
		return
	}

	if err := conn.send(outgoing{Op: opHello, Data: hello{
		HeartbeatIntervalMillis: int(g.heartbeatInterval.Milliseconds()),
	}}); err != nil {
		return
	}

	// Answer heartbeats forever; hand the first identify to the test.
	ready := false
	for {
		received, err := conn.receive()
		if err != nil {
			return
		}

		switch received.Op {
		case opHeartbeat:
			if err := conn.send(outgoing{Op: opHeartbeatAcknowledge, Data: nil}); err != nil {
				return
			}
		case opIdentify, opResume:
			select {
			case g.identified <- received:
			default:
			}
			if ready {
				continue
			}
			ready = true
			if err := conn.dispatch(1, eventReady, Ready{
				SessionID:        "session-1",
				ResumeGatewayURL: "ws://" + g.listener.Addr().String(),
				User:             User{ID: "42", Username: "cnayp", Bot: true},
				Application:      Application{ID: "app-99"},
				Guilds:           []UnavailableGuild{{ID: "guild-1", Unavailable: true}},
			}); err != nil {
				return
			}
			if onReady != nil {
				go onReady(conn)
			}
		}
	}
}

// serverConn is the server side of a WebSocket connection, framing by hand.
type serverConn struct {
	raw net.Conn
	br  *bufio.Reader
}

// Frame opcodes, duplicated here because the client package keeps them private.
const (
	opTextFrame  byte = 0x1
	opCloseFrame byte = 0x8
)

func (c *serverConn) send(payload outgoing) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.write(opTextFrame, encoded)
}

// dispatch sends an event payload with a sequence number.
func (c *serverConn) dispatch(sequence int64, event string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	frame, err := json.Marshal(incoming{
		Op:       opDispatch,
		Data:     encoded,
		Sequence: sequence,
		Type:     event,
	})
	if err != nil {
		return err
	}
	return c.write(opTextFrame, frame)
}

// write emits an unmasked server frame.
func (c *serverConn) write(opcode byte, payload []byte) error {
	head := []byte{0x80 | opcode, 0}
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
	_, err := c.raw.Write(append(head, payload...))
	return err
}

// receive reads one masked client frame and decodes the gateway payload in it.
func (c *serverConn) receive() (incoming, error) {
	for {
		var head [2]byte
		if _, err := io.ReadFull(c.br, head[:]); err != nil {
			return incoming{}, err
		}

		opcode := head[0] & 0x0F
		length := int64(head[1] & 0x7F)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return incoming{}, err
			}
			length = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.br, ext[:]); err != nil {
				return incoming{}, err
			}
			length = int64(binary.BigEndian.Uint64(ext[:]))
		}

		if head[1]&0x80 == 0 {
			return incoming{}, errors.New("client frame is not masked")
		}
		var key [4]byte
		if _, err := io.ReadFull(c.br, key[:]); err != nil {
			return incoming{}, err
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return incoming{}, err
		}
		for i := range payload {
			payload[i] ^= key[i%4]
		}

		if opcode == opCloseFrame {
			return incoming{}, io.EOF
		}
		if opcode != opTextFrame {
			continue // ping/pong, not interesting to the tests
		}

		var received incoming
		if err := json.Unmarshal(payload, &received); err != nil {
			return incoming{}, err
		}
		return received, nil
	}
}

// serverHandshake completes the upgrade the client expects.
func serverHandshake(br *bufio.Reader, w io.Writer) error {
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	sum := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = fmt.Fprintf(w, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	return err
}

// restStub serves the /gateway/bot lookup a Session makes before dialing.
func restStub(t *testing.T, gatewayURL string) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/bot" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": gatewayURL})
	}))
	t.Cleanup(server.Close)

	return NewClient("test-token", WithBaseURL(server.URL))
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSessionIdentifiesAndBecomesReady(t *testing.T) {
	gateway := newFakeGateway(t, 50*time.Millisecond)
	gateway.serve(nil)

	session := NewSession(restStub(t, gateway.url()), "test-token", IntentGuilds, testLogger())

	ready := make(chan Ready, 1)
	session.OnReady(func(_ context.Context, r Ready) { ready <- r })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()

	select {
	case received := <-gateway.identified:
		if received.Op != opIdentify {
			t.Fatalf("first payload op = %s, want identify", received.Op)
		}
		var body identify
		if err := json.Unmarshal(received.Data, &body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body.Token != "test-token" {
			t.Errorf("identify token = %q, want %q", body.Token, "test-token")
		}
		if body.Intents != IntentGuilds {
			t.Errorf("identify intents = %d, want %d", body.Intents, IntentGuilds)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway never received an identify payload")
	}

	select {
	case r := <-ready:
		if r.SessionID != "session-1" {
			t.Errorf("ready session id = %q, want %q", r.SessionID, "session-1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ready handler never ran")
	}

	if have := session.ApplicationID(); have != "app-99" {
		t.Errorf("ApplicationID() = %q, want %q", have, "app-99")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v, want nil after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}

func TestSessionHeartbeatsAndMeasuresLatency(t *testing.T) {
	gateway := newFakeGateway(t, 40*time.Millisecond)
	gateway.serve(nil)

	session := NewSession(restStub(t, gateway.url()), "test-token", IntentGuilds, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = session.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for session.Latency() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat was acknowledged within the deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if latency := session.Latency(); latency <= 0 || latency > time.Second {
		t.Errorf("Latency() = %s, want a plausible round trip", latency)
	}
}

func TestSessionDispatchesInteractions(t *testing.T) {
	gateway := newFakeGateway(t, 50*time.Millisecond)
	gateway.serve(func(conn *serverConn) {
		_ = conn.dispatch(2, eventInteractionCreate, Interaction{
			ID:    "interaction-1",
			Token: "interaction-token",
			Type:  ApplicationCommandInteraction,
			Data:  InteractionData{ID: "cmd-1", Name: "ping", Type: ChatInputCommand},
		})
	})

	session := NewSession(restStub(t, gateway.url()), "test-token", IntentGuilds, testLogger())

	interactions := make(chan *Interaction, 1)
	session.OnInteraction(func(_ context.Context, i *Interaction) { interactions <- i })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = session.Run(ctx) }()

	select {
	case i := <-interactions:
		if i.Data.Name != "ping" {
			t.Errorf("interaction command = %q, want %q", i.Data.Name, "ping")
		}
		if i.ID != "interaction-1" {
			t.Errorf("interaction id = %q, want %q", i.ID, "interaction-1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interaction handler never ran")
	}
}

func TestSessionRunStopsOnFatalCloseCode(t *testing.T) {
	gateway := newFakeGateway(t, time.Second)
	gateway.closeWith = closeAuthenticationFailed
	gateway.serve(nil)

	session := NewSession(restStub(t, gateway.url()), "bad-token", IntentGuilds, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := session.Run(ctx)
	if err == nil {
		t.Fatal("Run() = nil, want a fatal error for an authentication failure")
	}

	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Run() error = %v, want it to wrap a *websocket.CloseError", err)
	}
	if closeErr.Code != closeAuthenticationFailed {
		t.Errorf("close code = %d, want %d", closeErr.Code, closeAuthenticationFailed)
	}
}

func TestFatalCloseError(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		fatal bool
	}{
		{name: "authentication failed", err: &websocket.CloseError{Code: closeAuthenticationFailed}, fatal: true},
		{name: "disallowed intents", err: &websocket.CloseError{Code: closeDisallowedIntents}, fatal: true},
		{name: "invalid api version", err: &websocket.CloseError{Code: closeInvalidAPIVersion}, fatal: true},
		{name: "session timed out", err: &websocket.CloseError{Code: 4009}, fatal: false},
		{name: "normal closure", err: &websocket.CloseError{Code: websocket.StatusNormalClosure}, fatal: false},
		{name: "plain network error", err: errors.New("connection reset"), fatal: false},
		{name: "wrapped fatal", err: fmt.Errorf("read: %w", &websocket.CloseError{Code: closeInvalidIntents}), fatal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if have := fatalCloseError(tt.err) != nil; have != tt.fatal {
				t.Errorf("fatalCloseError(%v) fatal = %t, want %t", tt.err, have, tt.fatal)
			}
		})
	}
}

func TestSessionForgetSessionClearsResumeState(t *testing.T) {
	session := NewSession(NewClient("t"), "t", IntentGuilds, testLogger())
	session.rememberSession(Ready{
		SessionID:        "session-1",
		ResumeGatewayURL: "ws://resume.invalid",
		Application:      Application{ID: "app-1"},
	})
	session.sequence.Store(17)

	if have := session.connectURL("ws://gateway.invalid"); have != "ws://resume.invalid/?v=10&encoding=json" {
		t.Errorf("connectURL() = %q, want the resume url", have)
	}

	session.forgetSession()

	if have := session.connectURL("ws://gateway.invalid"); have != "ws://gateway.invalid/?v=10&encoding=json" {
		t.Errorf("connectURL() = %q, want the discovered gateway url", have)
	}
	if have := session.sequence.Load(); have != noSequence {
		t.Errorf("sequence = %d, want %d", have, noSequence)
	}
}
