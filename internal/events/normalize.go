package events

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// titleLimit is the display budget for a title, in runes. Upstream titles are
// written by hundreds of organizers and occasionally run to a paragraph.
const titleLimit = 120

// ellipsis marks a title that was cut. The full title is one link away.
const ellipsis = "…"

// normalize turns the raw events of both calendars into the set that gets
// announced: sanitized, filtered to the window, collapsed by series and ordered
// by start.
//
// It also returns the zone names it could not resolve, so the caller can log
// them once rather than per event. An unresolved zone is not fatal: the event
// keeps its instant and is bucketed by its UTC day instead.
func normalize(raw []Event, now time.Time) (Set, []string) {
	var (
		unknownZones []string
		seenZone     = make(map[string]struct{})
		kept         = make([]Event, 0, len(raw))
	)

	horizon := now.AddDate(0, horizonMonths, 0)

	for _, event := range raw {
		if !event.published || event.canceled || event.test {
			continue
		}

		event.Title = sanitize(event.Title)
		event.Place = sanitize(event.Place)
		event.Group = sanitize(event.Group)
		if event.Title == "" {
			continue
		}
		event.Title = truncate(event.Title, titleLimit)

		if event.DateTBA {
			// No date to filter on, and none to invent. It survives to be listed
			// with its placeholder.
			kept = append(kept, event)
			continue
		}

		if !event.Start.After(now) || event.Start.After(horizon) {
			continue
		}

		if event.Exact {
			day, ok := localDay(event.Start, event.Zone)
			if !ok {
				if _, repeated := seenZone[event.Zone]; !repeated {
					seenZone[event.Zone] = struct{}{}
					unknownZones = append(unknownZones, event.Zone)
				}
			}
			event.LocalDay = day
		}

		kept = append(kept, event)
	}

	set := dedupeAcrossSources(collapse(kept))
	sortByStart(set)
	return set, unknownZones
}

// localDay returns midnight of the calendar day the instant falls on where the
// event happens, and reports whether the zone was known. A "this week" window
// has to be bucketed by that day rather than by the UTC one: an evening event
// in the Americas falls on the next UTC day, and a morning event in Asia on the
// previous one.
func localDay(start time.Time, zone string) (time.Time, bool) {
	location, err := time.LoadLocation(zone)
	if err != nil {
		utc := start.UTC()
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC), false
	}
	local := start.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location), true
}

// collapse reduces every recurring series to its next occurrence. Without it a
// handful of standing calls dominate any list: one working group accounts for
// seven of the hundred events in a six month window.
//
// The calendar's own series identifier is preferred where it publishes one,
// which it does for a minority of records. Everything else falls back to the
// organizing group, by its stable id where the calendar gives one, and a
// normalized title, which is why the normalization strips case, punctuation and
// the occurrence numbering organizers append.
func collapse(events []Event) Set {
	next := make(map[string]int, len(events))
	set := make(Set, 0, len(events))

	for _, event := range events {
		key := collapseKey(event)

		index, seen := next[key]
		if !seen {
			next[key] = len(set)
			set = append(set, event)
			continue
		}

		// Keep whichever occurrence comes first. A placeholder date never
		// displaces a real one.
		if betterOccurrence(event, set[index]) {
			set[index] = event
		}
	}
	return set
}

// dedupeAcrossSources removes the second listing of an event that two different
// calendars both carry.
//
// It exists for one real case: a Kubernetes Community Day appears on the CNCF
// website, and appears again on the community group calendar when its
// organizers also run a group. The two spell it differently — "KCD Gujarat
// 2026" against "Kubernetes Community Day Gujarat 2026" — so the match is made
// on a canonical title plus the day it starts, which is specific enough that
// two genuinely different events would have to share a name and a date to
// collide.
//
// The richer record wins: the community listing carries an exact start time, a
// venue and an organizing group where the website listing carries a bare date.
// The cost of that preference is a known edge: an event that is listed on the
// website first and joins the community calendar later changes which record
// represents it, and is therefore announced a second time. It happens at most
// once per event, only for the handful of events both calendars carry, and the
// alternative — preferring the poorer record — would trade it for permanently
// worse announcements.
//
// Two entries from the same calendar are left alone: collapsing series is that
// calendar's business and has already happened.
func dedupeAcrossSources(set Set) Set {
	first := make(map[string]int, len(set))
	deduped := make(Set, 0, len(set))

	for _, event := range set {
		key := crossSourceKey(event)
		if key == "" {
			deduped = append(deduped, event)
			continue
		}

		index, seen := first[key]
		if !seen {
			first[key] = len(deduped)
			deduped = append(deduped, event)
			continue
		}
		if deduped[index].Source == event.Source {
			deduped = append(deduped, event)
			continue
		}
		if richer(event, deduped[index]) {
			deduped[index] = event
		}
	}
	return deduped
}

// crossSourceKey identifies an event well enough to recognise it on another
// calendar. It is empty for an event with no settled date, which cannot be
// matched on one.
func crossSourceKey(event Event) string {
	if event.DateTBA || event.Start.IsZero() {
		return ""
	}
	return canonicalTitle(event.Title) + "|" + event.Start.UTC().Format(time.DateOnly)
}

// communityDayNames rewrites what one calendar spells out and the other
// abbreviates. KCD is the abbreviation of Kubernetes Community Day, and the two
// calendars do not agree on which to use.
var communityDayNames = strings.NewReplacer(
	"kubernetes community days", "kcd",
	"kubernetes community day", "kcd",
)

// canonicalTitle normalizes a title far enough that two calendars naming the
// same event arrive at the same string.
func canonicalTitle(title string) string {
	return communityDayNames.Replace(normalizeTitle(title))
}

// richer reports whether candidate carries more than current: an exact start
// beats a bare date, and a venue beats nothing.
func richer(candidate, current Event) bool {
	if candidate.Exact != current.Exact {
		return candidate.Exact
	}
	return current.Place == "" && candidate.Place != ""
}

// collapseKey is the identity of a series. It is qualified by source, so that
// two calendars listing an event under the same name stay two events.
func collapseKey(event Event) string {
	if event.SeriesID != "" {
		return string(event.Source) + "|series|" + event.SeriesID
	}
	group := event.groupKey
	if group == "" {
		group = strings.ToLower(event.Group)
	}
	return string(event.Source) + "|" + group + "|" + normalizeTitle(event.Title)
}

// betterOccurrence reports whether candidate should replace current as the
// occurrence shown for a series.
func betterOccurrence(candidate, current Event) bool {
	if current.DateTBA != candidate.DateTBA {
		return current.DateTBA
	}
	if candidate.DateTBA {
		return false
	}
	return candidate.Start.Before(current.Start)
}

// normalizeTitle reduces a title to what is stable across the occurrences of
// one series: lower case, letters and digits only, and without the leading or
// trailing numbering organizers use to distinguish occurrences ("#6",
// "2026:", "vol 3").
func normalizeTitle(title string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteRune(' ')
		}
	}

	words := strings.Fields(builder.String())
	for len(words) > 0 && isOccurrenceMarker(words[0]) {
		words = words[1:]
	}
	for len(words) > 0 && isOccurrenceMarker(words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// isOccurrenceMarker reports whether a word is only an occurrence number.
func isOccurrenceMarker(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// sortByStart orders the set earliest first, with the events whose date is not
// settled last: they have no position on a timeline.
func sortByStart(set Set) {
	sort.SliceStable(set, func(i, j int) bool {
		if set[i].DateTBA != set[j].DateTBA {
			return !set[i].DateTBA
		}
		return set[i].Start.Before(set[j].Start)
	})
}

// sanitize makes upstream text safe to place in a message: it removes the
// characters that do not render as themselves and collapses the whitespace that
// hides them.
//
// It deliberately does not escape markup. That is the renderer's job, because
// what needs escaping depends on where the text is being put, and this package
// does not know.
func sanitize(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))

	for _, r := range text {
		switch {
		case isInvisible(r):
			// Dropped outright: zero-width characters are how a title smuggles in
			// something a reader cannot see.
		case unicode.IsSpace(r):
			builder.WriteRune(' ')
		case unicode.IsControl(r):
			// Dropped.
		default:
			builder.WriteRune(r)
		}
	}

	return strings.Join(strings.Fields(builder.String()), " ")
}

// isInvisible reports whether a rune renders as nothing. Zero-width characters
// are how a title smuggles in something a reader cannot see, and they are
// already present in this data: an event description in the measured window
// carries a zero-width space, and a title carries a non-breaking one.
func isInvisible(r rune) bool {
	switch r {
	case '\u00ad', // soft hyphen
		'\u200b', // zero width space
		'\u200c', // zero width non-joiner
		'\u200d', // zero width joiner
		'\u2060', // word joiner
		'\ufeff': // zero width no-break space
		return true
	}
	// Cf covers the rest of the format characters, including the bidirectional
	// overrides that can reverse how a title reads.
	return unicode.Is(unicode.Cf, r)
}

// truncate cuts a title to limit runes, preferring to break on a word boundary,
// and marks the cut.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	cut := string(runes[:limit-1])
	if space := strings.LastIndex(cut, " "); space > limit/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ") + ellipsis
}
