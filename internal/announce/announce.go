// Package announce posts upcoming cloud native events into one Discord channel,
// once each time the daily schedule invokes it.
//
// It is the layer above internal/events and internal/discord: it decides what a
// run does, what of it is new, how it reads in Spanish and English, and what to
// do when publishing fails. What it has to remember between runs, which events
// each calendar listed and how many it returned, goes through a [Store]: on
// Lambda nothing survives from one run to the next.
//
// The schedule is not here. EventBridge Scheduler invokes a run at 13:00 UTC
// (personal-infra §5.24), and [kindOf] decides from the day whether the run
// also carries the weekly digest.
package announce

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// A Collector supplies the events to announce. It is [events.Collector].
type Collector interface {
	Collect(ctx context.Context, now time.Time, floors events.Floors) (events.Set, []events.Health)
}

// A Publisher posts a message to a channel. It is [discord.Client].
type Publisher interface {
	CreateMessage(ctx context.Context, channelID string, message discord.Message) error
}

// A Record is what the announcer keeps about one calendar between runs. It holds
// event identities published by the calendar and a count, nothing about any
// person: that is the privacy policy's claim.
type Record struct {
	// Seen are the identities the calendar listed on its last healthy run.
	Seen []string
	// Floor is how many events it returned then; see [events.Floors].
	Floor int
}

// A Store keeps the records between runs.
type Store interface {
	// Load returns the record of each of sources that has one. A calendar with
	// none has never had a healthy run, and the next one seeds it silently.
	Load(ctx context.Context, sources []events.Source) (map[events.Source]Record, error)
	// Save replaces one calendar's record.
	Save(ctx context.Context, source events.Source, record Record) error
	// ClaimDigest records that the weekly digest of day is going out, and
	// reports false when it already went. It is the guard against the schedule
	// delivering one run twice, which its at-least-once delivery allows.
	ClaimDigest(ctx context.Context, day time.Time) (bool, error)
}

// An Outcome is what one run did, counted. It carries no event content.
type Outcome struct {
	Kind Kind `json:"kind"`
	// Off is set when no announcement channel is configured and the run did
	// nothing at all.
	Off       bool `json:"off,omitempty"`
	Events    int  `json:"events"`
	New       int  `json:"new"`
	Messages  int  `json:"messages"`
	Published int  `json:"published"`
}

// An Announcer publishes upcoming events into one channel. Create one with
// [New] and drive it with [Announcer.Run].
type Announcer struct {
	collector Collector
	publisher Publisher
	store     Store
	channelID string
	sources   []events.Source
	logger    *slog.Logger
	// now is the clock, replaceable so that a run can be exercised at a chosen
	// instant instead of at whatever time the test happens to run.
	now func() time.Time
}

// New returns an announcer posting to channelID. An empty channelID leaves it
// switched off: [Announcer.Run] does nothing and says so.
func New(collector Collector, publisher Publisher, store Store, channelID string, logger *slog.Logger) *Announcer {
	return &Announcer{
		collector: collector,
		publisher: publisher,
		store:     store,
		channelID: channelID,
		sources:   events.Sources(),
		logger:    logger,
		now:       time.Now,
	}
}

// Run performs one run: read the calendars, record what they listed, and
// publish what is new.
//
// The records are saved before anything is published. Run therefore returns an
// error only while nothing has gone out, when loading or saving them failed:
// then repeating the run is safe, which is what lets Lambda retry it. A
// failure while publishing is logged and not returned, because a retry would
// post again what already went out, and a duplicate is more visible to a
// reader than a gap.
func (a *Announcer) Run(ctx context.Context) (Outcome, error) {
	return a.runAt(ctx, a.now())
}

func (a *Announcer) runAt(ctx context.Context, now time.Time) (Outcome, error) {
	outcome := Outcome{Kind: kindOf(now)}
	if a.channelID == "" {
		a.logger.Info("event announcements are off", "reason", "no announcement channel configured")
		outcome.Off = true
		return outcome, nil
	}

	records, err := a.store.Load(ctx, a.sources)
	if err != nil {
		return outcome, fmt.Errorf("load the announcer's records: %w", err)
	}
	set, health := a.collector.Collect(ctx, now, floorsOf(records))
	healthy := anyHealthy(health)
	fresh, updates := delta(records, set, health)
	for _, report := range health {
		record, changed := updates[report.Source]
		if !changed {
			continue
		}
		if err := a.store.Save(ctx, report.Source, record); err != nil {
			return outcome, fmt.Errorf("save the record of %s: %w", report.Source, err)
		}
	}

	var messages []discord.Message
	if healthy {
		// The digest is computed from the current listing rather than from what
		// is new, so it publishes on a first run like any other. It is skipped
		// when every calendar failed, because a week described from nothing is
		// not a quiet week.
		if outcome.Kind == KindWeekly && a.claimDigest(ctx, now) {
			messages = append(messages, renderDigest(set, now)...)
		}
		messages = append(messages, renderDaily(fresh)...)
	}

	published, err := a.publish(ctx, messages)
	outcome.Events, outcome.New, outcome.Messages, outcome.Published = len(set), len(fresh), len(messages), published
	a.logger.Info("announcement run",
		"kind", outcome.Kind,
		"calendars", statuses(health),
		"events", outcome.Events,
		"new", outcome.New,
		"messages", published,
		"next", nextRun(now).Format(time.RFC3339),
	)
	if err != nil {
		a.logger.Error("announcement run did not publish everything",
			"kind", outcome.Kind, "published", published, "of", len(messages), "error", err)
	}
	return outcome, nil
}

// claimDigest reports whether this run publishes today's digest. A store that
// cannot answer gets the cautious answer, no: a missing digest is a gap, and a
// second one a duplicate.
func (a *Announcer) claimDigest(ctx context.Context, now time.Time) bool {
	claimed, err := a.store.ClaimDigest(ctx, now)
	switch {
	case err != nil:
		a.logger.Error("could not record the weekly digest, skipping it", "error", err)
		return false
	case !claimed:
		a.logger.Info("the weekly digest already went out today")
		return false
	}
	return true
}

// publish sends the messages in order and stops at the first failure.
//
// There is no retry. A rejected post is almost always a permission that was
// changed or a channel that was deleted, and retrying that in a loop is the
// documented way to have an address blocked by Discord's edge. The messages
// that did go out are counted as published and are not repeated tomorrow: a
// duplicate is more visible to a reader than a gap.
func (a *Announcer) publish(ctx context.Context, messages []discord.Message) (int, error) {
	for i, message := range messages {
		if err := a.publisher.CreateMessage(ctx, a.channelID, message); err != nil {
			var apiErr *discord.APIError
			if errors.As(err, &apiErr) && apiErr.Unauthorized() {
				return i, fmt.Errorf("announcing is not permitted in this channel: %w", err)
			}
			return i, err
		}
	}
	return len(messages), nil
}

// anyHealthy reports whether at least one calendar contributed to the run.
func anyHealthy(health []events.Health) bool {
	for _, report := range health {
		if report.OK() {
			return true
		}
	}
	return false
}

// statuses renders the per-calendar outcome for the run's log line. It carries
// counts and statuses only: no title, no place, no link, nothing a member wrote
// or was named in.
func statuses(health []events.Health) []any {
	rendered := make([]any, 0, len(health)*2)
	for _, report := range health {
		rendered = append(rendered,
			string(report.Source),
			fmt.Sprintf("%s/%d records/%d skipped", report.Status, report.Records, report.Skipped),
		)
	}
	return rendered
}
