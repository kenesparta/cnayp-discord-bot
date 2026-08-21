# Implementation plan: Policy alignment

**Prerequisites:**

- No tooling beyond a text editor; there is no code in this feature.
- A local static server for previewing the pages, for example `python3 -m http.server -d site 8000`.
- Access to the Discord Developer Portal, to paste the new application description by hand.
- **FRs this depends on:** none functionally — FR-0003 is a root of the dependency graph (PRD §8, Part 1). It should
  nevertheless be **applied last in the release**, after FR-0002, so that the README's configuration table can name
  the environment variable FR-0002 introduces and so that the claims are checked against code that exists.

### Phase 1: Policy pages

**1. Privacy Policy** — Apply the six edits to the Privacy Policy: the updated date, the callout's final sentence, the
new paragraph describing the fetched listings, the message-history clarification, the sentence covering the in-memory
list of already-announced events, and the two additions to the log enumeration and the third-party list. Every other
sentence in the document stays exactly as it is. See §4 of the spec for the verbatim replacements.

**2. Terms of Service** — Apply the three edits: the updated date, the widened purpose in section 2 together with the
new bullet disclaiming third-party event information, and the rewritten first paragraph of section 3 that admits the
bot acts on its own. See §4 of the spec.

**3. Landing page** — Extend the lede so that the scheduled event announcements appear alongside the existing three
activities. The callout underneath is deliberately untouched.

### Phase 2: Repository documentation

**4. README** — Update the opening description, the "only dials out to Discord" sentence in the Policy site section
with its drift-trigger list, and the transcribed application metadata. See §4 of the spec for the replacement
description text.

**5. Agent instructions** — Update the equivalent claim in the repository's agent instructions so that the two
documents do not disagree about what the bot connects to, and extend the list of changes that oblige a Privacy Policy
update.

### Phase 3: Verification and release

**6. Review pass** — Re-read both policy pages end to end against the behaviour FR-0002 actually ships, not only the
edited spans, and confirm that no surviving sentence has become false. Preview the pages locally and follow every
internal link.

**7. Portal and tag** — Paste the new description into the Discord Developer Portal, then cut the release tag. The
policy pages deploy on tags while the bot image publishes on every push to the default branch, so an untagged merge
would put the new behaviour in production while the published policy still describes the old one.
