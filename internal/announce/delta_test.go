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

func TestDeltaIsEmptyOnTheFirstRun(t *testing.T) {
	// Seeding is silent, so a redeploy does not announce everything that is
	// already listed.
	set := events.Set{event(events.SourceCommunity, "a", 1), event(events.SourceCommunity, "b", 2)}
	seen := newSeenSet()

	if fresh := seen.delta(set, true); len(fresh) != 0 {
		t.Errorf("first run announced %d events, want none", len(fresh))
	}
	if !seen.seeded {
		t.Error("the first run did not seed the set")
	}
}

func TestDeltaReportsOnlyNewIdentities(t *testing.T) {
	seen := newSeenSet()
	first := events.Set{event(events.SourceCommunity, "a", 1)}
	seen.delta(first, true)

	second := events.Set{
		event(events.SourceCommunity, "a", 1),
		event(events.SourceCommunity, "b", 2),
		event(events.SourceConference, "319493", 40),
	}

	fresh := seen.delta(second, true)

	if len(fresh) != 2 {
		t.Fatalf("announced %d events, want 2", len(fresh))
	}
	if fresh[0].ID != "b" || fresh[1].ID != "319493" {
		t.Errorf("announced %q and %q, want b and 319493", fresh[0].ID, fresh[1].ID)
	}
}

func TestDeltaIsEmptyWhenNothingChanged(t *testing.T) {
	seen := newSeenSet()
	set := events.Set{event(events.SourceCommunity, "a", 1)}
	seen.delta(set, true)

	if fresh := seen.delta(set, true); len(fresh) != 0 {
		t.Errorf("announced %d events on an unchanged listing, want none", len(fresh))
	}
}

func TestDeltaKeepsTheTwoCalendarsApart(t *testing.T) {
	// The calendars number their events independently, so the same id in both is
	// two different events.
	seen := newSeenSet()
	seen.delta(events.Set{event(events.SourceCommunity, "42", 1)}, true)

	fresh := seen.delta(events.Set{
		event(events.SourceCommunity, "42", 1),
		event(events.SourceConference, "42", 5),
	}, true)

	if len(fresh) != 1 || fresh[0].Source != events.SourceConference {
		t.Errorf("announced %d events, want the conference one only", len(fresh))
	}
}

func TestDeltaAnnouncesAReturningEventAgain(t *testing.T) {
	// A meetup that is pulled and re-listed is news again.
	seen := newSeenSet()
	present := events.Set{event(events.SourceCommunity, "a", 1), event(events.SourceCommunity, "b", 2)}
	seen.delta(present, true)

	seen.delta(events.Set{event(events.SourceCommunity, "b", 2)}, true)
	fresh := seen.delta(present, true)

	if len(fresh) != 1 || fresh[0].ID != "a" {
		t.Errorf("announced %d events, want the returning one", len(fresh))
	}
}

func TestDeltaIgnoresARunWhereEveryCalendarFailed(t *testing.T) {
	// An empty listing because everything failed is not an empty listing.
	// Recording it would make every event look new tomorrow.
	seen := newSeenSet()
	set := events.Set{event(events.SourceCommunity, "a", 1)}
	seen.delta(set, true)

	if fresh := seen.delta(nil, false); fresh != nil {
		t.Errorf("a failed run announced %d events, want none", len(fresh))
	}

	if fresh := seen.delta(set, true); len(fresh) != 0 {
		t.Errorf("the failed run wiped the record: %d events look new", len(fresh))
	}
}

func TestDeltaOnAFailedFirstRunDoesNotSeed(t *testing.T) {
	seen := newSeenSet()

	seen.delta(nil, false)
	if seen.seeded {
		t.Fatal("a failed run seeded the set; the next run would announce nothing that is genuinely new")
	}

	// The first successful run still seeds silently.
	if fresh := seen.delta(events.Set{event(events.SourceCommunity, "a", 1)}, true); len(fresh) != 0 {
		t.Errorf("announced %d events, want none", len(fresh))
	}
}
