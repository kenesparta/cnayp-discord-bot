package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
	"github.com/kenesparta/cnayp-discord-bot/internal/awsapi"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

// fakeDynamo keeps items by pk and honours the one condition the table uses.
type fakeDynamo struct {
	items map[string]awsapi.Item
	err   error
}

func (f *fakeDynamo) GetItem(_ context.Context, _ string, key awsapi.Item) (awsapi.Item, error) {
	if f.err != nil {
		return nil, f.err
	}
	pk, _ := key["pk"].String()
	return f.items[pk], nil
}

func (f *fakeDynamo) PutItem(_ context.Context, _ string, item awsapi.Item, condition string) error {
	if f.err != nil {
		return f.err
	}
	pk, _ := item["pk"].String()
	if condition == "attribute_not_exists(pk)" {
		if _, exists := f.items[pk]; exists {
			return &awsapi.Error{StatusCode: 400, Type: "ConditionalCheckFailedException"}
		}
	}
	f.items[pk] = item
	return nil
}

var clock = time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)

func tableAt(db *fakeDynamo, now time.Time) *Table {
	return &Table{db: db, name: "cnayp-discord-bot-state", now: func() time.Time { return now }}
}

func TestTableRoundTripsARecord(t *testing.T) {
	db := &fakeDynamo{items: map[string]awsapi.Item{}}
	table := tableAt(db, clock)
	want := announce.Record{Seen: []string{"community/a", "community/b"}, Floor: 12}

	if err := table.Save(t.Context(), events.SourceCommunity, want); err != nil {
		t.Fatal(err)
	}
	records, err := table.Load(t.Context(), events.Sources())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := records[events.SourceCommunity]
	if !ok || got.Floor != 12 || len(got.Seen) != 2 || got.Seen[1] != "community/b" {
		t.Errorf("record = %+v, %v", got, ok)
	}
	if _, ok := records[events.SourceKCD]; ok {
		t.Error("a calendar never saved came back with a record")
	}
	if _, ok := db.items["calendar#community"]; !ok {
		t.Errorf("the item key is not calendar#<source>: %v", db.items)
	}
}

func TestTableKeepsAnEmptyListing(t *testing.T) {
	// A healthy calendar whose events all fell outside the window still has a
	// record, and an empty one must not read back as "never seen".
	db := &fakeDynamo{items: map[string]awsapi.Item{}}
	table := tableAt(db, clock)
	if err := table.Save(t.Context(), events.SourceKCD, announce.Record{Floor: 3}); err != nil {
		t.Fatal(err)
	}
	records, err := table.Load(t.Context(), []events.Source{events.SourceKCD})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := records[events.SourceKCD]; !ok {
		t.Error("an empty listing read back as no record")
	}
}

func TestTableTreatsAnExpiredRecordAsAbsent(t *testing.T) {
	db := &fakeDynamo{items: map[string]awsapi.Item{}}
	if err := tableAt(db, clock).Save(t.Context(), events.SourceCommunity, announce.Record{Floor: 1}); err != nil {
		t.Fatal(err)
	}

	later := tableAt(db, clock.Add(recordLifetime+time.Hour))
	records, err := later.Load(t.Context(), []events.Source{events.SourceCommunity})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := records[events.SourceCommunity]; ok {
		t.Error("an expired record still counted: DynamoDB's TTL is not to be trusted to have run")
	}
}

func TestTableRejectsAMalformedRecord(t *testing.T) {
	db := &fakeDynamo{items: map[string]awsapi.Item{
		"calendar#community": {"pk": awsapi.S("calendar#community"), "seen": awsapi.S("oops")},
	}}
	if _, err := tableAt(db, clock).Load(t.Context(), []events.Source{events.SourceCommunity}); err == nil {
		t.Error("a malformed record loaded without error")
	}
}

func TestTableClaimsTheDigestOncePerDay(t *testing.T) {
	db := &fakeDynamo{items: map[string]awsapi.Item{}}
	table := tableAt(db, clock)

	for i, want := range []bool{true, false} {
		claimed, err := table.ClaimDigest(t.Context(), clock.Add(time.Duration(i)*time.Minute))
		if err != nil || claimed != want {
			t.Errorf("claim %d = %v, %v; want %v", i+1, claimed, err, want)
		}
	}
	if claimed, _ := table.ClaimDigest(t.Context(), clock.AddDate(0, 0, 7)); !claimed {
		t.Error("the next week's digest could not be claimed")
	}
}

func TestTableReportsStoreErrors(t *testing.T) {
	db := &fakeDynamo{items: map[string]awsapi.Item{}, err: errors.New("throttled")}
	table := tableAt(db, clock)
	if _, err := table.Load(t.Context(), events.Sources()); err == nil {
		t.Error("Load hid a store error")
	}
	if err := table.Save(t.Context(), events.SourceCommunity, announce.Record{}); err == nil {
		t.Error("Save hid a store error")
	}
	if _, err := table.ClaimDigest(t.Context(), clock); err == nil {
		t.Error("ClaimDigest hid a store error")
	}
}

func TestMemoryBehavesLikeTheTable(t *testing.T) {
	memory := NewMemory()
	if err := memory.Save(t.Context(), events.SourceCommunity, announce.Record{Seen: []string{"community/a"}}); err != nil {
		t.Fatal(err)
	}
	records, _ := memory.Load(t.Context(), events.Sources())
	if len(records) != 1 {
		t.Errorf("records = %v", records)
	}
	first, _ := memory.ClaimDigest(t.Context(), clock)
	second, _ := memory.ClaimDigest(t.Context(), clock)
	if !first || second {
		t.Errorf("claims = %v, %v; want true, false", first, second)
	}
}
