package announce

import "time"

const (
	// runHourUTC is the daily slot. 13:00 UTC is 08:00 in Ayacucho: Peru keeps a
	// fixed UTC-5 offset with no daylight saving, so this is the same local hour
	// every day of the year. It is also late enough that organizers in Europe and
	// Asia who published overnight are already listed.
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

// nextRun returns the first slot strictly after now.
//
// The schedule is computed from the wall clock rather than counted off a ticker
// because the process restarts often — the image publishes on every push to the
// default branch — and an elapsed-time ticker would restart its 24 hours with
// it, drifting a little further from the intended hour on every deploy. A
// monotonic ticker also does not advance while a host is suspended.
//
// The other half of the reason is that a slot which passed while the process
// was down is not replayed: a restart at 13:05 waits for tomorrow rather than
// firing immediately. That is what makes an in-memory record of what has
// already been announced sufficient — a restart cannot produce a second run of
// the same slot, so it cannot produce a duplicate.
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
