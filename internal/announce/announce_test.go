package announce

import (
	"context"
	"errors"
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

// stubCollector returns a fixed listing, as internal/events would, and keeps
// the floors it was handed.
type stubCollector struct {
	set    events.Set
	health []events.Health
	calls  int
	floors events.Floors
}

func (s *stubCollector) Collect(_ context.Context, _ time.Time, floors events.Floors) (events.Set, []events.Health) {
	s.calls++
	s.floors = floors
	return s.set, s.health
}

// memStore is a Store in a map, failing on demand.
type memStore struct {
	records  map[events.Source]Record
	digests  map[string]bool
	loadErr  error
	saveErr  error
	claimErr error
}

func newMemStore() *memStore {
	return &memStore{records: map[events.Source]Record{}, digests: map[string]bool{}}
}

func (m *memStore) Load(_ context.Context, sources []events.Source) (map[events.Source]Record, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	out := map[events.Source]Record{}
	for _, source := range sources {
		if record, ok := m.records[source]; ok {
			out[source] = record
		}
	}
	return out, nil
}

func (m *memStore) Save(_ context.Context, source events.Source, record Record) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.records[source] = record
	return nil
}

func (m *memStore) ClaimDigest(_ context.Context, day time.Time) (bool, error) {
	if m.claimErr != nil {
		return false, m.claimErr
	}
	key := day.UTC().Format(time.DateOnly)
	if m.digests[key] {
		return false, nil
	}
	m.digests[key] = true
	return true, nil
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

// announcerFor builds an announcer over stubs and an empty store, pinned to the
// test clock.
func announcerFor(t *testing.T, collector *stubCollector, publisher *fakePublisher) (*Announcer, *recordingHandler) {
	t.Helper()

	handler := &recordingHandler{}
	announcer := New(collector, publisher, newMemStore(), "1539531942754517072", slog.New(handler))
	announcer.now = func() time.Time { return testClock }
	return announcer, handler
}

// storeOf returns the announcer's fake store.
func storeOf(a *Announcer) *memStore { return a.store.(*memStore) }

// mustRun performs one run at now and fails the test if it returned an error.
func mustRun(t *testing.T, a *Announcer, now time.Time) Outcome {
	t.Helper()
	outcome, err := a.runAt(t.Context(), now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return outcome
}

func TestRunPublishesNothingOnAQuietDay(t *testing.T) {
	// Silence is the design: a day with nothing new is a day with no message.
	set := events.Set{event(events.SourceCommunity, "a", 3)}
	collector := &stubCollector{set: set, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	// The first run seeds; the second finds nothing new.
	mustRun(t, announcer, testClock)
	publisher.sent = nil
	mustRun(t, announcer, testClock)

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages on a quiet day, want none", len(publisher.sent))
	}
}

func TestRunSeedsSilentlyThenAnnouncesWhatIsNew(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	mustRun(t, announcer, testClock)
	if len(publisher.sent) != 0 {
		t.Fatalf("the first run published %d messages, want none", len(publisher.sent))
	}

	collector.set = append(collector.set, event(events.SourceCommunity, "b", 5))
	mustRun(t, announcer, testClock)

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
	mustRun(t, announcer, testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 5))
	mustRun(t, announcer, testClock)

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

	// Seed the day before, so that Monday's run has both a digest and something
	// new to say. Seeding on the Monday itself would spend its one digest.
	mustRun(t, announcer, monday.AddDate(0, 0, -1))
	publisher.sent = nil
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	mustRun(t, announcer, monday)

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

	mustRun(t, announcer, monday)

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
	mustRun(t, announcer, time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC))

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

	mustRun(t, announcer, testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	mustRun(t, announcer, testClock)

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

	mustRun(t, announcer, testClock)
	collector.set = set
	mustRun(t, announcer, testClock)

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

	mustRun(t, announcer, testClock)

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

	mustRun(t, announcer, testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 4))
	mustRun(t, announcer, testClock)

	logged := handler.text()
	for _, forbidden := range []string{"Cloud Native Meetup", "Ayacucho", "ocgroups.dev", "1539531942754517072"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("the logs carry %q, which the Privacy Policy says they do not:\n%s", forbidden, logged)
		}
	}
}

func TestRunIsOffWithoutAChannel(t *testing.T) {
	handler := &recordingHandler{}
	collector := &stubCollector{}
	announcer := New(collector, &fakePublisher{}, newMemStore(), "", slog.New(handler))

	outcome := mustRun(t, announcer, testClock)

	if !outcome.Off || collector.calls != 0 {
		t.Errorf("outcome = %+v after %d collections, want off and none", outcome, collector.calls)
	}
	if !strings.Contains(handler.text(), "announcements are off") {
		t.Errorf("nothing was logged about the announcer being off:\n%s", handler.text())
	}
}

func TestRunHandsTheStoredFloorsToTheCollector(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 3)}, health: healthy()}
	announcer, _ := announcerFor(t, collector, &fakePublisher{})

	mustRun(t, announcer, testClock)
	mustRun(t, announcer, testClock)

	if collector.floors[events.SourceCommunity] != 12 {
		t.Errorf("floors = %v, want the community count of the run before", collector.floors)
	}
}

func TestRunRecoversAFailedCalendarWithoutRepeatingIt(t *testing.T) {
	conference := event(events.SourceConference, "k", 30)
	collector := &stubCollector{
		set:    events.Set{event(events.SourceCommunity, "a", 3), conference},
		health: healthy(),
	}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)
	mustRun(t, announcer, testClock)

	// A day with the conference calendar down, then the day it recovers.
	collector.set = events.Set{event(events.SourceCommunity, "a", 3)}
	collector.health = []events.Health{healthy()[0], failed(events.SourceConference)}
	mustRun(t, announcer, testClock)
	collector.set = events.Set{event(events.SourceCommunity, "a", 3), conference}
	collector.health = healthy()
	mustRun(t, announcer, testClock)

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages, want none: nothing is new", len(publisher.sent))
	}
}

func TestRunPublishesTheDigestOncePerDay(t *testing.T) {
	// The schedule delivers at least once, so one Monday can run twice.
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 1)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)

	mustRun(t, announcer, monday)
	mustRun(t, announcer, monday.Add(time.Minute))

	if len(publisher.sent) != 1 {
		t.Errorf("published %d messages, want the digest once", len(publisher.sent))
	}
	mustRun(t, announcer, monday.AddDate(0, 0, 7))
	if len(publisher.sent) != 2 {
		t.Errorf("the next Monday's digest was not published")
	}
}

func TestRunSkipsTheDigestWhenItCannotBeRecorded(t *testing.T) {
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 1)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)
	storeOf(announcer).claimErr = errors.New("throttled")

	mustRun(t, announcer, monday)

	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages, want no digest it could not record", len(publisher.sent))
	}
}

func TestRunFailsBeforePublishingWhenTheRecordsCannotBeLoaded(t *testing.T) {
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 1)}, health: healthy()}
	announcer, _ := announcerFor(t, collector, &fakePublisher{})
	storeOf(announcer).loadErr = errors.New("unreachable")

	if _, err := announcer.runAt(t.Context(), testClock); err == nil {
		t.Fatal("the run succeeded without its records")
	}
	if collector.calls != 0 {
		t.Error("the calendars were read although the run could not record them")
	}
}

func TestRunFailsBeforePublishingWhenARecordCannotBeSaved(t *testing.T) {
	// Saving comes before publishing, so a failure here leaves nothing posted
	// and the run can be repeated.
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 1)}, health: healthy()}
	publisher := &fakePublisher{}
	announcer, _ := announcerFor(t, collector, publisher)
	storeOf(announcer).saveErr = errors.New("throttled")

	if _, err := announcer.runAt(t.Context(), monday); err == nil {
		t.Fatal("the run succeeded without saving its records")
	}
	if len(publisher.sent) != 0 {
		t.Errorf("published %d messages before the records were saved", len(publisher.sent))
	}
}

func TestRunDoesNotReturnAPublishingFailure(t *testing.T) {
	// A retry would post again what already went out.
	collector := &stubCollector{set: events.Set{event(events.SourceCommunity, "a", 1)}, health: healthy()}
	publisher := &fakePublisher{err: errors.New("discord is down")}
	announcer, _ := announcerFor(t, collector, publisher)
	mustRun(t, announcer, testClock)
	collector.set = append(collector.set, event(events.SourceCommunity, "b", 2))

	outcome, err := announcer.runAt(t.Context(), testClock)

	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if outcome.New != 1 || outcome.Published != 0 {
		t.Errorf("outcome = %+v, want one new event and nothing published", outcome)
	}
}
