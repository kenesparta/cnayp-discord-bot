package events

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// degradedFloorPercent is the share of a calendar's last successful event count
// below which its answer is treated as suspect rather than as news. It catches
// the half-broken filter that an emptiness check misses: a partial index still
// returns events, just far too few.
const degradedFloorPercent = 30

// A Status is how one calendar fared in one run.
type Status string

// The outcomes of reading a calendar.
const (
	// StatusOK means the calendar answered and its answer was used.
	StatusOK Status = "ok"

	// StatusFailed means the calendar could not be read, or answered with
	// nothing at all. Its events do not take part in the run.
	StatusFailed Status = "failed"

	// StatusDegraded means the calendar answered with implausibly few events.
	// Its events do not take part in the run either.
	StatusDegraded Status = "degraded"
)

// Health reports how one calendar fared, so that a caller can log the run and
// decide whether announcing anything at all is honest.
type Health struct {
	Source  Source
	Status  Status
	Records int
	Skipped int
	Err     error
}

// OK reports whether this calendar contributed to the run.
func (h Health) OK() bool { return h.Status == StatusOK }

// A calendar is one source of events. Both implementations satisfy it through
// the fetcher they embed.
type calendar interface {
	fetch(ctx context.Context, now time.Time) ([]Event, int, error)
	name() Source
}

// name reports which calendar a fetcher belongs to.
func (f fetcher) name() Source { return f.source }

// A Collector reads both calendars and returns one normalized set.
//
// It holds the only state this package keeps: the event count each calendar
// last answered with, which is what the degraded guard compares against. That
// state lives for as long as the process and is never written anywhere.
//
// A Collector is driven by a single runner: [Collector.Collect] is not safe for
// concurrent use.
type Collector struct {
	community  calendar
	conference calendar
	logger     *slog.Logger
	lastGood   map[Source]int
}

// settings are the defaults [New] applies, and what an [Option] overrides.
type settings struct {
	httpClient     *http.Client
	userAgent      string
	communityBase  string
	conferenceBase string
}

// An Option overrides a [Collector] default.
type Option func(*settings)

// WithHTTPClient replaces the HTTP client, for tests or a custom transport.
func WithHTTPClient(client *http.Client) Option {
	return func(s *settings) { s.httpClient = client }
}

// WithUserAgent sets the agent string sent to both calendars. It must identify
// the bot: one of the two hosts rejects the runtime's default outright.
func WithUserAgent(userAgent string) Option {
	return func(s *settings) { s.userAgent = userAgent }
}

// WithCommunityBaseURL points the community calendar at another host, without a
// trailing slash.
func WithCommunityBaseURL(baseURL string) Option {
	return func(s *settings) { s.communityBase = baseURL }
}

// WithConferenceBaseURL points the conference calendar at another host, without
// a trailing slash.
func WithConferenceBaseURL(baseURL string) Option {
	return func(s *settings) { s.conferenceBase = baseURL }
}

// New returns a collector reading both calendars. It does not touch the
// network; [Collector.Collect] does.
func New(logger *slog.Logger, opts ...Option) *Collector {
	applied := settings{
		httpClient: &http.Client{Timeout: requestTimeout},
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(&applied)
	}

	return &Collector{
		community:  newCommunitySource(applied.httpClient, applied.userAgent, applied.communityBase),
		conference: newConferenceSource(applied.httpClient, applied.userAgent, applied.conferenceBase),
		logger:     logger,
		lastGood:   make(map[Source]int, 2),
	}
}

// Collect reads both calendars as of now and returns the events to announce
// together with the health of each calendar.
//
// It does not return an error. One calendar failing is not a failure of the
// run: the other still contributes, and the caller decides what to do with a
// set that is empty because everything failed. What must never happen is a
// failure being mistaken for good news, which is what the guards in read are
// for.
func (c *Collector) Collect(ctx context.Context, now time.Time) (Set, []Health) {
	communityEvents, communityHealth := c.read(ctx, c.community, now)
	conferenceEvents, conferenceHealth := c.read(ctx, c.conference, now)

	raw := make([]Event, 0, len(communityEvents)+len(conferenceEvents))
	raw = append(raw, communityEvents...)
	raw = append(raw, conferenceEvents...)

	set, unknownZones := normalize(raw, now)
	for _, zone := range unknownZones {
		c.logger.Warn("unknown event time zone, bucketing by UTC instead", "zone", zone)
	}

	return set, []Health{communityHealth, conferenceHealth}
}

// read fetches one calendar and applies the two guards that separate a quiet
// day from a broken one.
//
// The emptiness guard is the important one. Both calendars ignore a query
// parameter they do not recognise instead of rejecting it, so a renamed filter
// answers 200 with an empty list, decodes cleanly, and would otherwise be
// announced as "nothing coming up" every day for a year. Neither calendar
// legitimately empties, so nothing is the one answer that cannot be true.
func (c *Collector) read(ctx context.Context, cal calendar, now time.Time) ([]Event, Health) {
	source := cal.name()

	raw, skipped, err := cal.fetch(ctx, now)
	if err != nil {
		c.logger.Error("calendar unavailable", "calendar", source, "error", err)
		return nil, Health{Source: source, Status: StatusFailed, Skipped: skipped, Err: err}
	}

	count := len(raw)
	if count == 0 {
		err := &SourceError{Source: source, Code: CodeEmpty}
		c.logger.Error("calendar returned no events at all", "calendar", source, "error", err)
		return nil, Health{Source: source, Status: StatusFailed, Skipped: skipped, Err: err}
	}

	if floor := c.lastGood[source]; floor > 0 && count*100 < floor*degradedFloorPercent {
		err := &SourceError{Source: source, Code: CodeDegraded, Count: count}
		c.logger.Error("calendar returned implausibly few events",
			"calendar", source, "count", count, "previous", floor, "error", err)
		return nil, Health{Source: source, Status: StatusDegraded, Records: count, Skipped: skipped, Err: err}
	}

	c.lastGood[source] = count
	return raw, Health{Source: source, Status: StatusOK, Records: count, Skipped: skipped}
}
