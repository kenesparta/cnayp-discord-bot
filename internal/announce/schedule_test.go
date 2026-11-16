package announce

import (
	"testing"
	"time"
)

func TestNextRunIsTheNextSlotInUTC(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before today's slot",
			now:  time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC),
			want: time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC),
		},
		{
			// Exactly on the slot means it has just run: the next one is tomorrow.
			name: "on the slot",
			now:  time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC),
		},
		{
			name: "after today's slot",
			now:  time.Date(2026, 8, 20, 13, 0, 1, 0, time.UTC),
			want: time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC),
		},
		{
			name: "across a month boundary",
			now:  time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC),
			want: time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC),
		},
		{
			name: "across a year boundary",
			now:  time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC),
			want: time.Date(2027, 1, 1, 13, 0, 0, 0, time.UTC),
		},
		{
			// A caller in another zone gets the same instant.
			name: "from a non-UTC clock",
			now:  time.Date(2026, 8, 20, 3, 0, 0, 0, time.FixedZone("-05", -5*60*60)),
			want: time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextRun(tt.now)

			if !got.Equal(tt.want) {
				t.Errorf("nextRun(%v) = %v, want %v", tt.now, got, tt.want)
			}
			if !got.After(tt.now) {
				t.Errorf("nextRun(%v) = %v, which is not in the future", tt.now, got)
			}
		})
	}
}

func TestNextRunNeverReplaysAMissedSlot(t *testing.T) {
	// A container that restarts five minutes after the slot must wait for
	// tomorrow. Firing immediately is how a redeploy turns into a second
	// announcement of the same day.
	restart := time.Date(2026, 8, 20, 13, 5, 0, 0, time.UTC)

	if got := nextRun(restart); got.Day() != 21 {
		t.Errorf("nextRun(%v) = %v, want tomorrow's slot", restart, got)
	}
}

func TestRunKindIsWeeklyOnMonday(t *testing.T) {
	monday := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if monday.Weekday() != time.Monday {
		t.Fatalf("fixture is a %v, not a Monday", monday.Weekday())
	}

	if got := kindOf(monday); got != KindWeekly {
		t.Errorf("kindOf(Monday) = %q, want %q", got, KindWeekly)
	}
	for day := 1; day < 7; day++ {
		other := monday.AddDate(0, 0, day)
		if got := kindOf(other); got != KindDaily {
			t.Errorf("kindOf(%v) = %q, want %q", other.Weekday(), got, KindDaily)
		}
	}
}
