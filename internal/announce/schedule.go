package announce

import "time"

const (
	// runHourUTC is the daily slot. 13:00 UTC is 08:00 in Ayacucho: Peru keeps a
	// fixed UTC-5 offset with no daylight saving, so this is the same local hour
	// every day of the year. It is also late enough that organizers in Europe and
	// Asia who published overnight are already listed. It has to match the
	// schedule that invokes the run, cron(0 13 * * ? *) in personal-infra.
	runHourUTC = 13

	// weeklyWeekday is the day the digest goes out, in UTC.
	weeklyWeekday = time.Monday
)

// A Kind is what a run does. Every run announces what is new; the weekly one
// also publishes the digest.
type Kind string

// The kinds of run.
const (
	KindDaily  Kind = "daily"
	KindWeekly Kind = "weekly"
)

// nextRun returns the first slot strictly after now. The schedule that fires
// the slots is EventBridge Scheduler's (personal-infra §5.24); this only names
// the next one in a run's log line, so a missed run is visible as a gap
// between the "next" it promised and the run that actually followed.
func nextRun(now time.Time) time.Time {
	utc := now.UTC()

	slot := time.Date(utc.Year(), utc.Month(), utc.Day(), runHourUTC, 0, 0, 0, time.UTC)
	if !slot.After(utc) {
		slot = slot.AddDate(0, 0, 1)
	}
	return slot
}

// kindOf reports what the run at slot does.
func kindOf(slot time.Time) Kind {
	if slot.UTC().Weekday() == weeklyWeekday {
		return KindWeekly
	}
	return KindDaily
}
