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
	// conferenceBaseURL is the host serving the Linux Foundation event calendar.
	// Unlike the community calendar, this one is advertised for machine
	// consumption by the host's own llms.txt.
	conferenceBaseURL = "https://events.linuxfoundation.org"

	// conferencePath is the calendar's JSON route.
	conferencePath = "/wp-json/lfevents/v1/events"

	// conferenceCategory restricts the listing to CNCF events: the KubeCon
	// editions and the days co-located with them. Without it the listing widens
	// to the whole Linux Foundation slate, which the bot does not announce.
	// Sending it empty returns 500 with a page of markup, so it is never
	// interpolated from something that could be blank.
	conferenceCategory = "kubecon-cloudnativecon-cncf-events"

	// conferenceTBA is the placeholder this calendar publishes instead of a date
	// for an event whose dates are not settled.
	conferenceTBA = "TBA"
)

// floatingDateLayouts are the layouts seen in the two Linux Foundation
// calendars, newest first. Every current record uses the first; the others
// exist in the historical rows and are accepted so that a re-normalization
// upstream does not silence the feed.
var floatingDateLayouts = []string{"2006/01/02", "2006-01-02", "01/02/2006"}

// A conferenceSource reads the Linux Foundation conference calendar.
//
// Its dates are floating calendar dates: no clock time, no zone, and sometimes
// no date at all. Nothing downstream may present them as instants.
type conferenceSource struct {
	fetcher
	baseURL string
}

// newConferenceSource returns a source reading baseURL through client.
func newConferenceSource(client *http.Client, userAgent, baseURL string) conferenceSource {
	if baseURL == "" {
		baseURL = conferenceBaseURL
	}
	return conferenceSource{
		fetcher: fetcher{http: client, userAgent: userAgent, source: SourceConference},
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

// A conferenceEvent is one record as the calendar publishes it. The response is
// a flat array with no paging.
type conferenceEvent struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	DateStart string `json:"date_start"`
	DateEnd   string `json:"date_end"`
	URL       string `json:"url"`
	Location  struct {
		City    string `json:"city"`
		Region  string `json:"region"`
		Country string `json:"country"`
		Virtual bool   `json:"virtual"`
	} `json:"location"`
}

// fetch reads the calendar once and returns the events it could map, together
// with the number of records dropped as malformed.
func (s conferenceSource) fetch(ctx context.Context, _ time.Time) ([]Event, int, error) {
	var records []conferenceEvent
	if err := s.get(ctx, s.listURL(), &records); err != nil {
		return nil, 0, err
	}

	var (
		collected []Event
		skipped   int
	)
	for _, record := range records {
		event, ok := record.event()
		if !ok {
			skipped++
			continue
		}
		collected = append(collected, event)
	}
	return collected, skipped, nil
}

// listURL builds the request. The status filter is left at its default, which
// is upcoming and already excludes everything in the past.
func (s conferenceSource) listURL() string {
	query := url.Values{}
	query.Set("category", conferenceCategory)
	return s.baseURL + conferencePath + "?" + query.Encode()
}

// event maps a record onto the normalized shape. A record whose date is the
// calendar's placeholder is kept and marked rather than dropped or given an
// invented date; a date that parses in none of the known layouts drops that
// record alone.
func (r conferenceEvent) event() (Event, bool) {
	if r.ID == 0 || strings.TrimSpace(r.Name) == "" || r.URL == "" {
		return Event{}, false
	}

	event := Event{
		Source:    SourceConference,
		ID:        strconv.Itoa(r.ID),
		Title:     r.Name,
		Kind:      conferenceKind(r.Location.Virtual),
		Place:     conferencePlace(r.Location.City, r.Location.Country),
		URL:       r.URL,
		published: true,
	}

	if isTBA(r.DateStart) {
		event.DateTBA = true
		return event, true
	}

	start, ok := parseFloatingDate(r.DateStart)
	if !ok {
		return Event{}, false
	}
	event.Start = start
	event.LocalDay = start

	if end, ok := parseFloatingDate(r.DateEnd); ok {
		event.End = end
	}
	return event, true
}

// isTBA reports whether a date field holds the calendar's placeholder.
func isTBA(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), conferenceTBA)
}

// parseFloatingDate reads a floating calendar date, in any of the layouts the
// two Linux Foundation calendars have used. The result is midnight UTC of that
// day and carries no clock time: neither calendar publishes one.
func parseFloatingDate(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	for _, layout := range floatingDateLayouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// conferenceKind maps the calendar's explicit virtual flag.
func conferenceKind(virtual bool) Kind {
	if virtual {
		return KindVirtual
	}
	return KindInPerson
}

// conferencePlace renders the location. Country is a full English name rather
// than a code, and is used as published.
func conferencePlace(city, country string) string {
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	default:
		return country
	}
}
