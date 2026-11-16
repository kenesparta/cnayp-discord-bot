// Package state keeps the announcer's records between runs: in the DynamoDB
// table personal-infra provisions for the bot (its §5.24) when running on
// Lambda, and in memory otherwise.
//
// The item design is this repository's; personal-infra fixes only the table:
// partition key pk (S), TTL attribute expires_at.
//
//	pk = "calendar#<source>"     seen (L of S)  identities the calendar listed on its last healthy run
//	                             floor (N)      how many events it returned then
//	                             updated_at (S), expires_at (N)
//	pk = "digest#<YYYY-MM-DD>"   expires_at (N) the weekly digest of that day went out
//
// Nothing in either is about a person: an identity is a calendar's own id for a
// public event. That is the claim the privacy policy makes, and it is why the
// table needs no backup: losing it costs one silent run (personal-infra G57).
package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kenesparta/cnayp-discord-bot/internal/announce"
	"github.com/kenesparta/cnayp-discord-bot/internal/awsapi"
	"github.com/kenesparta/cnayp-discord-bot/internal/events"
)

const (
	// recordLifetime is how long a calendar's record outlives its last healthy
	// run. A calendar silent for longer is seeded again, silently, rather than
	// judged against a listing that old, which would make most of what it lists
	// look new.
	recordLifetime = 35 * 24 * time.Hour
	// digestLifetime only has to outlast the day it guards.
	digestLifetime = 8 * 24 * time.Hour
)

// dynamo is the part of [awsapi.DynamoDB] the table uses.
type dynamo interface {
	GetItem(ctx context.Context, table string, key awsapi.Item) (awsapi.Item, error)
	PutItem(ctx context.Context, table string, item awsapi.Item, condition string) error
}

// Table is the DynamoDB-backed [announce.Store].
type Table struct {
	db   dynamo
	name string
	now  func() time.Time
}

// NewTable returns a store over the table name.
func NewTable(db *awsapi.DynamoDB, name string) *Table {
	return &Table{db: db, name: name, now: time.Now}
}

// Load reads the record of each source with one consistent read apiece.
//
// An expired record counts as absent. DynamoDB's TTL deletes an item some time
// after expires_at passes, sometimes days after, so the expiry is checked here
// rather than trusted to have happened.
func (t *Table) Load(ctx context.Context, sources []events.Source) (map[events.Source]announce.Record, error) {
	now := t.now()
	records := make(map[events.Source]announce.Record, len(sources))
	for _, source := range sources {
		item, err := t.db.GetItem(ctx, t.name, awsapi.Item{"pk": awsapi.S(calendarKey(source))})
		if err != nil {
			return nil, fmt.Errorf("read the record of %s: %w", source, err)
		}
		if item == nil {
			continue
		}
		record, expires, err := decodeRecord(item)
		if err != nil {
			return nil, fmt.Errorf("the record of %s is malformed: %w", source, err)
		}
		if now.Unix() >= expires {
			continue
		}
		records[source] = record
	}
	return records, nil
}

// Save replaces the record of source.
func (t *Table) Save(ctx context.Context, source events.Source, record announce.Record) error {
	now := t.now()
	seen := make([]awsapi.Attribute, 0, len(record.Seen))
	for _, identity := range record.Seen {
		seen = append(seen, awsapi.S(identity))
	}
	item := awsapi.Item{
		"pk":         awsapi.S(calendarKey(source)),
		"seen":       awsapi.L(seen...),
		"floor":      awsapi.N(int64(record.Floor)),
		"updated_at": awsapi.S(now.UTC().Format(time.RFC3339)),
		"expires_at": awsapi.N(now.Add(recordLifetime).Unix()),
	}
	if err := t.db.PutItem(ctx, t.name, item, ""); err != nil {
		return fmt.Errorf("write the record of %s: %w", source, err)
	}
	return nil
}

// ClaimDigest writes the day's digest marker if it does not exist yet. Two runs
// of one day race on the condition, and only one of them wins.
func (t *Table) ClaimDigest(ctx context.Context, day time.Time) (bool, error) {
	item := awsapi.Item{
		"pk":         awsapi.S(digestKey(day)),
		"expires_at": awsapi.N(day.Add(digestLifetime).Unix()),
	}
	err := t.db.PutItem(ctx, t.name, item, "attribute_not_exists(pk)")
	switch {
	case awsapi.IsConditionFailed(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("write the digest marker: %w", err)
	}
	return true, nil
}

func calendarKey(source events.Source) string { return "calendar#" + string(source) }

func digestKey(day time.Time) string { return "digest#" + day.UTC().Format(time.DateOnly) }

// decodeRecord reads a calendar item back, with its expiry in Unix seconds.
func decodeRecord(item awsapi.Item) (announce.Record, int64, error) {
	list, ok := item["seen"].List()
	if !ok {
		return announce.Record{}, 0, fmt.Errorf("seen is not a list")
	}
	seen := make([]string, 0, len(list))
	for _, attribute := range list {
		identity, ok := attribute.String()
		if !ok {
			return announce.Record{}, 0, fmt.Errorf("seen holds a non-string")
		}
		seen = append(seen, identity)
	}
	floor, ok := item["floor"].Int()
	if !ok {
		return announce.Record{}, 0, fmt.Errorf("floor is not an integer")
	}
	expires, ok := item["expires_at"].Int()
	if !ok {
		return announce.Record{}, 0, fmt.Errorf("expires_at is not an integer")
	}
	return announce.Record{Seen: seen, Floor: int(floor)}, expires, nil
}

// Memory is an [announce.Store] that lives as long as the process: for running
// the bot locally, where every start is a first run.
type Memory struct {
	mu      sync.Mutex
	records map[events.Source]announce.Record
	digests map[string]bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{records: map[events.Source]announce.Record{}, digests: map[string]bool{}}
}

// Load returns the records it holds for sources.
func (m *Memory) Load(_ context.Context, sources []events.Source) (map[events.Source]announce.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make(map[events.Source]announce.Record, len(sources))
	for _, source := range sources {
		if record, ok := m.records[source]; ok {
			records[source] = record
		}
	}
	return records, nil
}

// Save replaces the record of source.
func (m *Memory) Save(_ context.Context, source events.Source, record announce.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[source] = record
	return nil
}

// ClaimDigest reports whether the day's digest is still to go out, and marks it.
func (m *Memory) ClaimDigest(_ context.Context, day time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := digestKey(day)
	if m.digests[key] {
		return false, nil
	}
	m.digests[key] = true
	return true, nil
}
