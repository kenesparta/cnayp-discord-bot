//go:build contract

package announce

import (
	"log/slog"
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// TestContractRendersLiveCalendars performs a whole run against the real
// calendars and checks what would have been posted, without posting anything.
//
// It is the only check that exercises fetching, normalizing and rendering
// together against data nobody wrote for a test, which is where the surprises
// live: a title long enough to break the budget, a venue in a script nobody
// considered, a calendar that suddenly returns twice as much. It logs the
// messages it built, so `make contract` doubles as a preview of what the
// channel would receive.
func TestContractRendersLiveCalendars(t *testing.T) {
	collector := events.New(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))

	now := time.Now()
	set, health := collector.Collect(t.Context(), now, nil)
	for _, report := range health {
		t.Logf("calendar %s: %s, %d records, %d skipped", report.Source, report.Status, report.Records, report.Skipped)
	}
	t.Logf("normalized to %d events", len(set))

	if len(set) == 0 {
		t.Fatal("the calendars produced no events at all")
	}

	rendered := map[string][]discord.Message{
		"digest": renderDigest(set, now),
		"daily":  renderDaily(set),
	}
	if len(rendered["digest"]) == 0 {
		t.Error("no digest was rendered")
	}

	for name, messages := range rendered {
		if len(messages) > maxMessages {
			t.Errorf("%s rendered %d messages, want at most %d", name, len(messages), maxMessages)
		}
		for i, message := range messages {
			length := utf8.RuneCountInString(message.Content)
			if length > contentLimit {
				t.Errorf("%s message %d is %d characters, over the %d limit", name, i+1, length, contentLimit)
			}
			t.Logf("---- %s message %d (%d chars, flags %d) ----\n%s", name, i+1, length, message.Flags, message.Content)
		}
	}
}
