// Package announce posts upcoming cloud native events into one Discord channel
// on a daily schedule.
//
// It is the layer above internal/events and internal/discord: it decides when a
// run happens, what of a run is new, how it reads in Spanish and English, and
// what to do when publishing fails. It holds the only state the feature keeps —
// the set of events already announced — in memory, for the lifetime of the
// process and nowhere else.
//
// The scheduler is deliberately not attached to the gateway. Anything hung off
// the session's ready handler fires again on every reconnect, and the session
// reconnects with backoff whenever Discord drops it, so a bad afternoon would
// become a run of duplicate announcements.
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
	Collect(ctx context.Context, now time.Time) (events.Set, []events.Health)
}

// A Publisher posts a message to a channel. It is [discord.Client].
type Publisher interface {
	CreateMessage(ctx context.Context, channelID string, message discord.Message) error
}

// An Announcer publishes upcoming events into one channel on a schedule.
// Create one with [New] and drive it with [Announcer.Run].
type Announcer struct {
	collector Collector
	publisher Publisher
	channelID string
	logger    *slog.Logger
	seen      *seenSet

	// now is the clock, replaceable so that a run can be exercised at a chosen
	// instant instead of at whatever time the test happens to run.
	now func() time.Time
}

// New returns an announcer posting to channelID. An empty channelID leaves it
// switched off: [Announcer.Run] returns immediately, and the bot goes on serving
// commands as before.
func New(collector Collector, publisher Publisher, channelID string, logger *slog.Logger) *Announcer {
	return &Announcer{
		collector: collector,
		publisher: publisher,
		channelID: channelID,
		logger:    logger,
		seen:      newSeenSet(),
		now:       time.Now,
	}
}

// Run waits for each slot and performs the run, until ctx is cancelled.
//
// It blocks. The caller owns the goroutine it runs on, cancels it through ctx
// and waits for it before shutting down, which is the same contract every other
// goroutine in this repository keeps.
func (a *Announcer) Run(ctx context.Context) {
	if a.channelID == "" {
		a.logger.Info("event announcements are off", "reason", "no announcement channel configured")
		return
	}

	for {
		slot := nextRun(a.now())

		timer := time.NewTimer(slot.Sub(a.now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		a.runOnce(ctx, slot)

		// A run that outlasted the following slot does not immediately run again:
		// the loop simply asks for the next one, and the slot that passed is
		// reported rather than silently skipped.
		if missed, skipped := skippedSlot(slot, a.now()); skipped {
			a.logger.Warn("run overran the next slot, skipping it", "missed", missed.Format(time.RFC3339))
		}
	}
}

// skippedSlot reports the slot that passed while a run was still in flight.
func skippedSlot(slot, now time.Time) (time.Time, bool) {
	expected := slot.AddDate(0, 0, 1)
	if nextRun(now).After(expected) {
		return expected, true
	}
	return time.Time{}, false
}

// runOnce performs one run: read both calendars, work out what is new, publish
// it, and account for the whole thing in a single log line.
func (a *Announcer) runOnce(ctx context.Context, slot time.Time) {
	kind := kindOf(slot)

	set, health := a.collector.Collect(ctx, slot)
	healthy := anyHealthy(health)

	fresh := a.seen.delta(set, healthy)

	var messages []discord.Message
	if healthy {
		// The digest is computed from the current listing rather than from what
		// is new, so it publishes on a first run like any other. It is skipped
		// when every calendar failed, because a week described from nothing is
		// not a quiet week.
		if kind == KindWeekly {
			messages = append(messages, renderDigest(set, slot)...)
		}
		messages = append(messages, renderDaily(fresh)...)
	}

	published, err := a.publish(ctx, messages)

	a.logger.Info("announcement run",
		"kind", kind,
		"calendars", statuses(health),
		"events", len(set),
		"new", len(fresh),
		"messages", published,
		"next", nextRun(a.now()).Format(time.RFC3339),
	)
	if err != nil {
		a.logger.Error("announcement run did not publish everything",
			"kind", kind, "published", published, "of", len(messages), "error", err)
	}
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
