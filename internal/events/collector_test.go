package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// discardLogger keeps test output clean; the collector logs every guard it trips.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubCalendar stands in for a source so a run's shape can be set exactly.
type stubCalendar struct {
	source  Source
	events  []Event
	skipped int
	err     error
	calls   int
}

func (s *stubCalendar) fetch(_ context.Context, _ time.Time) ([]Event, int, error) {
	s.calls++
	return s.events, s.skipped, s.err
}

func (s *stubCalendar) name() Source { return s.source }

// collectorWith builds a collector over two stub calendars.
func collectorWith(community, conference *stubCalendar) *Collector {
	return &Collector{
		community:  community,
		conference: conference,
		logger:     discardLogger(),
		lastGood:   make(map[Source]int, 2),
	}
}

// conferenceEventAt builds a published conference event some days out.
func conferenceEventAt(id string, days int) Event {
	return Event{
		Source:    SourceConference,
		ID:        id,
		Title:     "KubeCon + CloudNativeCon " + id,
		Start:     runClock.AddDate(0, 0, days),
		URL:       "https://events.linuxfoundation.org/" + id + "/",
		published: true,
	}
}

func TestCollectMergesBothCalendars(t *testing.T) {
	community := &stubCalendar{source: SourceCommunity, events: []Event{communityEventAt("a", 3)}}
	conference := &stubCalendar{source: SourceConference, events: []Event{conferenceEventAt("b", 10)}}

	set, health := collectorWith(community, conference).Collect(t.Context(), runClock)

	if len(set) != 2 {
		t.Fatalf("collected %d events, want 2", len(set))
	}
	if len(health) != 2 {
		t.Fatalf("reported %d calendars, want 2", len(health))
	}
	for _, report := range health {
		if !report.OK() {
			t.Errorf("%s = %q, want ok", report.Source, report.Status)
		}
		if report.Records != 1 {
			t.Errorf("%s recorded %d events, want 1", report.Source, report.Records)
		}
	}
}

func TestCollectTreatsAnEmptyCalendarAsAFailure(t *testing.T) {
	// A renamed filter answers 200 with an empty list. Announcing that as "no
	// events" would be a lie that lasts until someone notices, which is the
	// failure this guard exists for.
	community := &stubCalendar{source: SourceCommunity}
	conference := &stubCalendar{source: SourceConference, events: []Event{conferenceEventAt("b", 10)}}

	collector := collectorWith(community, conference)
	_, health := collector.Collect(t.Context(), runClock)

	report := health[0]
	if report.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", report.Status, StatusFailed)
	}
	var sourceErr *SourceError
	if !errors.As(report.Err, &sourceErr) || sourceErr.Code != CodeEmpty {
		t.Errorf("Err = %v, want %q", report.Err, CodeEmpty)
	}
	if collector.lastGood[SourceCommunity] != 0 {
		t.Error("an empty answer updated the baseline the degraded guard compares against")
	}
}

func TestCollectTreatsACollapsedCountAsDegraded(t *testing.T) {
	community := &stubCalendar{source: SourceCommunity}
	for i := range 100 {
		community.events = append(community.events, communityEventAt(string(rune('a'+i%26))+string(rune('a'+i/26)), i+1))
	}
	conference := &stubCalendar{source: SourceConference, events: []Event{conferenceEventAt("b", 10)}}
	collector := collectorWith(community, conference)

	if _, health := collector.Collect(t.Context(), runClock); !health[0].OK() {
		t.Fatalf("first run = %q, want ok", health[0].Status)
	}

	// The next run answers with a tenth of that.
	community.events = community.events[:10]
	_, health := collector.Collect(t.Context(), runClock)

	if health[0].Status != StatusDegraded {
		t.Errorf("Status = %q, want %q", health[0].Status, StatusDegraded)
	}
	var sourceErr *SourceError
	if !errors.As(health[0].Err, &sourceErr) || sourceErr.Code != CodeDegraded {
		t.Errorf("Err = %v, want %q", health[0].Err, CodeDegraded)
	}
}

func TestCollectAcceptsANormalFluctuation(t *testing.T) {
	community := &stubCalendar{source: SourceCommunity}
	for i := range 10 {
		community.events = append(community.events, communityEventAt(string(rune('a'+i)), i+1))
	}
	conference := &stubCalendar{source: SourceConference, events: []Event{conferenceEventAt("b", 10)}}
	collector := collectorWith(community, conference)

	collector.Collect(t.Context(), runClock)

	// Half the events is a plausible week, not a broken filter.
	community.events = community.events[:5]
	_, health := collector.Collect(t.Context(), runClock)

	if !health[0].OK() {
		t.Errorf("Status = %q, want ok: half is above the floor", health[0].Status)
	}
}

func TestCollectSurvivesOneCalendarFailing(t *testing.T) {
	community := &stubCalendar{
		source: SourceCommunity,
		err:    &SourceError{Source: SourceCommunity, Code: CodeUnreachable},
	}
	conference := &stubCalendar{source: SourceConference, events: []Event{
		conferenceEventAt("b", 10), conferenceEventAt("c", 20),
	}}

	set, health := collectorWith(community, conference).Collect(t.Context(), runClock)

	if len(set) != 2 {
		t.Errorf("collected %d events, want the healthy calendar's 2", len(set))
	}
	if health[0].Status != StatusFailed {
		t.Errorf("community status = %q, want %q", health[0].Status, StatusFailed)
	}
	if !health[1].OK() {
		t.Errorf("conference status = %q, want ok", health[1].Status)
	}
}

func TestCollectReturnsNothingWhenEveryCalendarFails(t *testing.T) {
	community := &stubCalendar{source: SourceCommunity, err: &SourceError{Code: CodeRejected, Status: 503}}
	conference := &stubCalendar{source: SourceConference, err: &SourceError{Code: CodeUnreachable}}

	set, health := collectorWith(community, conference).Collect(t.Context(), runClock)

	if len(set) != 0 {
		t.Errorf("collected %d events, want none", len(set))
	}
	for _, report := range health {
		if report.Status != StatusFailed {
			t.Errorf("%s = %q, want %q", report.Source, report.Status, StatusFailed)
		}
	}
}

func TestCollectReportsSkippedRecords(t *testing.T) {
	community := &stubCalendar{source: SourceCommunity, events: []Event{communityEventAt("a", 3)}, skipped: 2}
	conference := &stubCalendar{source: SourceConference, events: []Event{conferenceEventAt("b", 10)}}

	_, health := collectorWith(community, conference).Collect(t.Context(), runClock)

	if health[0].Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", health[0].Skipped)
	}
}

func TestNewReadsBothCalendarsOverOneClient(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == communitySearchPath {
			_, _ = io.WriteString(w, `{"total":0,"events":[]}`)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()

	collector := New(discardLogger(),
		WithHTTPClient(srv.Client()),
		WithCommunityBaseURL(srv.URL),
		WithConferenceBaseURL(srv.URL),
	)

	set, health := collector.Collect(t.Context(), runClock)

	if len(paths) != 2 {
		t.Fatalf("requested %v, want both calendars", paths)
	}
	if len(set) != 0 {
		t.Errorf("collected %d events from empty calendars, want none", len(set))
	}
	// Both answered, and both answered with nothing, which is a failure.
	for _, report := range health {
		if report.Status != StatusFailed {
			t.Errorf("%s = %q, want %q", report.Source, report.Status, StatusFailed)
		}
	}
}
