package events

import "testing"

func TestIdentityIsQualifiedByItsSource(t *testing.T) {
	// The two calendars number their events independently, so the same id can
	// legitimately appear in both. Identity must keep them apart.
	community := Event{Source: SourceCommunity, ID: "42"}
	conference := Event{Source: SourceConference, ID: "42"}

	if community.Identity() == conference.Identity() {
		t.Fatalf("identities collide across sources: %q", community.Identity())
	}
	if want := "community/42"; community.Identity() != want {
		t.Errorf("Identity() = %q, want %q", community.Identity(), want)
	}
}
