package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/websocket"
)

// Gateway close codes Discord will never accept a retry for; see
// https://discord.com/developers/docs/topics/opcodes-and-status-codes#gateway.
const (
	closeAuthenticationFailed = 4004
	closeInvalidShard         = 4010
	closeShardingRequired     = 4011
	closeInvalidAPIVersion    = 4012
	closeInvalidIntents       = 4013
	closeDisallowedIntents    = 4014
)

// closeResumeImpossible tells the gateway not to keep our session when we hang
// up, so the next connection starts clean.
const closeResumeImpossible = 4000

// Reconnect backoff bounds.
const (
	minReconnectDelay = time.Second
	maxReconnectDelay = time.Minute
)

// noSequence marks a session that has not received a sequence number yet, which
// serializes as a JSON null in a heartbeat.
const noSequence int64 = -1

// errResumeRejected means the gateway refused to continue the previous session.
var errResumeRejected = errors.New("session is no longer resumable")

// A Session keeps a gateway connection alive: it identifies, heartbeats,
// resumes after a drop and dispatches events to the registered handlers.
//
// Register handlers before calling [Session.Run]; they are not safe to change
// while it runs.
type Session struct {
	client  *Client
	token   string
	intents Intents
	logger  *slog.Logger

	onReady       func(context.Context, Ready)
	onInteraction func(context.Context, *Interaction)

	// sequence is the last received event number, read by the heartbeat goroutine
	// while the read loop writes it.
	sequence atomic.Int64

	// acknowledged tracks whether the gateway answered the previous heartbeat. A
	// missed acknowledgement means the connection is a zombie and must be dropped.
	acknowledged atomic.Bool

	// heartbeatSent is when the outstanding heartbeat went out, as Unix nanoseconds.
	heartbeatSent atomic.Int64

	// mu guards the session identity, which the read loop writes and callers read.
	mu            sync.RWMutex
	applicationID string
	sessionID     string
	resumeURL     string
	latency       time.Duration
}

// NewSession returns a session that authenticates with token and subscribes to
// intents. It does not connect; [Session.Run] does.
func NewSession(client *Client, token string, intents Intents, logger *slog.Logger) *Session {
	s := &Session{
		client:  client,
		token:   token,
		intents: intents,
		logger:  logger,
	}
	s.sequence.Store(noSequence)
	return s
}

// OnReady registers the handler called when a new session becomes ready. It runs
// on the connection's goroutine, so the read loop waits for it.
func (s *Session) OnReady(handler func(context.Context, Ready)) { s.onReady = handler }

// OnInteraction registers the handler called for every incoming interaction.
// Handlers run on their own goroutine and must answer within three seconds.
func (s *Session) OnInteraction(handler func(context.Context, *Interaction)) {
	s.onInteraction = handler
}

// ApplicationID is the bot's application id, known once the session is ready.
func (s *Session) ApplicationID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.applicationID
}

// Latency is the round trip of the most recently acknowledged heartbeat. It is
// zero until the first acknowledgement arrives.
func (s *Session) Latency() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latency
}

// Respond answers an interaction.
func (s *Session) Respond(ctx context.Context, interaction *Interaction, response InteractionResponse) error {
	return s.client.RespondToInteraction(ctx, interaction, response)
}

// PublishCommands makes Discord's view of the bot's commands match commands.
// It requires a ready session, since the application id comes from READY.
func (s *Session) PublishCommands(ctx context.Context, guildID string, commands []ApplicationCommand) ([]ApplicationCommand, error) {
	return s.client.BulkOverwriteCommands(ctx, s.ApplicationID(), guildID, commands)
}

// Run connects to the gateway and keeps it connected until ctx is cancelled,
// reconnecting with exponential backoff. It returns nil on cancellation and an
// error only when reconnecting cannot help, such as a rejected token.
func (s *Session) Run(ctx context.Context) error {
	gatewayURL, err := s.client.GatewayURL(ctx)
	if err != nil {
		return fmt.Errorf("discover gateway: %w", err)
	}

	delay := minReconnectDelay
	for {
		err := s.connect(ctx, s.connectURL(gatewayURL))
		if ctx.Err() != nil {
			return nil
		}
		if fatal := fatalCloseError(err); fatal != nil {
			return fatal
		}

		if errors.Is(err, errResumeRejected) {
			s.forgetSession()
		}
		s.logger.Warn("gateway connection lost, reconnecting", "error", err, "delay", delay)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

// connectURL prefers the resume endpoint Discord handed us for the interrupted
// session, falling back to the discovered gateway for a fresh one.
func (s *Session) connectURL(gatewayURL string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.sessionID != "" && s.resumeURL != "" {
		return s.resumeURL + "/?v=10&encoding=json"
	}
	return gatewayURL + "/?v=10&encoding=json"
}

// connect runs a single gateway connection from handshake to disconnect. It
// always returns a non-nil error: the connection ended for some reason.
func (s *Session) connect(ctx context.Context, gatewayURL string) error {
	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	conn, err := websocket.Dial(dialCtx, gatewayURL)
	cancelDial()
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}

	// Deferred calls run last to first, so this unwinds as: cancel the connection
	// context, wait for its goroutines, then hang up.
	defer func() { _ = conn.Close(closeResumeImpossible, "") }()

	interval, err := s.readHello(ctx, conn)
	if err != nil {
		return err
	}

	connCtx, cancel := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer running.Wait()
	defer cancel()

	s.acknowledged.Store(true)
	running.Add(1)
	go func() {
		defer running.Done()
		s.heartbeat(connCtx, conn, interval)
	}()

	if err := s.identify(connCtx, conn); err != nil {
		return err
	}
	return s.readLoop(connCtx, conn, &running)
}

// readHello consumes the Hello payload every connection opens with and returns
// the heartbeat interval it mandates.
func (s *Session) readHello(ctx context.Context, conn *websocket.Conn) (time.Duration, error) {
	helloCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	payload, err := conn.Read(helloCtx)
	if err != nil {
		return 0, fmt.Errorf("read hello: %w", err)
	}

	var received incoming
	if err := json.Unmarshal(payload, &received); err != nil {
		return 0, fmt.Errorf("decode hello: %w", err)
	}
	if received.Op != opHello {
		return 0, fmt.Errorf("expected hello, got %s", received.Op)
	}

	var body hello
	if err := json.Unmarshal(received.Data, &body); err != nil {
		return 0, fmt.Errorf("decode hello data: %w", err)
	}
	if body.HeartbeatIntervalMillis <= 0 {
		return 0, fmt.Errorf("invalid heartbeat interval %d", body.HeartbeatIntervalMillis)
	}
	return time.Duration(body.HeartbeatIntervalMillis) * time.Millisecond, nil
}

// readLoop reads payloads until the connection fails or ctx is cancelled.
func (s *Session) readLoop(ctx context.Context, conn *websocket.Conn, running *sync.WaitGroup) error {
	for {
		payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		var received incoming
		if err := json.Unmarshal(payload, &received); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		if received.Sequence > 0 {
			s.sequence.Store(received.Sequence)
		}

		switch received.Op {
		case opDispatch:
			s.dispatch(ctx, received, running)
		case opHeartbeat:
			// The gateway can ask for a beat out of band.
			if err := s.sendHeartbeat(ctx, conn); err != nil {
				return err
			}
		case opHeartbeatAcknowledge:
			s.acknowledge()
		case opReconnect:
			return errors.New("gateway asked us to reconnect")
		case opInvalidSession:
			var resumable bool
			if err := json.Unmarshal(received.Data, &resumable); err != nil || !resumable {
				return errResumeRejected
			}
			return errors.New("gateway invalidated the session")
		default:
			s.logger.Debug("ignoring gateway payload", "op", received.Op)
		}
	}
}

// dispatch decodes an event and hands it to its handler. Interaction handlers
// run on their own goroutine so a slow command cannot stall the read loop.
func (s *Session) dispatch(ctx context.Context, received incoming, running *sync.WaitGroup) {
	switch received.Type {
	case eventReady:
		var ready Ready
		if err := json.Unmarshal(received.Data, &ready); err != nil {
			s.logger.Error("decode ready", "error", err)
			return
		}
		s.rememberSession(ready)
		s.logger.Info("session ready",
			"user", ready.User.Username,
			"guilds", len(ready.Guilds),
			"session", ready.SessionID,
		)
		if s.onReady != nil {
			s.onReady(ctx, ready)
		}

	case eventResumed:
		s.logger.Info("session resumed", "sequence", s.sequence.Load())

	case eventInteractionCreate:
		if s.onInteraction == nil {
			return
		}
		var interaction Interaction
		if err := json.Unmarshal(received.Data, &interaction); err != nil {
			s.logger.Error("decode interaction", "error", err)
			return
		}
		running.Add(1)
		go func() {
			defer running.Done()
			s.onInteraction(ctx, &interaction)
		}()

	default:
		s.logger.Debug("ignoring event", "event", received.Type)
	}
}

// heartbeat keeps the connection alive on the interval the gateway asked for,
// and tears it down when a beat goes unacknowledged.
func (s *Session) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	// Discord asks for a random first delay so reconnecting bots do not sync up.
	timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if !s.acknowledged.Swap(false) {
			s.logger.Warn("heartbeat went unacknowledged, dropping the connection")
			// Closing unblocks the read loop, which drives the reconnect.
			_ = conn.Close(closeResumeImpossible, "heartbeat timeout")
			return
		}

		if err := s.sendHeartbeat(ctx, conn); err != nil {
			if ctx.Err() == nil {
				s.logger.Error("send heartbeat", "error", err)
			}
			return
		}
		timer.Reset(interval)
	}
}

// sendHeartbeat writes one heartbeat carrying the last sequence number.
func (s *Session) sendHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	var sequence any
	if last := s.sequence.Load(); last != noSequence {
		sequence = last
	}

	s.heartbeatSent.Store(time.Now().UnixNano())
	if err := s.send(ctx, conn, outgoing{Op: opHeartbeat, Data: sequence}); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	return nil
}

// acknowledge records a heartbeat acknowledgement and the round trip it measured.
func (s *Session) acknowledge() {
	s.acknowledged.Store(true)

	sent := s.heartbeatSent.Load()
	if sent == 0 {
		return
	}
	round := time.Since(time.Unix(0, sent))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = round
}

// identify starts a new session, or resumes the previous one when the gateway
// still has it.
func (s *Session) identify(ctx context.Context, conn *websocket.Conn) error {
	s.mu.RLock()
	sessionID := s.sessionID
	s.mu.RUnlock()

	if sessionID != "" {
		s.logger.Info("resuming session", "session", sessionID, "sequence", s.sequence.Load())
		return s.send(ctx, conn, outgoing{Op: opResume, Data: resume{
			Token:     s.token,
			SessionID: sessionID,
			Sequence:  s.sequence.Load(),
		}})
	}

	s.logger.Info("identifying", "intents", int(s.intents))
	return s.send(ctx, conn, outgoing{Op: opIdentify, Data: identify{
		Token:   s.token,
		Intents: s.intents,
		Properties: identifyProperties{
			OS:      runtime.GOOS,
			Browser: "cnayp-discord-bot",
			Device:  "cnayp-discord-bot",
		},
	}})
}

// send encodes and writes a gateway payload.
func (s *Session) send(ctx context.Context, conn *websocket.Conn, payload outgoing) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", payload.Op, err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := conn.Write(writeCtx, encoded); err != nil {
		return fmt.Errorf("write %s payload: %w", payload.Op, err)
	}
	return nil
}

// rememberSession stores what a later resume needs.
func (s *Session) rememberSession(ready Ready) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applicationID = ready.Application.ID
	s.sessionID = ready.SessionID
	s.resumeURL = ready.ResumeGatewayURL
}

// forgetSession discards resume state so the next connection identifies afresh.
func (s *Session) forgetSession() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessionID = ""
	s.resumeURL = ""
	s.sequence.Store(noSequence)
}

// fatalCloseError reports the error unchanged when reconnecting cannot fix it,
// and nil when a retry is worth attempting.
func fatalCloseError(err error) error {
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return nil
	}

	switch closeErr.Code {
	case closeAuthenticationFailed:
		return fmt.Errorf("discord rejected the bot token: %w", err)
	case closeInvalidIntents, closeDisallowedIntents:
		return fmt.Errorf("requested intents are invalid or not enabled for this bot: %w", err)
	case closeInvalidShard, closeShardingRequired, closeInvalidAPIVersion:
		return err
	default:
		return nil
	}
}
