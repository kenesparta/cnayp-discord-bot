// Package events reads the public calendars of upcoming cloud native events and
// normalizes them into a single shape.
//
// Two calendars are read: the CNCF community group calendar, which publishes
// local meetups as exact instants in a named time zone, and the Linux
// Foundation conference calendar, which publishes the flagship conferences as
// floating calendar dates with no clock time at all. Reconciling that
// difference is most of what this package does.
//
// It knows nothing about Discord. It sits beside internal/discord rather than
// above it, and must never import it.
package events

import "time"

// horizonMonths is how far ahead the calendars are read and announced. It is
// the window the requirement fixes, and both calendars are trimmed to it.
const horizonMonths = 6

// A Source names which calendar an event came from. It is half of an event's
// identity: the two calendars number their events independently — one with
// UUIDs, the other with database row ids — so an identifier only means
// something together with its source.
type Source string

// The calendars this package reads.
const (
	// SourceCommunity is the CNCF community group calendar: local meetups,
	// study groups and project calls, with exact start times.
	SourceCommunity Source = "community"

	// SourceConference is the Linux Foundation conference calendar restricted to
	// CNCF events: KubeCon and its co-located days, as calendar dates.
	SourceConference Source = "conference"
)

// A Kind says how an event is attended.
type Kind string

// The attendance kinds the calendars publish.
const (
	KindInPerson Kind = "in-person"
	KindVirtual  Kind = "virtual"
	KindHybrid   Kind = "hybrid"
)

// An Event is one upcoming event, normalized from either calendar.
//
// Start is exact for community events and date-only for conference events, and
// [Event.Exact] says which. A date-only start is midnight UTC of the published
// day and carries no meaningful clock time: rendering it as an instant would
// assert something the calendar never said.
type Event struct {
	// Source and ID together identify the event. Neither is unique on its own.
	Source Source
	ID     string

	// Title is the display title: sanitized, whitespace-collapsed and truncated.
	Title string

	// Start is the start instant, or midnight UTC of the start date when Exact
	// is false. End is the zero time when the calendar publishes no end.
	Start time.Time
	End   time.Time

	// Exact reports whether Start is a real instant rather than a bare date.
	Exact bool

	// DateTBA reports that the calendar published a placeholder instead of a
	// date. Such an event is kept and marked, never given an invented date.
	DateTBA bool

	// Zone is the IANA name of the zone Start happens in, empty for date-only
	// events. LocalDay is midnight of the event's own calendar day, which is what
	// a "this week" window must be bucketed by: an evening event in the Americas
	// falls on the next UTC day.
	Zone     string
	LocalDay time.Time

	// Kind, Place and Group describe where and by whom. Place is empty when the
	// calendar publishes no location; Group is empty for conference events.
	Kind  Kind
	Place string
	Group string

	// SeriesID is the calendar's own identifier for a recurring series, empty
	// when it publishes none. Collapsing falls back to the group and title.
	SeriesID string

	// URL is the canonical page for the event on its calendar.
	URL string

	// canceled, test and published are the calendar's own status flags. They are
	// unexported deliberately: they exist only until normalization drops the
	// records that fail them, and nothing outside this package should ever be
	// handed an event that was canceled or was never published.
	canceled  bool
	test      bool
	published bool
}

// Identity returns the key that identifies this event across runs. It is the
// only key a caller may de-duplicate on: titles repeat, slugs are scoped to a
// group, and the two calendars' identifiers live in unrelated namespaces.
func (e Event) Identity() string {
	return string(e.Source) + "/" + e.ID
}

// A Set is a group of normalized events, ordered by start instant.
type Set []Event
