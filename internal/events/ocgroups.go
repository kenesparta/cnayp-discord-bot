package events

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// communityBaseURL is the host serving the CNCF community group calendar.
	// It is not a documented API: the JSON comes from the same route the site's
	// own event search calls, on a service that deploys continuously. The shape
	// is therefore checked by a scheduled contract test, not assumed.
	communityBaseURL = "https://ocgroups.dev"

	// communitySearchPath is that route.
	communitySearchPath = "/explore/events/search"

	// communitySlug selects the CNCF community. A slug the server does not know
	// is not rejected: it answers 200 with an empty list, which is why an empty
	// result is treated as a failure rather than a quiet day.
	communitySlug = "cncf"

	// communityPageSize is the server's hard cap. Asking for more returns 422.
	communityPageSize = 100

	// communityMaxPages bounds the paging loop. The loop is driven by a total the
	// server controls, so it needs a ceiling of its own; 10 pages is an order of
	// magnitude above the ~100 events a six month window holds.
	communityMaxPages = 10
)

// A communitySource reads the CNCF community group calendar: local meetups,
// study groups and project calls, each with an exact start and a named zone.
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

// A communityPage is one page of the search response. The response carries no
// next-page link, so Total is what drives paging.
type communityPage struct {
	Total  int              `json:"total"`
	Events []communityEvent `json:"events"`
}

// A communityEvent is one record as the calendar publishes it.
//
// The payload also carries a popover_html blob of server-rendered markup on
// every record, roughly three fifths of the response by size, which is
// deliberately not declared here so that encoding/json discards it.
type communityEvent struct {
	ID        string `json:"event_id"`
	Name      string `json:"name"`
	StartsAt  int64  `json:"starts_at"`
	EndsAt    *int64 `json:"ends_at"`
	Timezone  string `json:"timezone"`
	Kind      string `json:"kind"`
	Canceled  bool   `json:"canceled"`
	Published bool   `json:"published"`
	TestEvent bool   `json:"test_event"`
	GroupName string `json:"group_name"`
	GroupSlug string `json:"group_slug"`
	Community string `json:"community_name"`
	Slug      string `json:"slug"`
	SeriesID  string `json:"event_series_id"`
	VenueCity string `json:"venue_city"`
	VenueName string `json:"venue_name"`
}

// fetch reads every page of the window and returns the events it could map,
// together with the number of records dropped as malformed.
func (s communitySource) fetch(ctx context.Context, now time.Time) ([]Event, int, error) {
	from, to := communityQueryWindow(now)

	var (
		collected []Event
		skipped   int
		seen      = make(map[string]struct{})
	)

	for page := range communityMaxPages {
		offset := page * communityPageSize

		var body communityPage
		if err := s.get(ctx, s.searchURL(from, to, offset), &body); err != nil {
			return nil, 0, err
		}

		for _, record := range body.Events {
			event, ok := record.event(s.baseURL)
			if !ok {
				skipped++
				continue
			}
			// Pages are ordered by a start time many events share, and the server
			// documents no tiebreaker, so the same record can straddle a boundary.
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			collected = append(collected, event)
		}

		if offset+communityPageSize >= body.Total {
			break
		}
	}

	return collected, skipped, nil
}

// searchURL builds one page request. Every parameter here is load bearing: an
// unknown one would be ignored silently rather than rejected, so none of them
// can be assumed to work without the contract test that proves it.
func (s communitySource) searchURL(from, to string, offset int) string {
	query := url.Values{}
	query.Set("community[0]", communitySlug)
	query.Set("date_from", from)
	query.Set("date_to", to)
	query.Set("sort_by", "date")
	query.Set("sort_direction", "asc")
	query.Set("limit", strconv.Itoa(communityPageSize))
	query.Set("offset", strconv.Itoa(offset))

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

// event maps a record onto the normalized shape. It reports false when the
// record is missing something without which the event cannot be announced at
// all: an identity, a title, a start, or the slugs its page address is built
// from.
func (r communityEvent) event(baseURL string) (Event, bool) {
	if r.ID == "" || strings.TrimSpace(r.Name) == "" || r.StartsAt == 0 {
		return Event{}, false
	}
	if r.GroupSlug == "" || r.Slug == "" {
		return Event{}, false
	}

	event := Event{
		Source:    SourceCommunity,
		ID:        r.ID,
		Title:     r.Name,
		Start:     time.Unix(r.StartsAt, 0).UTC(),
		Exact:     true,
		Zone:      r.Timezone,
		Kind:      communityKind(r.Kind),
		Place:     firstNonEmpty(r.VenueCity, r.VenueName),
		Group:     r.GroupName,
		SeriesID:  r.SeriesID,
		URL:       r.pageURL(baseURL),
		canceled:  r.Canceled,
		test:      r.TestEvent,
		published: r.Published,
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

// communityKind maps the calendar's attendance strings. An unrecognised value
// falls back to in person, which understates rather than overstates: a member
// who travels to an event that turned out to be online has lost an evening,
// while the reverse is a missed event.
func communityKind(raw string) Kind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(KindVirtual):
		return KindVirtual
	case string(KindHybrid):
		return KindHybrid
	default:
		return KindInPerson
	}
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
