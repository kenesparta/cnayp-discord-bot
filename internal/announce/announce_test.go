package announce

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// recordingHandler keeps every log record so a run's accounting can be asserted.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// text renders every record so assertions can look for what must not be there.
func (h *recordingHandler) text() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var rendered strings.Builder
	for _, record := range h.records {
		rendered.WriteString(record.Message)
		record.Attrs(func(attr slog.Attr) bool {
			rendered.WriteString(" " + attr.Key + "=" + attr.Value.String())
			return true
		})
		rendered.WriteString("\n")
	}
	return rendered.String()
}

// count returns how many records carry this message.
func (h *recordingHandler) count(message string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	found := 0
	for _, record := range h.records {
		if record.Message == message {
			found++
		}
	}
	return found
}

// stubCollector returns a fixed listing, as internal/events would.
type stubCollector struct {
	set    events.Set
	health []events.Health
	calls  int
}

func (s *stubCollector) Collect(context.Context, time.Time) (events.Set, []events.Health) {
	s.calls++
	return s.set, s.health
}

// fakePublisher records what would have been posted.
type fakePublisher struct {
	sent    []discord.Message
	channel string
	err     error
	failAt  int
}

func (p *fakePublisher) CreateMessage(_ context.Context, channelID string, message discord.Message) error {
	p.channel = channelID
	if p.err != nil && len(p.sent) >= p.failAt {
		return p.err
	}
	p.sent = append(p.sent, message)
	return nil
}

// healthy is the report of a run where both calendars answered.
func healthy() []events.Health {
	return []events.Health{
		{Source: events.SourceCommunity, Status: events.StatusOK, Records: 12},
		{Source: events.SourceConference, Status: events.StatusOK, Records: 4},
	}
}

// announcerFor builds an announcer over stubs, pinned to the test clock.
func announcerFor(t *testing.T, collector *stubCollector, publisher *fakePublisher) (*Announcer, *recordingHandler) {
	t.Helper()

	handler := &recordingHandler{}
	announcer := New(collector, publisher, "1539531942754517072", slog.New(handler))
	announcer.now = func() time.Time { return testClock }
	return announcer, handler
}

func TestRunPublishesNothingOnAQuietDay(t *testing.T) {
	// Silence is the design: a day with nothing new is a day with no message.
	set := events.Set{event(events.SourceCommunity, "a", 3)}
	collector := &stubCollector{set: set, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	// The first run seeds; the second finds nothing new.
	announcer.runOnce(t.Context(), testClock)
	publisher.sent = nil
	announcer.runOnce(t.Context(), testClock)

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages on a quiet day, want none", len(publisher.sent))
	}
}

func TestRunSeedsSilentlyThenAnnouncesWhatIsNew(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), testClock)
	if len(publisher.sent) != 0 {
		t.Fatalf("the first run published %d messages, want none", len(publisher.sent))
	}

	collector.set = append(collector.set, event(events.SourceCommunity, "b", 5))
	announcer.runOnce(t.Context(), testClock)

	if len(publisher.sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(publisher.sent))
	}
	content := publisher.sent[0].Content
	if !strings.Contains(content, "event/b") {
		t.Errorf("the new event was not announced:\n%s", content)
	}
	if strings.Contains(content, "event/a") {
		t.Errorf("an already announced event was announced again:\n%s", content)
	}
	if publisher.channel != "1539531942754517072" {
		t.Errorf("published to %q, want the configured channel", publisher.channel)
	}
}

func TestRunSuppressesNotificationsOnTheDaily(t *testing.T) {
	collector := &stubCollector{health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	collector.set = events.Set{event(events.SourceCommunity, "a", 3)}
	announcer.runOnce(t.Context(), testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 5))
	announcer.runOnce(t.Context(), testClock)

	if publisher.sent[0].Flags&discord.FlagSuppressNotifications == 0 {
		t.Error("the daily announcement would push a notification to every member")
	}
}

func TestRunPublishesTheDigestBeforeTheDailyOnMonday(t *testing.T) {
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if kindOf(monday) != KindWeekly {
		t.Fatalf("fixture is not the weekly slot")
	}

	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	// Seed, so that the second run has both a digest and something new to say.
	announcer.runOnce(t.Context(), monday)
	publisher.sent = nil
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	announcer.runOnce(t.Context(), monday)

	if len(publisher.sent) < 2 {
		t.Fatalf("published %d messages, want the digest and the daily", len(publisher.sent))
	}
	if !strings.Contains(publisher.sent[0].Content, "Esta semana") {
		t.Errorf("the first message is not the digest:\n%s", publisher.sent[0].Content)
	}
	last := publisher.sent[len(publisher.sent)-1].Content
	if !strings.Contains(last, "Nuevos eventos") {
		t.Errorf("the last message is not the daily announcement:\n%s", last)
	}
}

func TestRunPublishesTheDigestOnAFirstRun(t *testing.T) {
	// Seeding silences the daily message, never the digest: the digest is
	// computed from the listing, not from what is new.
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	collector := &stubCollector{
		set:    events.Set{event(events.SourceCommunity, "a", 1)},
		health: healthy(),
	}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), monday)

	if len(publisher.sent) != 1 {
		t.Fatalf("published %d messages, want the digest alone", len(publisher.sent))
	}
	if !strings.Contains(publisher.sent[0].Content, "Esta semana") {
		t.Errorf("the message is not the digest:\n%s", publisher.sent[0].Content)
	}
}

func TestRunPublishesNothingWhenEveryCalendarFailed(t *testing.T) {
	collector := &stubCollector{
		health: []events.Health{
			{Source: events.SourceCommunity, Status: events.StatusFailed},
			{Source: events.SourceConference, Status: events.StatusFailed},
		},
	}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	// Even on a Monday: a week described from nothing is not a quiet week.
	announcer.runOnce(t.Context(), time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC))

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages from a failed run, want none", len(publisher.sent))
	}
}

func TestRunPublishesWhenOneCalendarFailed(t *testing.T) {
	collector := &stubCollector{
		set: events.Set{event(events.SourceCommunity, "a", 3)},
		health: []events.Health{
			{Source: events.SourceCommunity, Status: events.StatusOK, Records: 1},
			{Source: events.SourceConference, Status: events.StatusFailed},
		},
	}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	announcer.runOnce(t.Context(), testClock)

	if len(publisher.sent) != 1 {
		t.Errorf("published %d messages, want the healthy calendar's news", len(publisher.sent))
	}
}

func TestRunStopsPublishingOnADeniedPost(t *testing.T) {
	// Retrying a permission failure in a loop is the documented way to have an
	// address blocked by Discord's edge.
	var set events.Set
	for i := range 12 {
		set = append(set, event(events.SourceCommunity, string(rune('a'+i)), i+1))
	}
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "seed", 1)}, health: healthy()}
	publisher := &fakePublisher{err: &discord.APIError{StatusCode: 403, Code: 50013, Message: "Missing Permissions"}}
	announcer, handler := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), testClock)
	collector.set = set
	announcer.runOnce(t.Context(), testClock)

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages despite being denied", len(publisher.sent))
	}
	if handler.count("announcement run") != 2 {
		t.Errorf("the run was accounted for %d times, want once per run", handler.count("announcement run"))
	}
}

func TestRunLogsOneOutcomeLineCarryingTheAccounting(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, handler := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), testClock)

	if got := handler.count("announcement run"); got != 1 {
		t.Fatalf("logged %d outcome lines, want exactly 1", got)
	}
	logged := handler.text()
	for _, want := range []string{"kind=", "events=", "new=", "messages=", "next="} {
		if !strings.Contains(logged, want) {
			t.Errorf("the outcome line is missing %q:\n%s", want, logged)
		}
	}
}

func TestRunLogsNothingAMemberWrote(t *testing.T) {
	// The Privacy Policy promises the logs carry no message content. The run's
	// accounting is counts and statuses only.
	set := events.Set{event(events.SourceCommunity, "a", 3)}
	collector := &stubCollector{set: set, health: healthy()}
	publisher := &fakePublisher{}
	announcer, handler := announcerFor(t, collector, publisher)

	announcer.runOnce(t.Context(), testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	announcer.runOnce(t.Context(), testClock)

	logged := handler.text()
	for _, forbidden := range []string{"Cloud Native Meetup", "Ayacucho", "ocgroups.dev", "1539531942754517072"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("the logs carry %q, which the Privacy Policy says they do not:\n%s", forbidden, logged)
		}
	}
}

func TestRunSkipsASlotItOverran(t *testing.T) {
	slot := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)

	if _, skipped := skippedSlot(slot, slot.Add(time.Minute)); skipped {
		t.Error("a run of one minute was reported as overrunning its slot")
	}

	// A run still going a day and a half later has passed the next slot.
	missed, skipped := skippedSlot(slot, slot.Add(36*time.Hour))
	if !skipped {
		t.Fatal("a run that outlasted the next slot was not reported")
	}
	if !missed.Equal(slot.AddDate(0, 0, 1)) {
		t.Errorf("reported %v as missed, want %v", missed, slot.AddDate(0, 0, 1))
	}
}

func TestAnnouncerDoesNotStartWithoutAChannel(t *testing.T) {
	handler := &recordingHandler{}
	announcer := New(&stubCollector{}, &fakePublisher{}, "", slog.New(handler))

	done := make(chan struct{})
	go func() {
		defer close(done)
		announcer.Run(t.Context())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return with no channel configured")
	}

	if !strings.Contains(handler.text(), "announcements are off") {
		t.Errorf("nothing was logged about the announcer being off:\n%s", handler.text())
	}
}

func TestRunReturnsWhenTheContextIsCancelled(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	announcer, _ := announcerFor(t, collector, &fakePublisher{})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		announcer.Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
	if collector.calls != 0 {
		t.Errorf("the collector ran %d times before the first slot", collector.calls)
	}
}
