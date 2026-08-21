# Implementation plan: Event ingestion and normalization

**Prerequisites:**

- Go 1.26.5, the toolchain already pinned by `go.mod`. No new module dependency may be introduced — `go.mod` stays
  dependency-free.
- `golangci-lint` v2 with the repository's `.golangci.yml`; `revive` requires a doc comment on every exported const
  block, and unused parameters must be written as `_`.
- No new environment variable and no new secret. The announcement channel arrives with FR-0002.
- Network access to `ocgroups.dev` and `events.linuxfoundation.org` is needed only by the scheduled contract check,
  never by the normal test command.
- **FRs this depends on:** none. FR-0001 is a root of the dependency graph (PRD §8, Part 1).

### Phase 1: Package foundation

**1. Event shape** — Create the `internal/events` package and declare the single normalized event type that both
sources map onto, together with its attendance kind, its source discriminator and its identity accessor. The doc
comments carry the two facts that will otherwise be rediscovered painfully: one source publishes exact instants and
the other publishes floating calendar dates, and identity is only meaningful when qualified by source. See §6 of the
spec.

**2. Request discipline** — Add the shared fetch helper that every source call goes through, applying the identifying
User-Agent, the context deadline, the status check, the content-type assertion and the body size ceiling before any
decoding happens. This is the single place where a non-JSON response is refused rather than parsed. See §5 of the spec.

**3. Typed source errors** — Define the error type and its code set so that a caller can distinguish an unreachable
host from a rejected request, an unusable payload, an empty result and a degraded result. The codes are those listed
in the FR's Error handling block; nothing else may be added without amending the PRD.

### Phase 2: Source clients

**4. Community-group client** — Implement the client for the community-group listing: build the windowed query with
the deliberately widened date bounds, page on the response's total up to the ceiling, deduplicate while paging, map
each record onto the event shape, compose the canonical page address from its slugs, and count the records dropped for
missing mandatory fields. See §5 Contract A of the spec.

**5. Conference client** — Implement the client for the flagship conference listing: one request with the CNCF
category filter, multi-layout date parsing, explicit handling of the placeholder date, and mapping of the location
object onto the event's place and attendance kind. See §5 Contract B of the spec.

**6. Community days client** — Implement the client for the CNCF website's community-hosted listing, which is in
practice the Kubernetes Community Days: one taxonomy-filtered request per page, HTML-entity decoding of titles,
floating date parsing shared with the conference client, and paging until a short page. See §5 Contract C of the spec
for why the taxonomy filter is the only one worth sending.

**7. Time zone database** — Add the blank time zone data import to the binary's entry point, with a comment naming the
static base image as the reason, so that the local-day computation resolves every zone name the feeds use rather than
only the ones the container happens to ship.

### Phase 3: Normalization

**8. Status and time filtering** — Remove the records the FR excludes — canceled, test, unpublished, and any whose
start has already passed relative to the single clock reading taken at the start of the run.

**9. Series collapsing** — Reduce each recurring series to its next occurrence, preferring the identifier the source
publishes and falling back to the organizing group combined with a normalized title. See §3 of the spec for why the
fallback normalization strips case, punctuation and trailing occurrence numbering.

**10. Text sanitization** — Strip control and zero-width characters from upstream text, collapse whitespace, and
truncate titles to the FR's character budget at a word boundary with a visible ellipsis. Markup escaping is
deliberately not done here; it belongs to the renderer in FR-0002.

**11. Local day and ordering** — Compute each event's own calendar day from its published time zone, degrading to UTC
when the zone name is unknown, and sort the merged set by start instant.

**12. Cross-calendar de-duplication** — Reduce an event that two calendars both carry to a single listing, matching on
a canonical title and the day it starts, and keeping whichever record carries the most: a start time, a venue, an
organizer. See §3 of the spec for the case this exists for and the one cost it accepts.

### Phase 4: Collection and guards

**13. Collector** — Introduce the type that owns the source clients, fetches them one after another, feeds their
output through normalization and returns the resulting set together with a health report per source. A failure of one
source must never prevent the others from contributing.

**14. Health guards** — Add the two guards that make a silent upstream failure loud: a well-formed response carrying
no records is a failure rather than a quiet day, and a record count that collapses relative to that source's last
successful run is treated as suspect. Both discard the affected source's contribution for the run and leave the
baseline untouched. See §6 of the spec for the state these guards hold.

**15. Scheduled contract check** — Add the build-tagged check that exercises the live endpoints and the assumptions
the guards depend on, plus the workflow that runs it on a schedule. It must stay out of the normal test command so
that an upstream outage never fails a build.
