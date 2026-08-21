package announce

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// conference builds a date-only conference event, as the Linux Foundation
// calendar publishes them.
func conference(id string, days int, name string) events.Event {
	start := day(testClock.AddDate(0, 0, days))
	return events.Event{
		Source:   events.SourceConference,
		ID:       id,
		Title:    name,
		Start:    start,
		End:      start.AddDate(0, 0, 3),
		LocalDay: start,
		Kind:     events.KindInPerson,
		Place:    "Salt Lake City, United States",
		URL:      "https://events.linuxfoundation.org/" + id + "/",
	}
}

func TestRenderDailyIsBilingualAndCarriesTheEvent(t *testing.T) {
	fresh := events.Set{event(events.SourceCommunity, "a", 3)}

	messages := renderDaily(fresh)

	if len(messages) != 1 {
		t.Fatalf("rendered %d messages, want 1", len(messages))
	}
	content := messages[0].Content

	for _, want := range []string{
		"Nuevos eventos cloud native", // Spanish
		"New cloud native events",     // English
		"Cloud Native Meetup a",       // the title, verbatim
		"Ayacucho, PE",
		"Cloud Native Ayacucho",
		"https://ocgroups.dev/cncf/group/ncz2zfb/event/a",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("message is missing %q:\n%s", want, content)
		}
	}
}

func TestRenderDailyIsEmptyWhenNothingIsNew(t *testing.T) {
	// A quiet day is a day with no message at all, not a message saying so.
	if messages := renderDaily(nil); messages != nil {
		t.Errorf("rendered %d messages for an empty delta, want none", len(messages))
	}
}

func TestRenderNamesEveryCalendarItReadsFrom(t *testing.T) {
	messages := renderDaily(events.Set{event(events.SourceCommunity, "a", 3)})

	content := messages[len(messages)-1].Content
	for _, want := range []string{"CNCF Community Groups", "CNCF Events", "Linux Foundation Events"} {
		if !strings.Contains(content, want) {
			t.Errorf("message does not attribute %q:\n%s", want, content)
		}
	}
}

func TestRenderUsesDiscordTimestampsForExactStarts(t *testing.T) {
	// The feed spans every continent, so the start is rendered in the reader's
	// own zone with a countdown rather than in one the bot picked.
	fresh := events.Set{event(events.SourceCommunity, "a", 3)}
	unix := fresh[0].Start.Unix()

	content := renderDaily(fresh)[0].Content

	if !strings.Contains(content, "<t:") || !strings.Contains(content, ":R>") {
		t.Errorf("message carries no Discord timestamp:\n%s", content)
	}
	if !strings.Contains(content, timestamp(unix)) {
		t.Errorf("message does not carry the event's own instant %d:\n%s", unix, content)
	}
}

// timestamp renders the absolute half of the markup, for assertions.
func timestamp(unix int64) string {
	return fmt.Sprintf("<t:%d:f>", unix)
}

func TestRenderUsesAPlainRangeForDateOnlyEvents(t *testing.T) {
	// The conference calendar publishes no clock time. Rendering one would state
	// something the source never said.
	fresh := events.Set{conference("319493", 60, "KubeCon + CloudNativeCon North America")}

	content := renderDaily(fresh)[0].Content

	if strings.Contains(content, "<t:") {
		t.Errorf("a date-only event was rendered as an instant:\n%s", content)
	}
	if !strings.Contains(content, "2026") {
		t.Errorf("message carries no date:\n%s", content)
	}
}

func TestRenderMarksAPlaceholderDate(t *testing.T) {
	forum := events.Event{
		Source:  events.SourceConference,
		ID:      "1",
		Title:   "Open Source Forum",
		DateTBA: true,
		URL:     "https://events.linuxfoundation.org/open-source-forum/",
	}

	content := renderDaily(events.Set{forum})[0].Content

	if !strings.Contains(content, "fecha por confirmar") || !strings.Contains(content, "date TBA") {
		t.Errorf("message does not mark the missing date:\n%s", content)
	}
}

func TestRenderMarksVirtualEventsWithoutAPlace(t *testing.T) {
	online := event(events.SourceCommunity, "a", 3)
	online.Kind = events.KindVirtual
	online.Place = ""

	content := renderDaily(events.Set{online})[0].Content

	if !strings.Contains(content, "en línea") || !strings.Contains(content, "online") {
		t.Errorf("message does not mark an online event:\n%s", content)
	}
}

func TestRenderEscapesMarkdown(t *testing.T) {
	// Titles are written by hundreds of organizers. One asterisk would otherwise
	// swallow the rest of the message in italics.
	hostile := event(events.SourceCommunity, "a", 3)
	hostile.Title = "Cloud *Native* _Day_ ~~2026~~ `code` |spoiler|"

	content := renderDaily(events.Set{hostile})[0].Content

	for _, raw := range []string{"*Native*", "_Day_", "~~2026~~", "`code`", "|spoiler|"} {
		if strings.Contains(content, raw) {
			t.Errorf("message contains unescaped %q:\n%s", raw, content)
		}
	}
	if !strings.Contains(content, `Cloud \*Native\*`) {
		t.Errorf("message does not escape the title:\n%s", content)
	}
}

func TestRenderCapsTheDailyAtTen(t *testing.T) {
	// Fifty new events in a day is a backfill upstream, not fifty new meetups.
	var fresh events.Set
	for i := range 15 {
		fresh = append(fresh, event(events.SourceCommunity, string(rune('a'+i)), i+1))
	}

	messages := renderDaily(fresh)

	shown := 0
	for _, message := range messages {
		shown += strings.Count(message.Content, "ocgroups.dev/cncf/group/")
	}
	if shown != deltaLimit {
		t.Errorf("listed %d events, want %d", shown, deltaLimit)
	}

	last := messages[len(messages)-1].Content
	if !strings.Contains(last, "y 5 más") || !strings.Contains(last, "and 5 more") {
		t.Errorf("message does not report the 5 left out:\n%s", last)
	}
	if !strings.Contains(last, exploreURL) {
		t.Errorf("the overflow line does not link anywhere:\n%s", last)
	}
}

func TestRenderStaysWithinTheContentLimit(t *testing.T) {
	// A week where every group posts at once. The digest has to fit Discord's
	// limit whatever the calendars hand over.
	longTitle := strings.Repeat("Kubernetes and cloud native platform engineering ", 3)
	var set events.Set
	for i := range 60 {
		crowded := event(events.SourceCommunity, string(rune('a'+i%26))+string(rune('a'+i/26)), i%7)
		crowded.Title = longTitle + string(rune('a'+i%26))
		crowded.Group = "Cloud Native Group " + string(rune('a'+i%26))
		set = append(set, crowded)
	}

	messages := renderDigest(set, testClock)

	if len(messages) > maxMessages {
		t.Errorf("rendered %d messages, want at most %d", len(messages), maxMessages)
	}
	if len(messages) < 2 {
		t.Fatalf("rendered %d messages; the fixture was meant to overflow one", len(messages))
	}
	for i, message := range messages {
		if length := utf8.RuneCountInString(message.Content); length > contentLimit {
			t.Errorf("message %d is %d characters, over the %d limit", i, length, contentLimit)
		}
	}

	last := messages[len(messages)-1].Content
	if !strings.Contains(last, "más") || !strings.Contains(last, "more") {
		t.Errorf("nothing reported what was left out:\n%s", last)
	}
	if !strings.Contains(last, attribution) {
		t.Errorf("the last message carries no attribution:\n%s", last)
	}
}

func TestRenderDigestListsTheComingSevenDays(t *testing.T) {
	set := events.Set{
		event(events.SourceCommunity, "today", 0),
		event(events.SourceCommunity, "inside", 6),
		event(events.SourceCommunity, "boundary", 7),
		event(events.SourceCommunity, "beyond", 20),
	}

	content := renderDigest(set, testClock)[0].Content

	if !strings.Contains(content, "event/today") || !strings.Contains(content, "event/inside") {
		t.Errorf("digest is missing an event inside the week:\n%s", content)
	}
	for _, outside := range []string{"event/boundary", "event/beyond"} {
		if strings.Contains(content, outside) {
			t.Errorf("digest lists %s, which is outside the week:\n%s", outside, content)
		}
	}
}

func TestRenderDigestSaysSoWhenTheWeekIsEmpty(t *testing.T) {
	// The digest is the heartbeat: it publishes even with nothing to say, so a
	// quiet week is distinguishable from a dead bot.
	messages := renderDigest(events.Set{event(events.SourceCommunity, "later", 30)}, testClock)

	if len(messages) == 0 {
		t.Fatal("no digest was rendered for an empty week")
	}
	content := messages[0].Content
	if !strings.Contains(content, "No hay eventos") || !strings.Contains(content, "No events are scheduled") {
		t.Errorf("digest does not say the week is empty:\n%s", content)
	}
}

func TestRenderDigestNotifiesWhileTheDailyDoesNot(t *testing.T) {
	daily := renderDaily(events.Set{event(events.SourceCommunity, "a", 3)})
	digest := renderDigest(events.Set{event(events.SourceCommunity, "a", 3)}, testClock)

	if daily[0].Flags&discord.FlagSuppressNotifications == 0 {
		t.Error("the daily message would push a notification")
	}
	if digest[0].Flags&discord.FlagSuppressNotifications != 0 {
		t.Error("the weekly digest is silent; it is the one message worth an interruption")
	}
	if daily[0].Flags&discord.FlagSuppressEmbeds == 0 {
		t.Error("link previews are not suppressed; ten links would arrive as ten cards")
	}
}

func TestRenderHorizonListsAtMostFiveConferences(t *testing.T) {
	set := events.Set{event(events.SourceCommunity, "a", 2)}
	for i := range 8 {
		set = append(set, conference(string(rune('a'+i)), 30+i*10, "KubeCon "+string(rune('a'+i))))
	}

	var content strings.Builder
	for _, message := range renderDigest(set, testClock) {
		content.WriteString(message.Content)
	}

	if !strings.Contains(content.String(), "En el horizonte") {
		t.Fatalf("digest has no horizon block:\n%s", content.String())
	}
	listed := strings.Count(content.String(), "https://events.linuxfoundation.org")
	if listed != horizonLimit {
		t.Errorf("horizon lists %d conferences, want %d", listed, horizonLimit)
	}
}

func TestRenderHorizonIsOmittedWhenThereAreNoConferences(t *testing.T) {
	messages := renderDigest(events.Set{event(events.SourceCommunity, "a", 2)}, testClock)

	for _, message := range messages {
		if strings.Contains(message.Content, "En el horizonte") {
			t.Errorf("digest carries an empty horizon block:\n%s", message.Content)
		}
	}
}

func TestDateRangeCollapsesWhatBothEndsShare(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		end   time.Time
		want  string
	}{
		{
			name:  "same month",
			start: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
			want:  "9–12 Nov 2026",
		},
		{
			name:  "across months",
			start: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC),
			want:  "30 Nov – 2 Dec 2026",
		},
		{
			name:  "single day",
			start: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
			want:  "23 Sep 2026",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dateRange(tt.start, tt.end); got != tt.want {
				t.Errorf("dateRange() = %q, want %q", got, tt.want)
			}
		})
	}
}
