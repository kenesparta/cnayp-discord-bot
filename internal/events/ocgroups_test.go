package events

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// kindServer answers each kind's request with its body, or with an empty
// listing, and records the query of every request it saw.
func kindServer(t *testing.T, bodies map[Kind]string) (communitySource, *[]string) {
	t.Helper()

	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		body, ok := bodies[Kind(r.URL.Query().Get("kind[0]"))]
		if !ok {
			body = `{"total":0,"truncated":false,"events":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return newCommunitySource(srv.Client(), defaultUserAgent, srv.URL), &queries
}

// communityRecord renders one record of the calendar's minimal projection.
func communityRecord(id, slug string, startsAt int64) string {
	return fmt.Sprintf(`{
		"event_id": %q,
		"name": "Cloud Native Meetup",
		"starts_at": %d,
		"ends_at": %d,
		"group_slug": "ncz2zfb",
		"group_slug_pretty": "cloud-native-ayacucho",
		"community_name": "cncf",
		"slug": %q,
		"latitude": -13.16,
		"longitude": -74.22
	}`, id, startsAt, startsAt+3600, slug)
}

// listing wraps records as one kind's answer.
func listing(records ...string) string {
	return fmt.Sprintf(`{"total":%d,"truncated":false,"events":[%s]}`, len(records), strings.Join(records, ","))
}

func TestOCGroupsAsksOncePerKind(t *testing.T) {
	source, queries := kindServer(t, nil)

	now := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	if _, _, err := source.fetch(t.Context(), now); err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	if len(*queries) != len(communityKinds) {
		t.Fatalf("made %d requests, want one per kind", len(*queries))
	}
	for i, query := range *queries {
		for _, want := range []string{
			"community%5B0%5D=cncf",
			// One day wider on each side than the window that is displayed,
			// because the server filters on the UTC day and events belong to
			// their own.
			"date_from=2026-08-19",
			"date_to=2027-02-21",
			// Without it the route answers 422 (cncf/open-community-groups#730).
			"view_mode=calendar",
			"kind%5B0%5D=" + string(communityKinds[i]),
		} {
			if !strings.Contains(query, want) {
				t.Errorf("query %q is missing %q", query, want)
			}
		}
	}
}

func TestOCGroupsTakesTheKindFromTheQuery(t *testing.T) {
	// The projection does not say how an event is attended; the query it
	// answered does.
	source, _ := kindServer(t, map[Kind]string{
		KindVirtual: listing(communityRecord("v", "sv", 1787184300)),
		KindHybrid:  listing(communityRecord("h", "sh", 1787184300)),
	})

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	kinds := map[string]Kind{}
	for _, event := range collected {
		kinds[event.ID] = event.Kind
	}
	if kinds["v"] != KindVirtual || kinds["h"] != KindHybrid {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestOCGroupsKeepsAnEventListedUnderTwoKindsOnce(t *testing.T) {
	record := communityRecord("abc", "sa", 1787184300)
	source, _ := kindServer(t, map[Kind]string{KindInPerson: listing(record), KindVirtual: listing(record)})

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}
	if len(collected) != 1 || collected[0].Kind != KindInPerson {
		t.Errorf("collected = %+v, want one event, kept under its first kind", collected)
	}
}

func TestOCGroupsRefusesATruncatedListing(t *testing.T) {
	// Past the 1,000-event cap the far end of the window is missing.
	source, _ := kindServer(t, map[Kind]string{
		KindInPerson: `{"total":1500,"truncated":true,"events":[` + communityRecord("abc", "sa", 1787184300) + `]}`,
	})

	_, _, err := source.fetch(t.Context(), time.Now())

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != CodeUnusable {
		t.Errorf("fetch() = %v, want %s", err, CodeUnusable)
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
			source, _ := kindServer(t, map[Kind]string{KindInPerson: listing(tt.record, good)})

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
	source, _ := kindServer(t, map[Kind]string{KindInPerson: listing(communityRecord("abc", "n6guw8d", 1787184300))})

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
	// The projection carries neither a zone nor a venue (#730); they stay empty
	// rather than guessed.
	if event.Zone != "" || event.Place != "" {
		t.Errorf("Zone = %q, Place = %q, want both empty", event.Zone, event.Place)
	}
	if event.Kind != KindInPerson {
		t.Errorf("Kind = %q, want %q", event.Kind, KindInPerson)
	}
	if event.Group != "Cloud Native Ayacucho" || event.groupKey != "ncz2zfb" {
		t.Errorf("Group = %q, groupKey = %q", event.Group, event.groupKey)
	}
	// Upstream lists only public, uncanceled, non-test events.
	if !event.published || event.canceled || event.test {
		t.Errorf("status flags = published %v, canceled %v, test %v", event.published, event.canceled, event.test)
	}
}

func TestOCGroupsBuildsTheCanonicalURL(t *testing.T) {
	// The payload carries no URL: it is composed from the community, group and
	// event slugs. In production this reads
	// https://ocgroups.dev/cncf/group/ncz2zfb/event/n6guw8d.
	source, _ := kindServer(t, map[Kind]string{KindInPerson: listing(communityRecord("abc", "n6guw8d", 1787184300))})

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	want := source.baseURL + "/cncf/group/ncz2zfb/event/n6guw8d"
	if collected[0].URL != want {
		t.Errorf("URL = %q, want %q", collected[0].URL, want)
	}
}

func TestGroupName(t *testing.T) {
	for pretty, want := range map[string]string{
		"cloud-native-ayacucho": "Cloud Native Ayacucho",
		"santiago":              "Santiago",
		"":                      "",
		"--":                    "",
	} {
		if got := groupName(pretty); got != want {
			t.Errorf("groupName(%q) = %q, want %q", pretty, got, want)
		}
	}
}

func TestCollapseKeysOnTheGroupsIDNotItsName(t *testing.T) {
	// Most community records carry no readable group name. Keyed on the name,
	// two groups' "Monthly Meetup" would collapse into one, and one group's
	// meetup would never be announced.
	at := func(group string, days int) Event {
		start := runClock.AddDate(0, 0, days)
		return Event{Source: SourceCommunity, ID: group + "-" + string(rune('a'+days)), Title: "Monthly Meetup",
			Start: start, Exact: true, groupKey: group, published: true}
	}
	set := collapse([]Event{at("g1", 1), at("g2", 2), at("g1", 30)})

	if len(set) != 2 {
		t.Errorf("collapsed to %d events, want one per group", len(set))
	}
}
