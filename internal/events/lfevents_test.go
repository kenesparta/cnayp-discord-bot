package events

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// conferenceServer serves one listing body and records the query it was asked for.
func conferenceServer(t *testing.T, body string) (conferenceSource, *string) {
	t.Helper()

	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return newConferenceSource(srv.Client(), defaultUserAgent, srv.URL), &query
}

func TestLFEventsRestrictsToTheCNCFCategory(t *testing.T) {
	// Without the category the listing widens to the whole Linux Foundation
	// slate, which this bot does not announce.
	source, query := conferenceServer(t, `[]`)

	if _, _, err := source.fetch(t.Context(), time.Now()); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if !strings.Contains(*query, "category=kubecon-cloudnativecon-cncf-events") {
		t.Errorf("query %q does not restrict the category", *query)
	}
}

func TestLFEventsParsesEveryKnownDateLayout(t *testing.T) {
	tests := map[string]string{
		"current":  "2026/11/09",
		"iso":      "2026-11-09",
		"american": "11/09/2026",
	}
	want := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			parsed, ok := parseConferenceDate(raw)
			if !ok {
				t.Fatalf("parseConferenceDate(%q) failed", raw)
			}
			if !parsed.Equal(want) {
				t.Errorf("parseConferenceDate(%q) = %v, want %v", raw, parsed, want)
			}
		})
	}
}

func TestLFEventsKeepsPlaceholderDates(t *testing.T) {
	// The calendar publishes "TBA" for an event whose dates are not settled. It
	// is kept and marked: dropping it hides a real conference, and inventing a
	// date states something the calendar never said.
	body := `[{
		"id": 319493,
		"name": "Open Source Forum",
		"date_start": "TBA",
		"date_end": "TBA",
		"url": "https://events.linuxfoundation.org/open-source-forum/",
		"location": {"city": "", "region": "", "country": "", "virtual": false}
	}]`
	source, _ := conferenceServer(t, body)

	collected, skipped, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(collected) != 1 {
		t.Fatalf("collected %d events, want 1", len(collected))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if !collected[0].DateTBA {
		t.Error("DateTBA = false, want true")
	}
	if !collected[0].Start.IsZero() {
		t.Errorf("Start = %v, want the zero time: no date was published", collected[0].Start)
	}
}

func TestLFEventsSkipsUnparsableDates(t *testing.T) {
	body := `[
		{"id": 1, "name": "Broken", "date_start": "not a date", "date_end": "",
		 "url": "https://events.linuxfoundation.org/broken/",
		 "location": {"city": "", "region": "", "country": "", "virtual": false}},
		{"id": 2, "name": "KubeCon + CloudNativeCon North America", "date_start": "2026/11/09",
		 "date_end": "2026/11/12", "url": "https://events.linuxfoundation.org/kubecon-na/",
		 "location": {"city": "Salt Lake City", "region": "UT", "country": "United States", "virtual": false}}
	]`
	source, _ := conferenceServer(t, body)

	collected, skipped, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(collected) != 1 || collected[0].ID != "2" {
		t.Fatalf("collected %d events, want only the well formed one", len(collected))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestLFEventsCarriesPlaceAndAttendanceKind(t *testing.T) {
	body := `[
		{"id": 1, "name": "KubeCon + CloudNativeCon North America", "date_start": "2026/11/09",
		 "date_end": "2026/11/12", "url": "https://events.linuxfoundation.org/kubecon-na/",
		 "location": {"city": "Salt Lake City", "region": "UT", "country": "United States", "virtual": false}},
		{"id": 2, "name": "Cloud Native Live", "date_start": "2026/09/23", "date_end": "2026/09/23",
		 "url": "https://events.linuxfoundation.org/cloud-native-live/",
		 "location": {"city": "", "region": "", "country": "", "virtual": true}}
	]`
	source, _ := conferenceServer(t, body)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	inPerson, virtual := collected[0], collected[1]
	if inPerson.Place != "Salt Lake City, United States" {
		t.Errorf("Place = %q, want %q", inPerson.Place, "Salt Lake City, United States")
	}
	if inPerson.Kind != KindInPerson {
		t.Errorf("Kind = %q, want %q", inPerson.Kind, KindInPerson)
	}
	if virtual.Kind != KindVirtual {
		t.Errorf("Kind = %q, want %q", virtual.Kind, KindVirtual)
	}
	if inPerson.Exact {
		t.Error("Exact = true, want false: this calendar publishes bare dates")
	}
	if want := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC); !inPerson.End.Equal(want) {
		t.Errorf("End = %v, want %v", inPerson.End, want)
	}
}

func TestLFEventsSkipsRecordsWithoutIdentityOrLink(t *testing.T) {
	body := `[
		{"id": 0, "name": "No id", "date_start": "2026/11/09", "url": "https://x/", "location": {}},
		{"id": 3, "name": "", "date_start": "2026/11/09", "url": "https://x/", "location": {}},
		{"id": 4, "name": "No link", "date_start": "2026/11/09", "url": "", "location": {}}
	]`
	source, _ := conferenceServer(t, body)

	collected, skipped, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(collected) != 0 {
		t.Errorf("collected %d events, want 0", len(collected))
	}
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
}
