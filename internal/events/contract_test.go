//go:build contract

// These tests talk to the live calendars, so they are kept out of the normal
// test command by a build tag and run on a schedule instead:
//
//	go test -tags contract -run TestContract ./internal/events/
//
// They do not test this package. They test the assumptions this package is
// built on, and a failure here means an upstream moved, not that the bot broke.
// That distinction is the whole point of separating them: an outage must never
// fail a build.
package events

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestContractCommunityCalendarAnswersWithEvents(t *testing.T) {
	source := newCommunitySource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")

	collected, skipped, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	// The window held 98 events when this was written. A floor of 30 catches a
	// broken filter without failing over a quiet season.
	if len(collected) < 30 {
		t.Errorf("collected %d events, want at least 30: the filter or the shape has changed", len(collected))
	}
	if skipped > len(collected)/10 {
		t.Errorf("skipped %d of %d records: the record shape has changed", skipped, skipped+len(collected))
	}

	for _, event := range collected {
		if event.ID == "" || event.Title == "" || event.Start.IsZero() || event.URL == "" {
			t.Fatalf("record %+v is missing a mandatory field", event)
		}
	}
}

func TestContractCommunityIgnoresAnUnknownSlug(t *testing.T) {
	// This is the assumption the emptiness guard rests on: the server does not
	// reject a community it has never heard of, it answers 200 with nothing. If
	// this ever starts returning an error, the guard could be relaxed.
	source := newCommunitySource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")
	endpoint := strings.Replace(source.searchURL("2026-01-01", "2027-01-01", KindInPerson),
		"community%5B0%5D=cncf", "community%5B0%5D=cncf-does-not-exist", 1)

	var body communityPage
	if err := source.get(t.Context(), endpoint, &body); err != nil {
		t.Fatalf("get() = %v, want a successful response carrying nothing", err)
	}
	if body.Total != 0 {
		t.Errorf("total = %d for an unknown community, want 0", body.Total)
	}
}

func TestContractCommunityKindsPartitionTheListing(t *testing.T) {
	// The projection does not say how an event is attended, so the source asks
	// once per kind (cncf/open-community-groups#730). That is only complete if
	// the kinds add up to the whole listing; a new kind would be missed.
	source := newCommunitySource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")
	from, to := communityQueryWindow(time.Now())

	total := func(endpoint string) int {
		t.Helper()
		var body communityPage
		if err := source.get(t.Context(), endpoint, &body); err != nil {
			t.Fatalf("get() = %v", err)
		}
		return body.Total
	}

	sum := 0
	for _, kind := range communityKinds {
		sum += total(source.searchURL(from, to, kind))
	}
	all := strings.Replace(source.searchURL(from, to, KindInPerson), "kind%5B0%5D=in-person&", "", 1)
	if whole := total(all); sum != whole {
		t.Errorf("the kinds add up to %d events, the whole listing is %d: a kind is missing", sum, whole)
	}
}

func TestContractCommunityRejectsAnUnknownKind(t *testing.T) {
	// What makes a renamed kind fail loudly rather than drop its events.
	source := newCommunitySource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")
	endpoint := strings.Replace(source.searchURL("2026-01-01", "2027-01-01", KindInPerson),
		"kind%5B0%5D=in-person", "kind%5B0%5D=teleported", 1)

	var body communityPage
	err := source.get(t.Context(), endpoint, &body)

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Status != http.StatusUnprocessableEntity {
		t.Errorf("get() with an unknown kind = %v, want a 422", err)
	}
}

func TestContractConferenceCalendarAnswersWithEvents(t *testing.T) {
	source := newConferenceSource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")

	collected, _, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	// 24 events fell in the window when this was written.
	if len(collected) < 5 {
		t.Errorf("collected %d events, want at least 5: the category slug may have changed", len(collected))
	}
}

func TestContractConferenceRejectsTheDefaultUserAgent(t *testing.T) {
	// The reason every request sets an agent string. If this ever stops being a
	// 403, the workaround is no longer load bearing and this test says so.
	source := newConferenceSource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, source.listURL(), nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d with the runtime's default agent, want 403", resp.StatusCode)
	}
}

func TestContractKCDCalendarAnswersWithCommunityDays(t *testing.T) {
	source := newKCDSource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")

	collected, skipped, err := source.fetch(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("fetch() = %v, want nil", err)
	}

	// 61 records all time, 13 of them still to come, when this was written. The
	// listing includes the past, so the floor is against the whole corpus.
	if len(collected) < 30 {
		t.Errorf("collected %d events, want at least 30: the host taxonomy may have changed", len(collected))
	}
	if skipped > len(collected)/10 {
		t.Errorf("skipped %d records: the meta shape has changed", skipped)
	}

	upcoming := 0
	for _, event := range collected {
		if event.Start.After(time.Now()) {
			upcoming++
		}
	}
	if upcoming == 0 {
		t.Error("no upcoming community days at all, which has not been true since the listing existed")
	}
}

func TestContractKCDTaxonomyStillMeansCommunityHosted(t *testing.T) {
	// The whole source rests on one magic number. If the term is renamed or
	// renumbered, the listing silently becomes something else.
	source := newKCDSource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")

	var term struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	endpoint := kcdBaseURL + "/wp-json/wp/v2/lf-event-host/" + kcdHostTerm + "?_fields=id,name,slug"
	if err := source.get(t.Context(), endpoint, &term); err != nil {
		t.Fatalf("get() = %v, want nil", err)
	}

	if term.Slug != "community-hosted" {
		t.Errorf("term %s is now %q (%s), not the community-hosted listing", kcdHostTerm, term.Name, term.Slug)
	}
}
