package events

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// communityBaseURL is the host serving the CNCF community group calendar.
	// It is not a documented API: the JSON comes from the same route the site's
	// own explore calendar calls, on a service that deploys continuously. The
	// shape is therefore checked by a scheduled contract test, not assumed.
	communityBaseURL = "https://ocgroups.dev"

	// communitySearchPath is that route.
	//
	// Since cncf/open-community-groups#730 (2026-10-12) it serves only the
	// explore calendar and map widgets. It requires view_mode, refuses any
	// other with 422, and answers with a minimal projection of each event: no
	// time zone, venue, attendance kind, status flags, group name or series.
	// What it returns is filtered upstream to listed events (is_event_listed:
	// public, not canceled, not a test), ordered by start, and capped at 1,000
	// with a truncated flag beyond that. It ignores limit and offset.
	communitySearchPath = "/explore/events/search"

	// communitySlug selects the CNCF community. A slug the server does not know
	// is not rejected: it answers 200 with an empty list, which is why an empty
	// result is treated as a failure rather than a quiet day.
	communitySlug = "cncf"

	// communityViewMode selects the calendar widget's projection. The map's
	// keeps only events with a location, which drops the online ones.
	communityViewMode = "calendar"
)

// communityKinds are the attendance kinds the calendar files events under. The
// projection does not say which one an event is, but the search filters by it,
// so the source asks once per kind: the three answers partition the listing,
// which a contract test checks. An unknown kind is rejected with 422, so a
// renamed one fails loudly instead of dropping its events.
var communityKinds = []Kind{KindInPerson, KindVirtual, KindHybrid}

// A communitySource reads the CNCF community group calendar: local meetups,
// study groups and project calls, each with an exact start.
type communitySource struct {
	fetcher
	baseURL string
}

// newCommunitySource returns a source reading baseURL through client.
func newCommunitySource(client *http.Client, userAgent, baseURL string) communitySource {
	if baseURL == "" {
		baseURL = communityBaseURL
	}
	return communitySource{
		fetcher: fetcher{http: client, userAgent: userAgent, source: SourceCommunity},
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

// A communityPage is the whole answer for one kind. There is no paging: the
// projection is the listing, up to its cap.
type communityPage struct {
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
	Events    []communityEvent `json:"events"`
}

// A communityEvent is one record of the minimal projection. It also carries the
// event's coordinates, which are not declared here.
type communityEvent struct {
	ID       string `json:"event_id"`
	Name     string `json:"name"`
	StartsAt int64  `json:"starts_at"`
	EndsAt   *int64 `json:"ends_at"`
	// GroupSlug is the group's stable id, which the page address and the
	// series collapse are built from. GroupPretty is its readable slug, which
	// only some groups set.
	GroupSlug   string `json:"group_slug"`
	GroupPretty string `json:"group_slug_pretty"`
	Community   string `json:"community_name"`
	Slug        string `json:"slug"`
}

// fetch reads the window once per attendance kind and returns the events it
// could map, together with the number of records dropped as malformed.
func (s communitySource) fetch(ctx context.Context, now time.Time) ([]Event, int, error) {
	from, to := communityQueryWindow(now)

	var (
		collected []Event
		skipped   int
		seen      = make(map[string]struct{})
	)

	for _, kind := range communityKinds {
		var body communityPage
		if err := s.get(ctx, s.searchURL(from, to, kind), &body); err != nil {
			return nil, 0, err
		}
		// Past the cap the far end of the window is missing. A listing known to
		// be incomplete is not one to announce from, and failing keeps the
		// calendar's record as it was (internal/announce).
		if body.Truncated {
			return nil, 0, &SourceError{
				Source: s.source,
				Code:   CodeUnusable,
				Err:    fmt.Errorf("%s events truncated to %d of %d", kind, len(body.Events), body.Total),
			}
		}

		for _, record := range body.Events {
			event, ok := record.event(s.baseURL, kind)
			if !ok {
				skipped++
				continue
			}
			// The kinds partition the listing, so a repeat would be a change
			// upstream; keep the first rather than announce it twice.
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			collected = append(collected, event)
		}
	}

	return collected, skipped, nil
}

// searchURL builds the request for one kind. Every parameter here is load
// bearing, and none of them can be assumed to work without the contract test
// that proves it.
func (s communitySource) searchURL(from, to string, kind Kind) string {
	query := url.Values{}
	query.Set("community[0]", communitySlug)
	query.Set("date_from", from)
	query.Set("date_to", to)
	query.Set("kind[0]", string(kind))
	query.Set("view_mode", communityViewMode)

	return s.baseURL + communitySearchPath + "?" + query.Encode()
}

// communityQueryWindow returns the inclusive date bounds to ask for.
//
// The bounds are one day wider on each side than the window that is displayed,
// because the server filters on the UTC calendar day while an event belongs to
// its own local day: an evening meetup in Vancouver is filed under tomorrow's
// UTC date, and a morning one in Tokyo under yesterday's. Narrowing back to the
// intended window happens after the local day is known.
func communityQueryWindow(now time.Time) (from, to string) {
	start := now.UTC().AddDate(0, 0, -1)
	end := now.UTC().AddDate(0, horizonMonths, 1)
	return start.Format(time.DateOnly), end.Format(time.DateOnly)
}

// event maps a record onto the normalized shape, with the kind it was listed
// under. It reports false when the record is missing something without which
// the event cannot be announced at all: an identity, a title, a start, or the
// slugs its page address is built from.
//
// What the projection no longer carries is left empty rather than guessed. With
// no zone the event is bucketed by its UTC day (normalize), which can file an
// evening meetup in the Americas under the next day; with no venue an in-person
// event is shown without a place.
func (r communityEvent) event(baseURL string, kind Kind) (Event, bool) {
	if r.ID == "" || strings.TrimSpace(r.Name) == "" || r.StartsAt == 0 {
		return Event{}, false
	}
	if r.GroupSlug == "" || r.Slug == "" {
		return Event{}, false
	}

	event := Event{
		Source:   SourceCommunity,
		ID:       r.ID,
		Title:    r.Name,
		Start:    time.Unix(r.StartsAt, 0).UTC(),
		Exact:    true,
		Kind:     kind,
		Group:    groupName(r.GroupPretty),
		groupKey: r.GroupSlug,
		URL:      r.pageURL(baseURL),
		// Upstream lists only events that are public, not canceled and not a
		// test, and nothing else reaches this projection.
		published: true,
	}
	if r.EndsAt != nil {
		event.End = time.Unix(*r.EndsAt, 0).UTC()
	}
	return event, true
}

// pageURL composes the canonical page address. The payload carries no URL of
// its own, and the pretty form of a group slug is not stable, so the address is
// built from the ids the record does carry.
func (r communityEvent) pageURL(baseURL string) string {
	community := r.Community
	if community == "" {
		community = communitySlug
	}
	return baseURL + "/" + community + "/group/" + r.GroupSlug + "/event/" + r.Slug
}

// groupName turns a readable slug into a display name:
// "cloud-native-ayacucho" reads "Cloud Native Ayacucho". It is a guess at the
// group's real name, which the projection does not carry, and it can miss an
// acronym's case ("cncf-lima" reads "Cncf Lima"). Most groups set no readable
// slug, and then the group is shown without a name.
func groupName(pretty string) string {
	words := strings.FieldsFunc(pretty, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	for i, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[i] = string(unicode.ToUpper(first)) + word[size:]
	}
	return strings.Join(words, " ")
}
