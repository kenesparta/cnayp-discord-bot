package announce

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

const (
	// contentLimit is Discord's cap on a message body, in characters.
	contentLimit = 2000

	// footerReserve holds back enough of that budget for the attribution and, if
	// it is needed, the overflow line. Both are appended after the blocks are
	// laid out, so the space has to be kept rather than discovered.
	footerReserve = 240

	// maxMessages bounds one run. Rendering the whole six month window one line
	// per event comes to roughly fifteen thousand characters; a cap is what
	// stands between a busy week and a wall of text.
	maxMessages = 3

	// deltaLimit bounds the daily message. A calendar that suddenly lists fifty
	// new events is far more likely to be a backfill upstream than fifty new
	// meetups.
	deltaLimit = 10

	// horizonLimit bounds the digest's closing block.
	horizonLimit = 5

	// digestDays is the width of the weekly digest window.
	digestDays = 7

	// exploreURL is where the overflow line sends a reader for the rest.
	exploreURL = "https://ocgroups.dev/explore/events"
)

// The framing the bot writes around text it did not write. Spanish first, then
// English, on one line each so that neither reads as an afterthought. Event
// titles, organizer names and places are never translated: they are reproduced
// exactly as their organizers wrote them.
const (
	headingDaily   = "📅 **Nuevos eventos cloud native** · **New cloud native events**"
	headingDigest  = "🗓️ **Esta semana** · **This week**"
	headingHorizon = "🌎 **En el horizonte** · **On the horizon**"
	emptyWeek      = "_No hay eventos programados para esta semana._\n_No events are scheduled for this week._"
	attribution    = "-# Fuente · Source: CNCF Community Groups · Linux Foundation Events"
	markerTBA      = "fecha por confirmar · date TBA"
	markerOnline   = "en línea · online"
	overflowLine   = "-# … y %d más · and %d more: %s"
)

// blockSeparator is the blank line between one event and the next.
const blockSeparator = "\n\n"

// dailyFlags suppress the push notification: a daily feed that buzzes every
// phone is a daily feed that gets muted. The message still appears and still
// marks the channel unread.
const dailyFlags = discord.FlagSuppressEmbeds | discord.FlagSuppressNotifications

// digestFlags notify normally. The weekly digest is the one message worth
// interrupting someone for, and it is also the heartbeat that says the bot is
// alive on a week where nothing else was posted.
const digestFlags = discord.FlagSuppressEmbeds

// renderDaily lays out the events that are new since the previous run.
func renderDaily(fresh events.Set) []discord.Message {
	if len(fresh) == 0 {
		return nil
	}

	shown, omitted := trim(fresh, deltaLimit)
	blocks := make([]string, 0, len(shown))
	for _, event := range shown {
		blocks = append(blocks, block(event))
	}

	return pack(headingDaily, blocks, omitted, dailyFlags)
}

// renderDigest lays out the coming week, followed by the conferences on the
// horizon.
//
// It is computed only from the current listing, never from what has already
// been announced, so it publishes on the first run after a restart like any
// other and says the same thing whoever is reading.
func renderDigest(set events.Set, now time.Time) []discord.Message {
	blocks := make([]string, 0, len(set))

	thisWeek := week(set, now)
	if len(thisWeek) == 0 {
		blocks = append(blocks, emptyWeek)
	}
	for _, event := range thisWeek {
		blocks = append(blocks, block(event))
	}

	if conferences := horizon(set); len(conferences) > 0 {
		blocks = append(blocks, headingHorizon)
		for _, event := range conferences {
			blocks = append(blocks, block(event))
		}
	}

	return pack(headingDigest, blocks, 0, digestFlags)
}

// week returns the events whose own local day falls in the seven days that
// begin on the day of the run.
//
// The comparison is by calendar day rather than by instant, and by the event's
// day rather than the run's: an evening meetup in Lima and a morning one in
// Tokyo can share an instant window and belong to different days.
func week(set events.Set, now time.Time) events.Set {
	start := day(now.UTC())
	end := start.AddDate(0, 0, digestDays)

	var within events.Set
	for _, event := range set {
		if event.DateTBA {
			continue
		}
		eventDay := day(event.LocalDay)
		if !eventDay.Before(start) && eventDay.Before(end) {
			within = append(within, event)
		}
	}
	return within
}

// horizon returns the flagship conferences worth knowing about months ahead:
// the ones a member would submit a talk to, travel to, or organize a watch
// party around. Without it a conference only ever surfaces in the week it
// happens, which is too late to be useful.
func horizon(set events.Set) events.Set {
	var conferences events.Set
	for _, event := range set {
		if event.Source == events.SourceConference {
			conferences = append(conferences, event)
		}
	}

	shown, _ := trim(conferences, horizonLimit)
	return shown
}

// day reduces an instant to its calendar day, so that two days can be compared
// without their zones taking part.
func day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// trim cuts a set down to limit and reports how many were left out.
func trim(set events.Set, limit int) (events.Set, int) {
	if len(set) <= limit {
		return set, 0
	}
	return set[:limit], len(set) - limit
}

// block renders one event: what it is, when, where, who runs it, and where to
// read the rest.
func block(event events.Event) string {
	var line strings.Builder

	line.WriteString("**")
	line.WriteString(escape(event.Title))
	line.WriteString("**\n")
	line.WriteString(when(event))

	if marker, where := place(event); where != "" {
		line.WriteString(" · " + marker + " ")
		line.WriteString(escape(where))
	}
	if event.Group != "" {
		line.WriteString(" · 👥 ")
		line.WriteString(escape(event.Group))
	}
	line.WriteString("\n")
	line.WriteString(event.URL)

	return line.String()
}

// when renders the start.
//
// An event with a real instant is rendered as Discord timestamp markup, so that
// every reader sees it in their own time zone with a live countdown — which for
// a feed spanning every continent is the difference between useful and
// arithmetic. An event that came from the calendar publishing bare dates is
// rendered as a plain date range and never as an instant: doing otherwise would
// state a clock time the source never published.
func when(event events.Event) string {
	switch {
	case event.DateTBA:
		return "🗓️ " + markerTBA
	case event.Exact:
		unix := event.Start.Unix()
		return fmt.Sprintf("🗓️ <t:%d:f> (<t:%d:R>)", unix, unix)
	default:
		return "🗓️ " + dateRange(event.Start, event.End)
	}
}

// dateRange renders a floating calendar range, collapsing what the two ends
// share.
func dateRange(start, end time.Time) string {
	if end.IsZero() || end.Equal(start) {
		return start.Format("2 Jan 2006")
	}
	if start.Year() == end.Year() && start.Month() == end.Month() {
		return fmt.Sprintf("%d–%s", start.Day(), end.Format("2 Jan 2006"))
	}
	return start.Format("2 Jan") + " – " + end.Format("2 Jan 2006")
}

// place renders where an event happens, together with the marker that precedes
// it: a pin for somewhere to go, a screen for somewhere to log in.
func place(event events.Event) (marker, where string) {
	if event.Place != "" {
		return "📍", event.Place
	}
	if event.Kind == events.KindVirtual {
		return "💻", markerOnline
	}
	return "", ""
}

// markdownSpecials are the characters Discord reads as formatting. Escaping is
// done here rather than in internal/events because what needs escaping depends
// entirely on where the text is going.
const markdownSpecials = "\\*_~`|"

// escape neutralises formatting in text the bot did not write, so that a title
// containing an asterisk cannot swallow the rest of the message in italics.
//
// It is not what stops a mention: an @everyone in a title is stopped by the
// allowed-mentions field on the message itself, because escaping cannot be
// relied on for that and being wrong about it pings a whole server.
func escape(text string) string {
	var escaped strings.Builder
	escaped.Grow(len(text))

	for _, r := range text {
		if strings.ContainsRune(markdownSpecials, r) {
			escaped.WriteRune('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

// pack lays a heading and a list of blocks into at most maxMessages messages,
// each under Discord's content limit, and closes the last one with the
// attribution and, where something was left out, a line saying how much.
func pack(heading string, blocks []string, omitted int, flags discord.MessageFlags) []discord.Message {
	budget := contentLimit - footerReserve

	var messages []discord.Message
	current := heading

	for i, block := range blocks {
		candidate := current + blockSeparator + block
		if utf8.RuneCountInString(candidate) <= budget {
			current = candidate
			continue
		}

		// The block does not fit. Start another message unless this was the last
		// one available, in which case what remains is reported rather than lost
		// silently.
		if len(messages)+2 > maxMessages {
			omitted += len(blocks) - i
			break
		}
		messages = append(messages, discord.Message{Content: current, Flags: flags})
		current = block
	}

	footer := attribution
	if omitted > 0 {
		footer = fmt.Sprintf(overflowLine, omitted, omitted, exploreURL) + "\n" + attribution
	}

	return append(messages, discord.Message{Content: current + blockSeparator + footer, Flags: flags})
}
