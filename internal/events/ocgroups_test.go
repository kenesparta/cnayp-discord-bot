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

// communityServer serves the given page bodies in request order and records the
// query of every request it saw.
func communityServer(t *testing.T, pages ...string) (communitySource, *[]string) {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return newCommunitySource(srv.Client(), defaultUserAgent, srv.URL), &queries
}

// communityRecord renders one plausible record, including the markup blob the
// real calendar attaches to every event.
func communityRecord(id, slug string, startsAt int64) string {
	return fmt.Sprintf(`{
		"event_id": %q,
		"name": "Cloud Native Meetup",
		"starts_at": %d,
		"ends_at": %d,
		"timezone": "America/Lima",
		"kind": "in-person",
		"canceled": false,
		"published": true,
		"test_event": false,
		"group_name": "Cloud Native Ayacucho",
		"group_slug": "ncz2zfb",
		"community_name": "cncf",
		"slug": %q,
		"venue_city": "Ayacucho",
		"popover_html": "<div class=\"popover\"><a href=\"/x\">Cloud Native Meetup</a></div>"
	}`, id, startsAt, startsAt+3600, slug)
}

func TestOCGroupsBuildsTheWindowedQuery(t *testing.T) {
	source, queries := communityServer(t, `{"total":0,"events":[]}`)

	now := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	if _, _, err := source.fetch(t.Context(), now); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	query := (*queries)[0]
	for _, want := range []string{
		"community%5B0%5D=cncf",
		// One day wider on each side than the window that is displayed, because
		// the server filters on the UTC day and events belong to their own.
		"date_from=2026-08-19",
		"date_to=2027-02-21",
		"sort_by=date",
		"sort_direction=asc",
		"limit=100",
		"offset=0",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

func TestOCGroupsPagesUntilTotal(t *testing.T) {
	first := `{"total":150,"events":[` + communityRecord("a", "sa", 1787184300) + `]}`
	second := `{"total":150,"events":[` + communityRecord("b", "sb", 1787184400) + `]}`
	source, queries := communityServer(t, first, second)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	if len(*queries) != 2 {
		t.Fatalf("made %d requests, want 2", len(*queries))
	}
	if !strings.Contains((*queries)[1], "offset=100") {
		t.Errorf("second request %q does not advance the offset", (*queries)[1])
	}
	if len(collected) != 2 {
		t.Errorf("collected %d events, want 2", len(collected))
	}
}

func TestOCGroupsStopsAtThePageCeiling(t *testing.T) {
	// A total the server keeps inflating must not spin the loop forever.
	page := `{"total":100000,"events":[` + communityRecord("a", "sa", 1787184300) + `]}`
	source, queries := communityServer(t, page)

	if _, _, err := source.fetch(t.Context(), time.Now()); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	if len(*queries) != communityMaxPages {
		t.Errorf("made %d requests, want the ceiling of %d", len(*queries), communityMaxPages)
	}
}

func TestOCGroupsDeduplicatesAcrossPages(t *testing.T) {
	// Pages are ordered by a start many events share and the server documents no
	// tiebreaker, so a record can appear on both sides of a boundary.
	repeated := communityRecord("same", "slug", 1787184300)
	first := `{"total":150,"events":[` + repeated + `]}`
	second := `{"total":150,"events":[` + repeated + `]}`
	source, _ := communityServer(t, first, second)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(collected) != 1 {
		t.Errorf("collected %d events, want 1", len(collected))
	}
}

func TestOCGroupsSkipsMalformedRecords(t *testing.T) {
	tests := []struct {
		name   string
		record string
	}{
		{"no identifier", `{"name":"X","starts_at":1787184300,"group_slug":"g","slug":"s"}`},
		{"no title", `{"event_id":"1","starts_at":1787184300,"group_slug":"g","slug":"s"}`},
		{"no start", `{"event_id":"1","name":"X","group_slug":"g","slug":"s"}`},
		{"no slugs", `{"event_id":"1","name":"X","starts_at":1787184300}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			good := communityRecord("good", "sg", 1787184300)
			body := `{"total":2,"events":[` + tt.record + `,` + good + `]}`
			source, _ := communityServer(t, body)

			collected, skipped, err := source.fetch(t.Context(), time.Now())
			if err != nil {
				t.Fatalf("fetch() = %v, want nil", err)
			}
			// One bad record must never cost the whole response.
			if len(collected) != 1 {
				t.Errorf("collected %d events, want 1", len(collected))
			}
			if skipped != 1 {
				t.Errorf("skipped = %d, want 1", skipped)
			}
		})
	}
}

func TestOCGroupsMapsARecordCompletely(t *testing.T) {
	source, _ := communityServer(t, `{"total":1,"events":[`+communityRecord("abc", "n6guw8d", 1787184300)+`]}`)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	event := collected[0]

	if event.Source != SourceCommunity || event.ID != "abc" {
		t.Errorf("identity = %q, want community/abc", event.Identity())
	}
	if !event.Exact {
		t.Error("Exact = false, want true: this calendar publishes real instants")
	}
	if want := time.Unix(1787184300, 0).UTC(); !event.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", event.Start, want)
	}
	if event.End.IsZero() {
		t.Error("End is zero, want the published end")
	}
	if event.Zone != "America/Lima" {
		t.Errorf("Zone = %q, want America/Lima", event.Zone)
	}
	if event.Kind != KindInPerson {
		t.Errorf("Kind = %q, want %q", event.Kind, KindInPerson)
	}
	if event.Place != "Ayacucho" {
		t.Errorf("Place = %q, want Ayacucho", event.Place)
	}
	if event.Group != "Cloud Native Ayacucho" {
		t.Errorf("Group = %q, want Cloud Native Ayacucho", event.Group)
	}
	if !event.published || event.canceled || event.test {
		t.Errorf("status flags = published %v, canceled %v, test %v", event.published, event.canceled, event.test)
	}
}

func TestOCGroupsBuildsTheCanonicalURL(t *testing.T) {
	// The payload carries no URL: it is composed from the community, group and
	// event slugs. In production this reads
	// https://ocgroups.dev/cncf/group/ncz2zfb/event/n6guw8d.
	source, _ := communityServer(t, `{"total":1,"events":[`+communityRecord("abc", "n6guw8d", 1787184300)+`]}`)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	want := source.baseURL + "/cncf/group/ncz2zfb/event/n6guw8d"
	if collected[0].URL != want {
		t.Errorf("URL = %q, want %q", collected[0].URL, want)
	}
}

func TestOCGroupsDiscardsTheMarkupBlob(t *testing.T) {
	// Three fifths of the response is server-rendered markup. It must never
	// reach an Event, let alone a Discord message.
	source, _ := communityServer(t, `{"total":1,"events":[`+communityRecord("abc", "sa", 1787184300)+`]}`)

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	event := collected[0]
	for field, value := range map[string]string{
		"Title": event.Title, "Place": event.Place, "Group": event.Group, "URL": event.URL,
	} {
		if strings.Contains(value, "<") {
			t.Errorf("%s = %q, which carries markup", field, value)
		}
	}
}

func TestOCGroupsMapsAttendanceKinds(t *testing.T) {
	tests := map[string]Kind{
		"in-person": KindInPerson,
		"virtual":   KindVirtual,
		"hybrid":    KindHybrid,
		"":          KindInPerson,
		"whatever":  KindInPerson,
	}

	for raw, want := range tests {
		t.Run("kind "+raw, func(t *testing.T) {
			if got := communityKind(raw); got != want {
				t.Errorf("communityKind(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}
