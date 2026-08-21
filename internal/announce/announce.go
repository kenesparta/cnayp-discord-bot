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
