package announce

import (
	"testing"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

var testClock = time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)

// event builds a normalized event as internal/events would hand it over.
func event(source events.Source, id string, days int) events.Event {
	start := testClock.AddDate(0, 0, days)
	return events.Event{
		Source:   source,
		ID:       id,
		Title:    "Cloud Native Meetup " + id,
		Start:    start,
		Exact:    source == events.SourceCommunity,
		Zone:     "America/Lima",
		LocalDay: time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC),
		Kind:     events.KindInPerson,
		Place:    "Ayacucho, PE",
		Group:    "Cloud Native Ayacucho",
		URL:      "https://ocgroups.dev/cncf/group/ncz2zfb/event/" + id,
	}
}

func ok(source events.Source, records int) events.Health {
	return events.Health{Source: source, Status: events.StatusOK, Records: records}
}

func failed(source events.Source) events.Health {
	return events.Health{Source: source, Status: events.StatusFailed}
}

func identities(set events.Set) []string {
	out := make([]string, 0, len(set))
	for _, e := range set {
		out = append(out, e.Identity())
	}
	return out
}

func TestDeltaSeedsACalendarWithNoRecordSilently(t *testing.T) {
	set := events.Set{event(events.SourceCommunity, "a", 1), event(events.SourceCommunity, "b", 2)}

	fresh, updates := delta(nil, set, []events.Health{ok(events.SourceCommunity, 2)})

	if len(fresh) != 0 {
		t.Errorf("a first run announced %v", identities(fresh))
	}
	got := updates[events.SourceCommunity]
	if len(got.Seen) != 2 || got.Floor != 2 {
		t.Errorf("record = %+v, want both identities and a floor of 2", got)
	}
}

func TestDeltaAnnouncesOnlyWhatIsNew(t *testing.T) {
	records := map[events.Source]Record{events.SourceCommunity: {Seen: []string{"community/a"}, Floor: 1}}
	set := events.Set{event(events.SourceCommunity, "a", 1), event(events.SourceCommunity, "b", 2)}

	fresh, updates := delta(records, set, []events.Health{ok(events.SourceCommunity, 2)})

	if got := identities(fresh); len(got) != 1 || got[0] != "community/b" {
		t.Errorf("fresh = %v, want [community/b]", got)
	}
	if seen := updates[events.SourceCommunity].Seen; len(seen) != 2 {
		t.Errorf("the record was not replaced by the current listing: %v", seen)
	}
}

func TestDeltaForgetsWhatDisappearedUpstream(t *testing.T) {
	// A cancelled-then-rescheduled meetup is news when it comes back.
	records := map[events.Source]Record{events.SourceCommunity: {Seen: []string{"community/a", "community/b"}}}
	_, updates := delta(records, events.Set{event(events.SourceCommunity, "a", 1)},
		[]events.Health{ok(events.SourceCommunity, 1)})

	if seen := updates[events.SourceCommunity].Seen; len(seen) != 1 || seen[0] != "community/a" {
		t.Errorf("record = %v, want only what is listed now", seen)
	}
}

func TestDeltaKeepsAFailedCalendarsRecord(t *testing.T) {
	// The conference calendar is down today: its events are absent for a
	// reason that has nothing to do with them, so its record must survive, or
	// they would all be announced again when it recovers.
	records := map[events.Source]Record{
		events.SourceCommunity:  {Seen: []string{"community/a"}, Floor: 1},
		events.SourceConference: {Seen: []string{"conference/k"}, Floor: 4},
	}
	set := events.Set{event(events.SourceCommunity, "a", 1)}

	fresh, updates := delta(records, set, []events.Health{ok(events.SourceCommunity, 1), failed(events.SourceConference)})

	if len(fresh) != 0 {
		t.Errorf("fresh = %v, want nothing", identities(fresh))
	}
	if _, touched := updates[events.SourceConference]; touched {
		t.Error("the failed calendar's record was replaced")
	}

	// It recovers with one genuinely new event: only that one is news.
	set = append(set, event(events.SourceConference, "k", 30), event(events.SourceConference, "m", 40))
	fresh, _ = delta(records, set, []events.Health{ok(events.SourceCommunity, 1), ok(events.SourceConference, 5)})

	if got := identities(fresh); len(got) != 1 || got[0] != "conference/m" {
		t.Errorf("after recovery fresh = %v, want [conference/m]", got)
	}
}

func TestDeltaTreatsADegradedCalendarAsFailed(t *testing.T) {
	records := map[events.Source]Record{events.SourceCommunity: {Seen: []string{"community/a"}, Floor: 100}}
	degraded := events.Health{Source: events.SourceCommunity, Status: events.StatusDegraded, Records: 3}

	_, updates := delta(records, nil, []events.Health{degraded})

	if _, touched := updates[events.SourceCommunity]; touched {
		t.Error("a degraded answer replaced the record, and its count would become the floor")
	}
}

func TestDeltaSeedsANewCalendarWithoutSilencingTheOthers(t *testing.T) {
	// A calendar added later starts with no record. Its listing is seeded; the
	// calendars that already had records still announce.
	records := map[events.Source]Record{events.SourceCommunity: {Seen: []string{"community/a"}}}
	set := events.Set{
		event(events.SourceCommunity, "a", 1),
		event(events.SourceCommunity, "b", 2),
		event(events.SourceKCD, "x", 3),
	}

	fresh, updates := delta(records, set, []events.Health{ok(events.SourceCommunity, 2), ok(events.SourceKCD, 1)})

	if got := identities(fresh); len(got) != 1 || got[0] != "community/b" {
		t.Errorf("fresh = %v, want [community/b]", got)
	}
	if seen := updates[events.SourceKCD].Seen; len(seen) != 1 {
		t.Errorf("the new calendar was not seeded: %v", seen)
	}
}

func TestFloorsCarryTheStoredCounts(t *testing.T) {
	floors := floorsOf(map[events.Source]Record{events.SourceCommunity: {Floor: 12}})
	if floors[events.SourceCommunity] != 12 {
		t.Errorf("floors = %v", floors)
	}
}
