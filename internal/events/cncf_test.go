package events

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// kcdServer serves the given listing bodies in request order and records the
// query of every request it saw.
func kcdServer(t *testing.T, pages ...string) (kcdSource, *[]string) {
	t.Helper()

	var queries []string
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		body := pages[len(pages)-1]
		if served < len(pages) {
			body = pages[served]
		}
		served++
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return newKCDSource(srv.Client(), defaultUserAgent, srv.URL), &queries
}

// kcdRecord renders one record in the shape the CNCF website publishes.
func kcdRecord(id int, title, start, end, city string) string {
	return fmt.Sprintf(`{
		"id": %d,
		"link": "https://www.cncf.io/events/kcd-%d/",
		"title": {"rendered": %q},
		"meta": {
			"lf_event_date_start": %q,
			"lf_event_date_end": %q,
			"lf_event_external_url": "https://community2.cncf.io/events/details/whatever/",
			"lf_event_city": %q,
			"lf_event_logo": 0,
			"footnotes": ""
		}
	}`, id, id, title, start, end, city)
}

func TestCNCFAsksOnlyForCommunityHostedEvents(t *testing.T) {
	// The listing carries 310 records, of which the community-hosted taxonomy is
	// the only slice worth having and the only filter this endpoint honours: its
	// date parameters are accepted and silently ignored.
	source, queries := kcdServer(t, `[]`)

	if _, _, err := source.fetch(t.Context(), time.Now()); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	query := (*queries)[0]
	for _, want := range []string{"lf-event-host=351", "per_page=100", "page=1", "_fields="} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

func TestCNCFUnescapesTitles(t *testing.T) {
	// Titles arrive as HTML, so an en dash reaches us as an entity.
	body := `[` + kcdRecord(173546, "KCD UK &#8211; Edinburgh 2026", "2026/10/19", "2026/10/20", "Edinburgh") + `]`
	source, _ := kcdServer(t, body)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	if want := "KCD UK – Edinburgh 2026"; collected[0].Title != want {
		t.Errorf("Title = %q, want %q", collected[0].Title, want)
	}
}

func TestCNCFMapsARecordCompletely(t *testing.T) {
	body := `[` + kcdRecord(173540, "KCD S&#227;o Paulo 2026", "2026/09/26", "2026/09/26", "São Paulo") + `]`
	source, _ := kcdServer(t, body)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	event := collected[0]

	if event.Source != SourceKCD || event.ID != "173540" {
		t.Errorf("identity = %q, want kcd/173540", event.Identity())
	}
	if event.Exact {
		t.Error("Exact = true, want false: this calendar publishes bare dates")
	}
	if want := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC); !event.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", event.Start, want)
	}
	if !event.LocalDay.Equal(event.Start) {
		t.Errorf("LocalDay = %v, want the start date", event.LocalDay)
	}
	if event.Place != "São Paulo" {
		t.Errorf("Place = %q, want São Paulo", event.Place)
	}
	if want := "https://www.cncf.io/events/kcd-173540/"; event.URL != want {
		t.Errorf("URL = %q, want %q", event.URL, want)
	}
	if !event.Conference() {
		t.Error("Conference() = false; a community day belongs on the horizon")
	}
}

func TestCNCFSkipsRecordsItCannotUse(t *testing.T) {
	tests := []struct {
		name   string
		record string
	}{
		{"no identifier", `{"id":0,"link":"https://x/","title":{"rendered":"KCD X"},"meta":{"lf_event_date_start":"2026/09/26"}}`},
		{"no title", `{"id":1,"link":"https://x/","title":{"rendered":""},"meta":{"lf_event_date_start":"2026/09/26"}}`},
		{"no link", `{"id":2,"link":"","title":{"rendered":"KCD X"},"meta":{"lf_event_date_start":"2026/09/26"}}`},
		{"no date", `{"id":3,"link":"https://x/","title":{"rendered":"KCD X"},"meta":{"lf_event_date_start":""}}`},
		{"unusable date", `{"id":4,"link":"https://x/","title":{"rendered":"KCD X"},"meta":{"lf_event_date_start":"soon"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			good := kcdRecord(9, "KCD Panama 2026", "2027/01/22", "2027/01/22", "Panamá")
			source, _ := kcdServer(t, `[`+tt.record+`,`+good+`]`)

			collected, skipped, err := source.fetch(t.Context(), time.Now())
			if err != nil {
				t.Fatalf("fetch() = %v, want nil", err)
			}
			if len(collected) != 1 {
				t.Errorf("collected %d events, want 1", len(collected))
			}
			if skipped != 1 {
				t.Errorf("skipped = %d, want 1", skipped)
			}
		})
	}
}

func TestCNCFStopsAtAShortPage(t *testing.T) {
	source, queries := kcdServer(t, `[`+kcdRecord(1, "KCD One", "2026/09/26", "2026/09/26", "Lima")+`]`)

	if _, _, err := source.fetch(t.Context(), time.Now()); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(*queries) != 1 {
		t.Errorf("made %d requests for a single short page, want 1", len(*queries))
	}
}
