package announce

import "github.com/kenesparta/cnayp-discord-bot/internal/events"

// A seenSet remembers which events have already been announced, for as long as
// the process lives and no longer.
//
// It is seeded silently: the first successful run records what is currently
// listed and announces nothing. That is the whole design. A restart therefore
// cannot re-announce a hundred events, at the cost of missing one that appeared
// while the process was down — a miss rather than a duplicate, and the weekly
// digest picks it up if it falls in that week. Nothing is written to disk, which
// is what keeps the published claim of no persistent storage true.
type seenSet struct {
	seen   map[string]struct{}
	seeded bool
}

// newSeenSet returns an empty, unseeded set.
func newSeenSet() *seenSet {
	return &seenSet{seen: make(map[string]struct{})}
}

// delta records set as the current listing and returns the events that were not
// in the previous one.
//
// When healthy is false the run learned nothing: every calendar failed, so the
// listing is empty for a reason that has nothing to do with the calendars being
// empty. Recording it would make every event look new tomorrow, so the set is
// left exactly as it was.
//
// The record is replaced wholesale rather than added to, so an event that
// disappears upstream and comes back is announced again — which is the right
// answer for a cancelled-then-rescheduled meetup.
func (s *seenSet) delta(set events.Set, healthy bool) events.Set {
	if !healthy {
		return nil
	}

	current := make(map[string]struct{}, len(set))
	for _, event := range set {
		current[event.Identity()] = struct{}{}
	}

	if !s.seeded {
		s.seen = current
		s.seeded = true
		return nil
	}

	var fresh events.Set
	for _, event := range set {
		if _, known := s.seen[event.Identity()]; !known {
			fresh = append(fresh, event)
		}
	}
	s.seen = current
	return fresh
}
