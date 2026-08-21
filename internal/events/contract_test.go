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
	endpoint := strings.Replace(source.searchURL("2026-01-01", "2027-01-01", 0),
		"community%5B0%5D=cncf", "community%5B0%5D=cncf-does-not-exist", 1)

	var body communityPage
	if err := source.get(t.Context(), endpoint, &body); err != nil {
		t.Fatalf("get() = %v, want a successful response carrying nothing", err)
	}
	if body.Total != 0 {
		t.Errorf("total = %d for an unknown community, want 0", body.Total)
	}
}

func TestContractCommunityCapsThePageSize(t *testing.T) {
	source := newCommunitySource(&http.Client{Timeout: requestTimeout}, defaultUserAgent, "")
	endpoint := strings.Replace(source.searchURL("2026-01-01", "2027-01-01", 0), "limit=100", "limit=101", 1)

	var body communityPage
	err := source.get(t.Context(), endpoint, &body)

	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Status != http.StatusUnprocessableEntity {
		t.Errorf("get() with limit=101 = %v, want a 422: the page cap has changed", err)
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
