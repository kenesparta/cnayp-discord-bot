# Implementation plan: Scheduled announcement

**Prerequisites:**

- Go 1.26.5; `go.mod` must stay dependency-free.
- `golangci-lint` v2 with the repository's configuration.
- A new environment variable, `DISCORD_EVENTS_CHANNEL_ID`, holding the channel the bot announces in. It belongs in
  `secrets/prod.enc.yaml` alongside the existing values, and the deployment's compose file must pass it through.
- The bot must be a member of the target guild with permission to view and send messages in that channel. No other
  permission is needed, and none should be granted.
- **FRs this depends on:** FR-0001 (implemented). FR-0003 is independent but should be applied after this one, so the
  published policy describes behaviour that exists.

### Phase 1: Posting a message

**1. Message types** — Add the shape of a created message to the Discord package, together with the allowed-mentions
object and the two flags this feature needs. The allowed-mentions helper exists so that no caller can forget it: the
platform's default parses every mention in the text, and the text comes from strangers. See §5 of the spec.

**2. Create message endpoint** — Add the create-message call to the REST client, routed through the existing request
helper so that rate-limit handling and error redaction are inherited rather than reimplemented.

**3. Announcement channel configuration** — Add the channel variable to the configuration package, optional and
validated, following the existing style. An absent value disables the feature; a malformed one fails startup, because
a mistyped channel id is otherwise indistinguishable from silence.

### Phase 2: Deciding what to say

**4. Wall-clock schedule** — Add the pure function that computes the next run instant from a given moment, and the
rule that decides whether a slot is the weekly one. Both are ordinary functions of their arguments so that the whole
schedule can be tested without waiting for anything. See §3 of the spec for why this is not a ticker.

**5. Seen set** — Add the in-memory record of what has already been announced, seeded silently on the first successful
run, replaced wholesale on each later one, and left untouched by a run in which every calendar failed.

### Phase 3: Saying it

**6. Framing and formatting** — Add the bilingual framing strings, the escaping that makes a stranger's title safe to
place in a message, and the two ways a start is rendered: reader-local for the calendar that publishes instants, a
plain date range for the one that publishes bare dates.

**7. Daily message** — Render the newly listed events, capped, with the overflow line and the attribution that the
requirement asks for.

**8. Weekly digest** — Render the seven-day list, the line that says the week is empty when it is, and the horizon
block of flagship conferences, omitted when there are none.

**9. Chunking** — Split whatever was rendered into at most three messages under the platform's content limit, moving
the overflow line to the last one.

### Phase 4: Running it

**10. Announcer** — Add the type that owns the timer, performs a run, and emits exactly one outcome line describing
it. A run that outlasts its own next slot skips that slot rather than starting again immediately. Publishing stops on a
denied or unauthenticated response and does not retry.

**11. Wiring** — Build the announcer in the bot when a channel is configured, and own its goroutine in `Bot.Run`:
cancelled by the same context, awaited before `Run` returns, and deliberately not attached to the gateway's ready
handler. See §3 of the spec for what happens if it is.

**12. Deployment surface** — Pass the new variable through the deployment's compose file and record it in the
repository's configuration table, so that running the published image is enough to turn the feature on.
