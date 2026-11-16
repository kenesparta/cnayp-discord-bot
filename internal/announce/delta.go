package announce

import (
	"sort"

	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// delta compares one run with the stored records, calendar by calendar, and
// returns the events to announce and the records to save.
//
// Each calendar is judged on its own:
//
//   - One whose answer is unhealthy, failed or degraded, keeps its record
//     exactly as it was. Its events are missing from this run for a reason that
//     has nothing to do with the calendar being empty, and recording that would
//     make every one of them look new when it recovers.
//   - A healthy one with no record yet is seeded silently: what it lists is
//     recorded and none of it is announced. That is what keeps a first run, or a
//     run after the records were lost, from announcing a hundred events.
//   - Otherwise what it lists and its record did not have is new, and the record
//     becomes what it lists now. It is replaced wholesale rather than added to,
//     so an event that disappears upstream and comes back is announced again,
//     which is the right answer for a cancelled-then-rescheduled meetup.
//
// One edge stays: an event both the community calendar and the CNCF website
// carry is listed under whichever wins the cross-calendar dedup
// (internal/events), so while the community calendar is down it can appear,
// once, under the website's identity.
func delta(records map[events.Source]Record, set events.Set, health []events.Health) (events.Set, map[events.Source]Record) {
	listed := make(map[events.Source][]string)
	for _, event := range set {
		listed[event.Source] = append(listed[event.Source], event.Identity())
	}

	updates := make(map[events.Source]Record)
	announce := make(map[string]bool)
	for _, report := range health {
		if !report.OK() {
			continue
		}
		current := listed[report.Source]
		sort.Strings(current)
		previous, known := records[report.Source]
		updates[report.Source] = Record{Seen: current, Floor: report.Records}
		if !known {
			continue
		}

		seen := make(map[string]bool, len(previous.Seen))
		for _, identity := range previous.Seen {
			seen[identity] = true
		}
		for _, identity := range current {
			if !seen[identity] {
				announce[identity] = true
			}
		}
	}

	var fresh events.Set
	for _, event := range set {
		if announce[event.Identity()] {
			fresh = append(fresh, event)
		}
	}
	return fresh, updates
}

// floorsOf extracts the degraded guard's floors from the records.
func floorsOf(records map[events.Source]Record) events.Floors {
	floors := make(events.Floors, len(records))
	for source, record := range records {
		floors[source] = record.Floor
	}
	return floors
}
