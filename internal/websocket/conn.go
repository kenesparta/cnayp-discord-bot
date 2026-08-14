// Package websocket implements the client half of the WebSocket protocol
// (RFC 6455) on top of the standard library, limited to what a JSON gateway
// needs: a TLS dial, text messages, ping/pong and an orderly close.
//
// It is deliberately not a general-purpose library. Extensions, compression and
// server-side operation are out of scope, and unsupported input is rejected
// rather than tolerated.
package websocket

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Close status codes from RFC 6455 section 7.4.1, plus the Discord range.
const (
	StatusNormalClosure = 1000
	StatusGoingAway     = 1001
	StatusProtocolError = 1002
)

// handshakeGUID is the fixed value RFC 6455 section 1.3 mixes into the accept key.
const handshakeGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// defaultReadLimit caps a single message. Gateway READY payloads for a bot in
// many guilds are the largest thing we expect, and they stay far below this.
const defaultReadLimit = 8 << 20

// A CloseError reports that the peer closed the connection with a status code.
// Callers use it to tell a retryable disconnect from a fatal one.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("websocket closed with status %d", e.Code)
	}
	return fmt.Sprintf("websocket closed with status %d: %s", e.Code, e.Reason)
}

// A Conn is a client connection. Read and Write may each be called from one
// goroutine at a time; a reader and a writer may run concurrently.
type Conn struct {
	conn      net.Conn
	br        *bufio.Reader
	readLimit int64

	rmu sync.Mutex // serializes readers
	wmu sync.Mutex // serializes writers, including control frames from a reader

	closeOnce sync.Once
	closeErr  error
}

// Dial opens a connection to a ws:// or wss:// URL and performs the opening
// handshake. ctx bounds the dial and the handshake, not the connection's life.
func Dial(ctx context.Context, rawURL string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	var defaultPort string
	switch u.Scheme {
	case "wss":
		defaultPort = "443"
	case "ws":
		defaultPort = "80"
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), defaultPort)
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	if u.Scheme == "wss" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}

	c := &Conn{conn: conn, br: bufio.NewReader(conn), readLimit: defaultReadLimit}
	if err := c.handshake(ctx, u); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// handshake performs the HTTP Upgrade exchange of RFC 6455 section 4.1.
func (c *Conn) handshake(ctx context.Context, u *url.URL) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set handshake deadline: %w", err)
		}
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate handshake key: %w", err)
	}
	challenge := base64.StdEncoding.EncodeToString(nonce[:])

	// http.Request writes the request line and headers in canonical form; the
	// URL scheme is irrelevant to it, only the path and query are used.
	requestURL := *u
	requestURL.Scheme = "http"
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &requestURL,
		Host:   u.Host,
		Header: http.Header{
			"Upgrade":               {"websocket"},
			"Connection":            {"Upgrade"},
			"Sec-WebSocket-Key":     {challenge},
			"Sec-WebSocket-Version": {"13"},
		},
	}
	if err := req.Write(c.conn); err != nil {
		return fmt.Errorf("write handshake request: %w", err)
	}

	resp, err := http.ReadResponse(c.br, req)
	if err != nil {
		return fmt.Errorf("read handshake response: %w", err)
	}
	// The body is empty on a 101 and the connection is now ours; on a failure the
	// connection is discarded either way, so closing the body is all that is owed.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("handshake rejected with status %s", resp.Status)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("unexpected Upgrade header %q", resp.Header.Get("Upgrade"))
	}
	if !headerContainsToken(resp.Header.Get("Connection"), "upgrade") {
		return fmt.Errorf("unexpected Connection header %q", resp.Header.Get("Connection"))
	}
	if accept := resp.Header.Get("Sec-WebSocket-Accept"); accept != acceptKey(challenge) {
		return fmt.Errorf("invalid Sec-WebSocket-Accept %q", accept)
	}
	if extensions := resp.Header.Get("Sec-WebSocket-Extensions"); extensions != "" {
		return fmt.Errorf("server negotiated unsupported extension %q", extensions)
	}
	return nil
}

// acceptKey derives the value the server must echo in Sec-WebSocket-Accept.
// The SHA-1 here is prescribed by RFC 6455 and is not used as a security primitive.
func acceptKey(challenge string) string {
	sum := sha1.Sum([]byte(challenge + handshakeGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerContainsToken reports whether a comma-separated header value contains
// token, compared case-insensitively.
func headerContainsToken(value, token string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// Read returns the payload of the next data message, reassembling fragments and
// answering control frames along the way. Cancelling ctx unblocks the read and
// leaves the connection unusable.
//
// A close frame from the peer is reported as a [*CloseError].
func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetReadDeadline(time.Now()) })
	defer stop()

	var (
		message    []byte
		fragmented bool
	)
	for {
		f, err := readFrame(c.br, c.readLimit)
		if err != nil {
			return nil, err
		}

		switch f.opcode {
		case opPing:
			if err := c.writeFrame(opPong, f.payload); err != nil {
				return nil, fmt.Errorf("write pong: %w", err)
			}
		case opPong:
			// Unsolicited pongs are legal and carry no information we use.
		case opClose:
			return nil, c.peerClosed(f.payload)
		case opText, opBinary:
			if fragmented {
				return nil, errors.New("data frame inside a fragmented message")
			}
			if f.fin {
				return f.payload, nil
			}
			message, fragmented = f.payload, true
		case opContinuation:
			if !fragmented {
				return nil, errors.New("continuation frame outside a fragmented message")
			}
			if int64(len(message)+len(f.payload)) > c.readLimit {
				return nil, fmt.Errorf("message exceeds read limit %d", c.readLimit)
			}
			message = append(message, f.payload...)
			if f.fin {
				return message, nil
			}
		default:
			return nil, fmt.Errorf("unknown opcode %#x", f.opcode)
		}
	}
}

// peerClosed parses a close frame, echoes it as RFC 6455 section 5.5.1 requires
// and reports it to the caller.
func (c *Conn) peerClosed(payload []byte) error {
	closeErr := &CloseError{Code: StatusNormalClosure}
	if len(payload) >= 2 {
		closeErr.Code = int(binary.BigEndian.Uint16(payload))
		closeErr.Reason = string(payload[2:])
	}
	// Best effort: the peer is already going away.
	_ = c.writeFrame(opClose, payload)
	return closeErr
}

// Write sends data as a single text message.
func (c *Conn) Write(ctx context.Context, data []byte) error {
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Now()) })
	defer stop()

	return c.writeFrame(opText, data)
}

// writeFrame serializes concurrent writers, so a pong from the read loop cannot
// interleave with a message from the writer.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.conn, opcode, payload)
}

// Close sends a close frame with the given status and closes the underlying
// connection. It is safe to call more than once and from any goroutine, and it
// unblocks a concurrent Read.
func (c *Conn) Close(code int, reason string) error {
	c.closeOnce.Do(func() {
		payload := make([]byte, 2, 2+len(reason))
		binary.BigEndian.PutUint16(payload, uint16(code))
		payload = append(payload, reason...)

		// A best-effort goodbye: if the peer already vanished there is nothing to
		// report, the connection is going away regardless.
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = c.writeFrame(opClose, payload)

		c.closeErr = c.conn.Close()
	})
	return c.closeErr
}
