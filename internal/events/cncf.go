package events

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// kcdBaseURL is the CNCF website, which publishes the community-run
	// conferences that neither other calendar carries.
	kcdBaseURL = "https://www.cncf.io"

	// kcdPath is its WordPress event listing.
	kcdPath = "/wp-json/wp/v2/lf_event"

	// kcdHostTerm selects the "Community-hosted" host taxonomy, which in practice
	// is the Kubernetes Community Days. It narrows the listing from 310 records
	// to 61 server-side, and it is the one filter on this endpoint that actually
	// filters: the date parameters are accepted and silently ignored.
	//
	// A term id the site does not know answers 200 with an empty list rather than
	// an error, which is why an empty result is a failure everywhere here.
	kcdHostTerm = "351"

	// kcdPageSize is the maximum this endpoint accepts; more returns 400.
	kcdPageSize = 100

	// kcdMaxPages bounds the paging loop. The whole listing is 61 records today,
	// so this is headroom rather than an expectation.
	kcdMaxPages = 3
)

// A kcdSource reads the Kubernetes Community Days from the CNCF website.
//
// It exists because the other two calendars miss them entirely: the Linux
// Foundation listing has none, and the community group calendar carries a KCD
// only when its organizers happen to also run a community group. They are also
// the events closest to a regional community — São Paulo, Panamá, Sofia — which
// is what makes the gap worth a third client.
//
// Like the conference calendar it publishes floating calendar dates: no clock
// time and no zone.
type kcdSource struct {
	fetcher
	baseURL string
}

// newKCDSource returns a source reading baseURL through client.
func newKCDSource(client *http.Client, userAgent, baseURL string) kcdSource {
	if baseURL == "" {
		baseURL = kcdBaseURL
	}
	return kcdSource{
		fetcher: fetcher{http: client, userAgent: userAgent, source: SourceKCD},
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

// A kcdEvent is one record as the CNCF website publishes it. The interesting
// fields live under meta, which is a WordPress plugin's internal storage rather
// than a published contract, so every one of them is optional here.
type kcdEvent struct {
	ID    int    `json:"id"`
	Link  string `json:"link"`
	Title struct {
		// Rendered is HTML: "KCD UK &#8211; Edinburgh 2026".
		Rendered string `json:"rendered"`
	} `json:"title"`
	Meta struct {
		DateStart string `json:"lf_event_date_start"`
		DateEnd   string `json:"lf_event_date_end"`
		City      string `json:"lf_event_city"`
	} `json:"meta"`
}

// fetch reads the listing and returns the events it could map, together with
// the number of records dropped as malformed.
//
// The listing carries every community-hosted event ever held, with no way to
// ask for the upcoming ones: this endpoint accepts date parameters and ignores
// them silently. The window is therefore applied afterwards, in normalization,
// like it is for every other calendar.
func (s kcdSource) fetch(ctx context.Context, _ time.Time) ([]Event, int, error) {
	var (
		collected []Event
		skipped   int
	)

	for page := 1; page <= kcdMaxPages; page++ {
		var records []kcdEvent
		if err := s.get(ctx, s.listURL(page), &records); err != nil {
			return nil, 0, err
		}

		for _, record := range records {
			event, ok := record.event()
			if !ok {
				skipped++
				continue
			}
			collected = append(collected, event)
		}

		if len(records) < kcdPageSize {
			break
		}
	}

	return collected, skipped, nil
}

// listURL builds one page request, asking only for the fields that are used.
func (s kcdSource) listURL(page int) string {
	query := url.Values{}
	query.Set("lf-event-host", kcdHostTerm)
	query.Set("per_page", strconv.Itoa(kcdPageSize))
	query.Set("page", strconv.Itoa(page))
	query.Set("_fields", "id,link,title,meta")

	return s.baseURL + kcdPath + "?" + query.Encode()
}

// event maps a record onto the normalized shape.
func (r kcdEvent) event() (Event, bool) {
	// The title arrives as HTML, so an en dash reaches us as "&#8211;".
	title := strings.TrimSpace(html.UnescapeString(r.Title.Rendered))
	if r.ID == 0 || title == "" || r.Link == "" {
		return Event{}, false
	}

	start, ok := parseFloatingDate(r.Meta.DateStart)
	if !ok {
		return Event{}, false
	}

	event := Event{
		Source:    SourceKCD,
		ID:        strconv.Itoa(r.ID),
		Title:     title,
		Start:     start,
		LocalDay:  start,
		Kind:      KindInPerson,
		Place:     strings.TrimSpace(r.Meta.City),
		URL:       r.Link,
		published: true,
	}
	if end, ok := parseFloatingDate(r.Meta.DateEnd); ok {
		event.End = end
	}
	return event, true
}
