package events

import (
	"strings"
	"testing"
	"time"
)

var runClock = time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)

// communityEventAt builds a published community event starting after the run.
func communityEventAt(id string, days int) Event {
	return Event{
		Source:    SourceCommunity,
		ID:        id,
		Title:     "Cloud Native Meetup",
		Start:     runClock.AddDate(0, 0, days),
		Exact:     true,
		Zone:      "America/Lima",
		Kind:      KindInPerson,
		Group:     "Cloud Native Ayacucho",
		URL:       "https://ocgroups.dev/cncf/group/ncz2zfb/event/" + id,
		published: true,
	}
}

func TestFilterRemovesCanceledTestAndUnpublished(t *testing.T) {
	canceled := communityEventAt("a", 1)
	canceled.canceled = true
	test := communityEventAt("b", 2)
	test.test = true
	unpublished := communityEventAt("c", 3)
	unpublished.published = false
	good := communityEventAt("d", 4)

	set, _ := normalize([]Event{canceled, test, unpublished, good}, runClock)

	if len(set) != 1 || set[0].ID != "d" {
		t.Fatalf("kept %d events, want only the published one", len(set))
	}
}

func TestFilterRemovesEventsOutsideTheWindow(t *testing.T) {
	past := communityEventAt("past", -1)
	justGone := communityEventAt("just-gone", 0)
	soon := communityEventAt("soon", 1)
	beyond := communityEventAt("beyond", 200) // more than six months out

	set, _ := normalize([]Event{past, justGone, soon, beyond}, runClock)

	if len(set) != 1 || set[0].ID != "soon" {
		ids := make([]string, len(set))
		for i, event := range set {
			ids[i] = event.ID
		}
		t.Fatalf("kept %v, want only [soon]", ids)
	}
}

func TestPlaceholderDatesSurviveTheWindowFilter(t *testing.T) {
	// A conference with no settled date has no position on the timeline, so the
	// window filter has nothing to test it against. It must not be dropped.
	tba := Event{
		Source:    SourceConference,
		ID:        "319493",
		Title:     "Open Source Forum",
		DateTBA:   true,
		URL:       "https://events.linuxfoundation.org/open-source-forum/",
		published: true,
	}

	set, _ := normalize([]Event{tba}, runClock)

	if len(set) != 1 {
		t.Fatalf("kept %d events, want 1", len(set))
	}
}

func TestCollapseUsesTheSeriesIdentifierWhenPresent(t *testing.T) {
	var raw []Event
	for i, days := range []int{30, 10, 20, 40} {
		event := communityEventAt(string(rune('a'+i)), days)
		event.Title = "Cartografos Community Meeting " + string(rune('1'+i))
		event.SeriesID = "c1b7fdea-78f5-41c8-a128-c4eec1565351"
		raw = append(raw, event)
	}

	set, _ := normalize(raw, runClock)

	if len(set) != 1 {
		t.Fatalf("kept %d occurrences, want 1", len(set))
	}
	if want := runClock.AddDate(0, 0, 10); !set[0].Start.Equal(want) {
		t.Errorf("kept the occurrence at %v, want the earliest at %v", set[0].Start, want)
	}
}

func TestCollapseFallsBackToGroupAndNormalizedTitle(t *testing.T) {
	// Six identical monthly meetups, no series identifier, differing only in the
	// occurrence number organizers append.
	titles := []string{
		"CNCG Trivandrum Monthly Meetup",
		"cncg trivandrum monthly meetup #2",
		"CNCG Trivandrum Monthly Meetup - 3",
	}
	var raw []Event
	for i, title := range titles {
		event := communityEventAt(string(rune('a'+i)), 20-i*5)
		event.Title = title
		raw = append(raw, event)
	}

	set, _ := normalize(raw, runClock)

	if len(set) != 1 {
		titlesKept := make([]string, len(set))
		for i, event := range set {
			titlesKept[i] = event.Title
		}
		t.Fatalf("kept %v, want one occurrence", titlesKept)
	}
}

func TestCollapseKeepsSeriesOfDifferentGroupsApart(t *testing.T) {
	first := communityEventAt("a", 5)
	second := communityEventAt("b", 6)
	second.Group = "Cloud Native Lima"

	set, _ := normalize([]Event{first, second}, runClock)

	if len(set) != 2 {
		t.Errorf("kept %d events, want 2: different groups are different series", len(set))
	}
}

func TestCollapseDoesNotMergeAcrossSources(t *testing.T) {
	community := communityEventAt("a", 5)
	community.Title = "KubeCon + CloudNativeCon"
	community.Group = ""

	conference := Event{
		Source:    SourceConference,
		ID:        "319493",
		Title:     "KubeCon + CloudNativeCon",
		Start:     runClock.AddDate(0, 0, 6),
		URL:       "https://events.linuxfoundation.org/kubecon-na/",
		published: true,
	}

	set, _ := normalize([]Event{community, conference}, runClock)

	if len(set) != 2 {
		t.Errorf("kept %d events, want 2: the calendars are separate namespaces", len(set))
	}
}

func TestSanitizeStripsInvisibleAndControlCharacters(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"zero width space", "CNCF\u200bVancouver", "CNCFVancouver"},
		{"non breaking space", "Comprendre l'orchestration", "Comprendre l'orchestration"},
		{"control characters", "Cloud\x07Native\x00Day", "CloudNativeDay"},
		{"collapsed whitespace", "  Cloud   Native \n Day  ", "Cloud Native Day"},
		{"emoji survive", "🍻 Back-to-school Beers🍻", "🍻 Back-to-school Beers🍻"},
		{"right to left override", "Kubernetes\u202eDay", "KubernetesDay"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitize(tt.title); got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestTruncateTitleAtAWordBoundary(t *testing.T) {
	long := "Stop writing tickets and start writing YAML: Platform Engineering with Flux and Crossplane, " +
		"an evening of demonstrations and questions"

	got := truncate(long, titleLimit)

	if len([]rune(got)) > titleLimit {
		t.Errorf("truncate() returned %d runes, want at most %d", len([]rune(got)), titleLimit)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Errorf("truncate() = %q, want it to end with %q", got, ellipsis)
	}
	if strings.Contains(got, " "+ellipsis) {
		t.Errorf("truncate() = %q, want no space before the ellipsis", got)
	}
	if short := truncate("Cloud Native Meetup", titleLimit); short != "Cloud Native Meetup" {
		t.Errorf("truncate() shortened a title that fits: %q", short)
	}
}

func TestLocalDayUsesTheEventOwnZone(t *testing.T) {
	// 2026-08-20 01:30 UTC is still the evening of the 19th in Vancouver, and
	// that is the day the meetup belongs to.
	event := communityEventAt("a", 0)
	event.Start = time.Date(2026, 8, 21, 1, 30, 0, 0, time.UTC)
	event.Zone = "America/Vancouver"

	set, unknown := normalize([]Event{event}, runClock)

	if len(unknown) != 0 {
		t.Fatalf("unknown zones = %v, want none", unknown)
	}
	if day := set[0].LocalDay.Day(); day != 20 {
		t.Errorf("LocalDay = %v (day %d), want the 20th in Vancouver", set[0].LocalDay, day)
	}
}

func TestLocalDayFallsBackToUTCForAnUnknownZone(t *testing.T) {
	event := communityEventAt("a", 1)
	event.Zone = "Mars/Olympus"

	set, unknown := normalize([]Event{event}, runClock)

	if len(set) != 1 {
		t.Fatalf("kept %d events, want 1: an unknown zone must not drop an event", len(set))
	}
	if len(unknown) != 1 || unknown[0] != "Mars/Olympus" {
		t.Errorf("unknown zones = %v, want [Mars/Olympus]", unknown)
	}
	if set[0].LocalDay.Location() != time.UTC {
		t.Errorf("LocalDay location = %v, want UTC", set[0].LocalDay.Location())
	}
}

func TestLocalDayResolvesLegacyZoneNames(t *testing.T) {
	// Eleven of the hundred events in the measured window use these aliases.
	for _, zone := range []string{"US/Eastern", "Japan", "Brazil/West", "Asia/Calcutta"} {
		t.Run(zone, func(t *testing.T) {
			event := communityEventAt("a", 1)
			event.Zone = zone

			if _, unknown := normalize([]Event{event}, runClock); len(unknown) != 0 {
				t.Errorf("zone %q did not resolve; is the time zone database embedded?", zone)
			}
		})
	}
}

func TestSetIsOrderedByStartWithUndatedEventsLast(t *testing.T) {
	late := communityEventAt("late", 30)
	early := communityEventAt("early", 2)
	early.Group = "Cloud Native Lima"
	middle := communityEventAt("middle", 10)
	middle.Group = "Cloud Native Cusco"
	tba := Event{
		Source: SourceConference, ID: "t", Title: "Open Source Forum",
		DateTBA: true, URL: "https://x/", published: true,
	}

	set, _ := normalize([]Event{late, tba, middle, early}, runClock)

	got := make([]string, len(set))
	for i, event := range set {
		got[i] = event.ID
	}
	want := []string{"early", "middle", "late", "t"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}
