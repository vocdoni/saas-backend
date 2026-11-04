# DNI/NIE canonical form and login-hash migration (SP1): design

Status: amended (2026-11-04), awaiting owner approval   Class: architectural
Owner decisions so far:
- 2026-11-03 (umbrella plan §7/§12 + progress.md): letterless DNI/NIE: derive the check letter.
  ~~Derived entries are a warning-level bucket the organizer must explicitly confirm.~~
  Superseded on 2026-11-04 (below): derivation happens only at hash time; there is no
  derived-letter warning and nothing to confirm.
- 2026-11-03: umbrella plan approved with decisions 1–7 (providers FNMT+idCAT, certificate-only
  fallback, every plan, natural person only, holder display, 443 TCP front, verify-at-redeem).
  None of those are SP1 scope, but SP1's canonical comparison form must equal what cert-check
  hands SP2.
- 2026-11-03: SP1 runs first and alone: it is the riskiest single change (live login hashes).
- **2026-11-04 (this amendment; not to be relitigated): the system must never modify member
  data.** The stored `nationalId` stays exactly as the organizer entered it (trimmed, as on
  `main` before this branch). No canonical rewrite of stored values, no derived check letter
  stored, no `nationalIdOriginal` marker. Normalisation (separators, case, zero padding, and
  deriving a missing check letter) happens only when computing and comparing login hashes, on
  both sides (stored member and typed or certificate value). The voter-auth modal only reports
  who cannot sign in (missing DNI/NIE, letter mismatch, unsupported document, duplicates after
  normalisation); there is no "check letter added" warning class to confirm and no
  confirmation. Letterless DNIs match via the derived letter at comparison time; the owner
  accepted the risk that a digit typo in a letterless DNI can match another real person,
  without warning.

## Problem

The certificate flow (SP2) will identify voters by the DNI/NIE from a qualified certificate,
which cert-check always delivers in one exact shape: 9 characters, uppercase, leading zeros
kept (`Document.Number`, cert-check `internal/identity/document.go:37`). Stored member
`nationalId`s are nothing like that on `main`:

- Storage only trims whitespace: `OrgMember.Normalized()` applies `strings.TrimSpace` to
  `NationalID`, unlike email (lowercased) and birthdate (canonicalised). Per the 2026-11-04
  owner decision this trim-only storage is **correct and must stay**: the member's data belongs
  to the organizer and the system never rewrites it.
- The login hash lowercases both sides (`db.HashAuthTwoFaFields`, `db/types.go:563`), so
  matching is case-insensitive but separator- and padding-sensitive: stored `12.345.678-Z` can
  never match a certificate's `12345678Z`, and stored `2345678T` (7 digits) can never match
  `02345678T`.
- Organizers' spreadsheets really contain `12.345.678-Z`, `2345678T`, letterless `12345678`,
  and wrong letters; nothing validates or reports any of that at import or at census
  validation (`POST /processes/census/validation`).

Without one canonical form applied symmetrically **in the login-hash input** — and a migration
that recomputes the already-stored participant hashes the same way — SP2's certificate matching
would silently exclude every member whose ID was imported in any non-canonical variant.

## Goals

1. One canonical DNI/NIE comparison form, byte-identical to cert-check's
   `Identity.Document.Number`, produced by a single function `internal.NormalizeNationalID`,
   deriving a missing check letter (owner ruling: letterless IDs must work).
2. **Stored member data is never modified.** `nationalId` is stored trimmed, exactly as
   entered, as on `main`. The canonical form exists only inside hash computation and
   comparison.
3. The canonical form feeds the login hash on both sides — the stored member when participant
   hashes are (re)computed, and the typed or certificate value at `auth/0` — so a voter typing
   `12.345.678-Z`, ` 12345678 z ` or letterless `12345678` matches a member stored under any
   canonicalisable variant of the same DNI, and SP2's certificate value matches them all.
4. A migration recomputes the stored census-participant login hashes (and only those: it never
   writes an `orgMembers` document) with collision detection.
5. Import and census validation report, as structured non-blocking warnings, exactly who could
   not sign in: missing, letter mismatch, unsupported document, invalid, and duplicates after
   normalisation. No derived-letter class: a letterless DNI signs in fine and is not a warning.
6. SP2 can ask "has the nationalId hash migration run?" (publish guard input) through a
   db-level check.

## Non-goals

- The cert-check TLS listener, nonce hosts, transaction store, redeem endpoint, census
  `authMethod: "certificate"`, and the publish-time rejection itself: SP2. SP1 only provides
  the migration-applied check SP2 will call.
- Blocking anything new at import or validation. Existing blocking semantics (duplicate login
  hashes, missing data, `ErrPasswordNeedsLookupField`) stay; every new signal is warning-level.
  (Note §4: a pair whose values normalise to the same canonical form now genuinely collides on
  login hashes, so **building a census** over such a pair hits the existing duplicate block —
  that is the unchanged unique-hash rule doing its job, not a new block.)
- UI, copy, i18n: SP4 (it consumes the warning shape defined here). SP4 no longer includes a
  derived-letter confirmation gate (owner decision 2026-11-04).
- A TypeScript mirror of the normaliser (SP4 import-time hints): out of this repo. Appendix B
  of the umbrella plan is the shared vector table both sides test against.
- No new plan gating (owner decision 3: every plan), no new feature flags.

## Current state

The branch `feat/nationalid-normalization` @ 7568326 implements the **superseded** 2026-11-03
design (canonical storage + `nationalIdOriginal` marker + derived-letter warnings). The rework
delta against it is specified in
`.delivery/2026-11-03-nationalid-normalization/rework-plan.md`. What stands on the branch and
is unaffected by the amendment:

- `internal.NormalizeNationalID` (`internal/nationalid.go`, commit 046d74e): classifier and
  canonicaliser, Appendix B vectors, fuzz test. Unchanged by this amendment — only *where its
  output is used* changes.
- The migration-framework lock renewal (`migrations.RenewLock`, `migrations/lock.go:20`;
  `db/migrations.go` renewer wiring, commit 4b6f8ce).
- `MongoStorage.MigrationApplied` (`db/migrations.go:246`, commit 8a45364).
- The warnings plumbing of import and census validation (commits 21666e1, 205aa33): the
  `NationalIDWarning` wire type, `ValidateProcessCensusResponse` 200 JSON body,
  `AddMembersResponse`/`db.Job`/`JobResponse` warning fields and the anonymous-poll nulling —
  minus the `derived_letter` class and the `original`/`derived` fields (§4).
- The admin participant lookup `$in` (commit 6958057, `api/processes_admin.go:113`).
- The `auth/0` exact-first lookup idea (commit 0ef30bc) survives with a reworked mechanism
  (§3).

What the amendment removes or relocates (branch state in parentheses):

- Canonical storage: `OrgMember.Normalized()` canonicalises `NationalID` (`db/types.go:443`,
  line 448) → returns to `strings.TrimSpace` only.
- `OrgMember.NationalIDOriginal` (`db/types.go:358-362`), its `applyTo` clearing
  (`db/types.go:398-402`), its setting in `prepareOrgMember` (`db/org_members.go:282-283`),
  its projection (`db/org_members_groups.go:584`), its API exposure
  (`api/apicommon/types.go:1114-1116,1293`): all removed.
- Migration 0034 writes `orgMembers` documents (`migrations/0034_normalize_national_ids.go:196-204`)
  → never writes them (§5).

Hash plumbing facts the amended design builds on (verified at 7568326):

- Every participant login hash in `db` — writes (`db/census_participant.go:572`,
  `db/org_members.go:639`), the login lookup (`CensusParticipantByLoginHash`,
  `db/census_participant.go:169` → `calculateParticipantHashesBson`), and the blocking
  duplicate detection (`classifyCensusMembers`, `db/census_missing_data.go:151`) — funnels
  through **one** function, `calculateParticipantHashes` (`db/census_participant.go:874`).
- Every migration/repair-side hash funnels through two sibling builders over raw bson docs:
  `recomputeParticipantHashes` (`migrations/0015_normalize_member_emails.go:30`, used by 0015
  and 0034) and `computeParticipantHashes` (`migrations/repair_login_hashes.go:417`, used by
  `RepairLoginHashes`/`scripts/repairlogins`). Both call `hashMemberFields`
  (`migrations/login_hash.go:46`), the byte-for-byte mirror of `db.HashAuthTwoFaFields`
  guarded by `db.TestRepairMatchesCanonicalHash` (`db/repair_login_hashes_test.go:419`).
- `auth/0` builds an `inputMember` from the request, `.Normalized()`s it and resolves the
  participant via `participantByLogin` (`csp/handlers/auth.go:207,410`).
- Migration ledger: one `MigrationRecord{Version, AppliedAt}` per applied migration — **no
  name**, so renaming an unshipped migration is safe; `RunMigrationsUp` compares only the
  highest applied version. Registered migrations reach 0033 on `main` and `origin/lts`; this
  migration is **0034** (never 0016; re-verify the number on rebase).
- cert-check (read-only clone, private module — **not imported**): `Document.Number` is "9
  characters, uppercase, leading zeros kept"; `ValidDNI` = 8 digits +
  `TRWAGMYFPDXBNJZSQVHLCKE[n%23]`; `ValidNIE` = X/Y/Z + 7 digits + letter, prefix read as
  0/1/2; K/L/M-led personal NIFs are `ErrUnsupportedDocument`.

## Design

### 1. Canonical form and `internal.NormalizeNationalID`

Unchanged from the approved design and from commit 046d74e (`internal/nationalid.go`): the
classifier returns `NationalIDResult{Class, Type, Canonical, Normalized}` with classes
`missing`, `canonical`, `derived_letter`, `letter_mismatch`, `unsupported_document`,
`invalid`; `Normalized` is `Canonical` when one exists (canonical and derived_letter classes)
and the trimmed raw value otherwise. Pipeline: trim; strip interior whitespace/`.`/`-`;
uppercase ASCII; any other rune → `invalid` pass-through; DNI/NIE grammar with zero padding
and mod-23 letter derivation/check (NIE prefix read as 0/1/2); K/L/M NIFs and CIF shapes →
`unsupported_document`. Idempotent, panic-free, no regexps.

What changes is its **role**: `derived_letter` is no longer a storage/warning event. It is an
internal classification meaning "this value participates in canonical hashing via a derived
letter" (and still feeds duplicate grouping, §4). Nothing user-visible is keyed on it.

### 2. Storage stays raw; canonicalisation lives in the participant-hash layer

**Storage.** `OrgMember.Normalized()` (`db/types.go:443`) goes back to
`normalized.NationalID = strings.TrimSpace(normalized.NationalID)` — byte-identical to `main`.
Its godoc changes: for the nationalId, the canonical comparison form is applied in the hash
layer below, not at rest. `prepareOrgMember` keeps validating nothing new for nationalId and
never sets any marker. `OrgMember.NationalIDOriginal`, its `applyTo` handling, its projection
and its `apicommon` exposure are deleted. The system writes a member's `nationalId` only when
the organizer does.

**Hash input.** The canonicalisation is applied exactly where member values become login
hashes, one level **above** the frozen `HashAuthTwoFaFields`/`hashMemberFields` mirror pair,
which stays byte-identical to `main` (its lowercasing untouched — the constraint "never touch
the mirrored hash pair" is preserved, and the old Alternatives objection "both hash functions
grow DNI logic" does not apply):

- `db`: `calculateParticipantHashes` (`db/census_participant.go:874`) is split:
  - `rawParticipantHashes(census, member)` — today's body, hashing the member as given;
  - `calculateParticipantHashes(census, member)` — sets
    `member.NationalID = internal.NormalizeNationalID(member.NationalID).Normalized` (value
    copy) and delegates to `rawParticipantHashes`.
  Because participant writes, the login lookup and the blocking duplicate detection all call
  `calculateParticipantHashes`, every stored-member side of the comparison is canonical with
  this one change, while stored documents stay raw.
- `migrations`: `recomputeParticipantHashes` (`migrations/0015_normalize_member_emails.go:30`)
  and `computeParticipantHashes` (`migrations/repair_login_hashes.go:417`) apply the same
  one-liner to `m.NationalID` before calling `hashMemberFields`. The repair tooling
  (`RepairLoginHashes`, `scripts/repairlogins`) therefore recomputes the same canonical hashes
  the login path matches — on a migrated database it is a no-op, as before. (0015 re-run on a
  fresh/replayed database now also writes canonical nationalId hashes; 0034 then finds them
  already canonical and skips — the end state is identical.)
- Parity: `db.TestRepairMatchesCanonicalHash` already compares the two sides end to end
  (seeded members → repair-computed hashes vs login-lookup hashes). Its matrix is extended
  with **raw-stored variants**: `12.345.678-z` (separators + case), `2345678T` (padding),
  `12345678` (letterless), `12345678z` (lowercase canonical), plus the existing canonical and
  non-DNI rows. Any drift between the db-side and migrations-side canonicalisation points
  fails it.

**Typed side.** `auth/0` keeps building `inputMember` and calling `.Normalized()` (now
trim-only); the canonical hash of the typed value falls out of the lookup calling
`calculateParticipantHashes` (§3). Both sides of the comparison therefore canonicalise in the
same layer, from the same function.

The rate-limit keys (`authAttemptKeys`, `csp/handlers/auth.go:191-197`) call
`HashAuthTwoFaFields` directly on the trimmed input, i.e. they key on what was typed, exactly
as on `main`. Two variants of one DNI spend different budgets; that is `main`'s behaviour and
is acceptable (the budget guards enumeration, not variant counting).

### 3. `auth/0` lookups, and the admin participant lookup

**Canonical lookup (the normal case).** `CensusParticipantByLoginHash`
(`db/census_participant.go:169`) now canonicalises via `calculateParticipantHashesBson` →
`calculateParticipantHashes`. A voter typing `12.345.678-Z`, ` 12345678 z ` or letterless
`12345678` into a census with `authFields: ["nationalId"]` hashes to the same canonical form
as every migrated participant of a member stored under any canonicalisable variant — this is
goal 3, pinned by `TestCSPAuthNationalIDNormalization`. A stored value that is not
canonicalisable (wrong letter `12345678A`, a passport, an arbitrary id) hashes as its trimmed
self on both sides, so those censuses keep matching byte-for-byte as on `main`.

**Exact-first (verbatim) lookup — still needed, mechanism reworked.** After migration 0034,
participant hashes are canonical **except** for collision-skipped participants (§5): a
letterless member `12345678` whose lettered twin `12345678Z` sits in the same census keeps its
old raw hash (recomputing it would collide on the unique index). Without a verbatim lookup,
that member's typed `12345678` would canonical-hash straight to the twin — wrong-person
authentication (their auth-only token, or a 2FA code sent to the twin's contact). So
`participantByLogin` (`csp/handlers/auth.go:207`) keeps the exact-first structure of commit
0ef30bc, reworked for raw hashing:

- `db` gains `CensusParticipantByVerbatimLoginHash(census, member)`: identical to
  `CensusParticipantByLoginHash` but building the filter with `rawParticipantHashes` (no
  nationalId canonicalisation) — i.e. `main`'s hash of the member as given. Exported because
  `csp/handlers` is its (only) caller.
- `verbatimNationalIDLogin` becomes a predicate: the verbatim lookup runs only when the census
  logs in with the nationalId **and** `NormalizeNationalID(input.NationalID).Normalized`
  differs from the trimmed input beyond case (`strings.EqualFold`; a pure case change is no
  rewrite, the hash folds case). No second member struct is built: both lookups take the same
  trimmed `inputMember`, one hashing it raw, one canonical.
- Order and semantics as audited at 0ef30bc: verbatim first; a real lookup error returns; only
  `ErrNotFound` falls through to the canonical lookup. One rate-limit unit covers both lookups
  (the claim is taken once, before them). Migrated members match via the canonical lookup; a
  collision-skipped member keeps matching their own stored value exactly as on `main`; a
  letterless input never resolves to the lettered twin.
- A participant created **after** this change always holds a canonical hash (§2), so the
  verbatim lookup finds nothing for them and the canonical fallback matches: the verbatim path
  only ever resolves pre-migration collision survivors.

This narrow exact-first fallback sits on top of the migration; it is not the "dual lookup
instead of a migration" rejected under Alternatives.

**Admin participant lookup** (`votingProcessParticipantsHandler` → `nationalIDLookupValues`,
`api/processes_admin.go:113-125`, commit 6958057): **kept as implemented**. With raw storage
it matches the trimmed typed value verbatim, plus its canonical form when that differs
(`$in`), so an admin pasting any variant finds a member whose stored value is canonical, and a
verbatim-stored value (letterless collision survivors included) still matches exactly. Known
limitation, documented in the handler: a member stored under a *non-canonical variant*
(`12.345.678-Z`) is found only by typing that stored variant — full variant-equivalence search
would need a Go-side normalised scan and is out of SP1 (owner may commission it in SP4/SP6).
The swagger description is softened accordingly ("matches the value as stored, or by its
canonical DNI/NIE form for members stored canonically").

### 4. Warnings: who cannot sign in, one shape for import and census validation

```go
// db (wire type, like OrgMemberAggregationResults)
type NationalIDWarning struct {
    MemberID string `json:"memberId,omitempty" bson:"memberId,omitempty"` // validation path
    Line     int    `json:"line,omitempty"     bson:"line,omitempty"`     // import path, 1-based
    Class    string `json:"class"              bson:"class"`
}
```

`Class` is one of `missing`, `letter_mismatch`, `unsupported_document`, `invalid`,
`duplicate_after_normalization` — exactly the owner's "who cannot sign in" list. Changes from
the implemented branch: the `derived_letter` class and the `Original`/`Derived` fields are
**removed** (a letterless DNI signs in via the derived letter and is nobody's problem), and
with them the only DNI values the warnings carried — warnings are now IDs/lines and classes
only, PII-free. Warnings never block; all existing blocking behaviour is untouched.

**Census validation** (`POST /processes/census/validation`): computed only when
`census.AuthFields` contains `nationalId` (the certificate census of SP2 will be exactly that;
other censuses get no nationalId warnings).

- `nationalIDWarnings` (`db/nationalid_warnings.go`) is simplified: `add` takes no `original`
  (the projection of `nationalIdOriginal`, `db/org_members_groups.go:584`, is deleted);
  classes `canonical` **and** `derived_letter` emit no warning but still register the member
  under `r.Canonical` for duplicate grouping; the other classes emit their warning as today.
- `duplicate_after_normalization`: members sharing a canonical value each get one entry (both
  of a pair listed, matching the existing `Duplicates` semantics). A letterless/lettered pair
  (`12345678` + `12345678Z`) is the archetype. Consequence of §2 worth stating: in a **new**
  census such a pair now genuinely collides on login hashes, so census build refuses them via
  the existing duplicate block, and validation lists them in the blocking `Duplicates` as well
  as in the warning; a pre-existing migrated pair (collision-skipped, distinct stored hashes)
  appears only as the warning. The warning is the stable signal SP4 renders; it means "these
  people cannot (or could not both) sign in by certificate", with no confirmation attached.
- `missing` is reported by validation (that member cannot sign in with an empty value) and
  **skipped at import** (ruling R8 stands: an empty column is only a problem for a census that
  logs in with it).
- `db.OrgMemberAggregationResults.NationalIDWarnings` stays, so the warnings also ride the
  `400` error data when blocking problems coexist.
- The success path keeps the 200 JSON body:
  `apicommon.ValidateProcessCensusResponse{ NationalIDWarnings []db.NationalIDWarning }`
  (`api/apicommon/voting_processes.go:104`), empty array when clean. This remains the one
  observable change for existing API consumers (newline → JSON), recorded for the
  vocdoni.io / integrator-sdk follow-ups in the PR description per AGENTS.md.

**Import** (`POST /organizations/{orgAddress}/members`): unchanged plumbing from commits
21666e1/205aa33, minus the removed class/fields: line-numbered warnings
(`nationalIDImportWarnings`, `db/nationalid_warnings.go:85`), batch-local
`duplicate_after_normalization`, `AddMembersResponse.NationalIDWarnings` (sync),
`db.Job`/`CompleteJobResults`/`JobResponse` warnings (async), and `jobStatusHandler` nulling
the warnings for anonymous polls in the same branch as `Errors`
(`api/jobs.go:60`) — kept for shape-consistency with `Errors` even though the warnings no
longer carry values.

Single-member add/edit responses carry no warning in SP1, as before (census validation reports
the classes; SP4 may surface them inline).

### 5. Migration 0034: recompute participant login hashes, never touch members

Renamed (file and registration — safe, nothing is shipped and the ledger records only the
version) to `migrations/0034_canonicalize_nationalid_hashes.go`,
`AddMigration(34, "canonicalize_nationalid_login_hashes", ...)`, keeping
`const NationalIDNormalizationVersion = 34`. What survives from commit e3ce624 verbatim: the
keyset-batched iteration (`nationalIDBatchSize = 500`, per-batch 2-minute context via
`context.WithoutCancel`, `RenewLock` first in every batch, abort on a lost lock), the coarse
candidate prefilter (non-empty `nationalId` not shaped `^([0-9]{8}|[XYZ][0-9]{7})[A-Z]$`), the
census cache, the nationalId-census filter (ruling R9), the IDs-and-counters-only logging, and
the no-op `down`.

What changes:

1. **The member write is deleted.** The `orgMembers` collection is read-only to this
   migration (`UpdateOne` on members and the `nationalIdOriginal` `$set` go away). The only
   writes are `censusParticipants` hash `$set`s.
2. **Act condition** per member: `r := internal.NormalizeNationalID(m.NationalID)`; proceed
   only when `r.Canonical != "" && !strings.EqualFold(r.Canonical, m.NationalID)` — i.e. the
   lowercased hash input actually changes (`letter_mismatch`/`unsupported_document`/`invalid`
   values, already-canonical values and pure case variants are skipped; the hash lowercases,
   so a case variant's hash is already canonical).
3. **Per-participant unit.** For each of the member's participants in a census whose auth
   fields include `nationalId`, recompute the hash set with `recomputeParticipantHashes`
   (which now canonicalises, §2 — the migration no longer builds a `canonical` copy of the
   doc). If the participant's stored hashes already equal the recomputed ones, skip (no-op
   guard: makes re-runs and partially-migrated databases cheap and the counters honest).
   Otherwise run the collision `CountDocuments` (other participant of the same census holding
   any recomputed hash); on collision **skip that participant only** (not the whole member —
   with no member write there is nothing to keep consistent across censuses), log member and
   census IDs only, count `skipped`. Otherwise `$set` the hashes + `updatedAt`, count
   `rehashed`.
4. **Counters**: `rehashed`, `skipped` (the `normalized`/`derived` member counters go away
   with the member write).
5. **Idempotent and resumable for free**: member documents never change, so candidates are
   re-derivable on every run; a re-run recomputes the same canonical hashes and the no-op
   guard skips them. No write-order choreography is needed any more (the old
   participants-before-member ordering and its resume test are obsolete).
6. `down` stays a no-op — not because the data is lossy now (members are untouched) but
   because the old hashes are derivable only from the stored values, and
   `scripts/repairlogins` built at the **old** binary already does exactly that (see
   Compatibility: rollback).
7. The framework lock renewal (commit 4b6f8ce) and the pre-deploy candidate-count measurement
   on a production dump (owner/ops action) stand unchanged.

### 6. Migration-applied signal for SP2's publish guard

Unchanged (commit 8a45364): `MongoStorage.MigrationApplied(version int) (bool, error)`
(`db/migrations.go:246`) reports whether the version is in the ledger; SP2's publish guard
calls `ms.MigrationApplied(migrations.NationalIDNormalizationVersion)` and rejects publishing
a certificate census while false. Still needed for the same reason: the ledger compares only
the highest applied version, so a branch merge absorbing 0034 under a higher ledger would skip
it silently — and with raw storage the *only* migrated artifact is the hashes, which nothing
else would catch. The godoc comment is retouched to say "canonicalizes the nationalId login
hashes" instead of "stored nationalIds".

### Alternatives considered

| Option | Why not |
|---|---|
| Canonicalise stored values + `nationalIdOriginal` marker + derived-letter confirmation (the approved 2026-11-03 design, implemented through 7568326) | **Rejected by the owner 2026-11-04**: the system must never modify member data; no stored derivation, no marker, no confirmation. |
| Canonicalise inside `HashAuthTwoFaFields`/`hashMemberFields` themselves | Functionally equivalent, but it thaws the frozen byte-for-byte mirror pair (both would grow DNI logic that can drift) and leaves the verbatim lookup (§3) nothing to call — a third raw hash variant would be needed. The participant-hash layer above them already has a single choke point per package. |
| Dual lookup at auth (try old hash, then canonical) instead of a migration | Leaves two hash dialects live forever; SP2's redeem has only the certificate's canonical value, so letterless/separator members still never match by certificate; doubles every lookup. (§3's exact-first fallback is different: on top of the migration, only for rewritten inputs, only resolving pre-migration collision survivors.) |
| Require the letter (reject letterless) or match on digits only | Ruled out by the owner: requiring locks out the target members; digits-only destroys the letter's typo-detection signal entirely. The derived-letter typo risk is explicitly accepted instead. |
| Keep `derived_letter` as a warning without the confirmation | Owner decision says letterless members are not a reportable problem: they can sign in. Reporting them would reintroduce the "to confirm" bucket under another name. |
| Make nationalId warnings blocking at validate/import | Warnings must not block; organizers with mixed non-DNI data in the column must keep using email/OTP censuses untouched. |
| Put the warnings only in log lines / strings | SP4 needs structured classes; strings would be parsed by the app. |

## Compatibility and rollout

- **Wire changes** (net, vs `main`): `POST /processes/census/validation` 200 body changes from
  a bare newline to a JSON object (`nationalIdWarnings`); `AddMembersResponse`, `db.Job`
  (GET /jobs) gain optional warning fields; the 400 error data gains `nationalIdWarnings`.
  Vs the implemented branch: `NationalIDWarning` **loses** `original`/`derived`,
  `apicommon.OrgMember` **loses** `nationalIdOriginal`, and the `derived_letter` class
  disappears. `make swagger` regenerates the spec; the PR description records the
  vocdoni.io + integrator-sdk impact (gate F3's follow-up list, minus the removed fields).
- **Data migration**: 0034 recomputes `censusParticipants` hashes only (§5), at startup, under
  the existing cross-instance lock, batched and renewing it per batch. `orgMembers` is never
  written. The candidate count is measured on a production dump before the stg/prod deploy
  (owner/ops action).
- **Rolling deploy window**: while old replicas serve a migrated database, a voter whose
  member is stored under a non-canonical variant fails on old replicas when typing *any* form
  (old code hashes the typed value raw; the stored participant hash is canonical) **except**
  the canonical form itself, which old code hashes to the same bytes. Voters of
  canonical-stored members are unaffected. The window is the deploy's duration; same accepted
  pattern as 0015/0025.
- **Old binary on migrated data (rollback by redeploy)**: canonical-typed logins keep
  matching; variant-typed input fails until the new binary returns. If a longer rollback must
  restore variant logins, `scripts/repairlogins` **built at the old version** recomputes raw
  hashes from the (untouched) stored values, exactly undoing 0034. Caveat, documented in the
  migration's godoc: 0034 stays recorded in the ledger, so after such a repair the new binary
  must be brought back with a run of the **new** `scripts/repairlogins` (which canonicalises,
  §2) — the recorded version alone no longer proves canonical hashes. The publish guard (§6)
  cannot detect this case; it guards the skipped-migration merge hazard, not deliberate ops
  reversals.
- **Fresh databases**: migrations run in order at startup; participant hashes are canonical
  from the first insert (§2), so 0034 finds nothing to rewrite.
- **Existing non-certificate `nationalId` censuses**: matching gets strictly more forgiving
  for canonicalisable values and stays byte-identical for everything else. Collision-skipped
  participants keep today's behaviour at `auth/0` via §3's exact-first lookup.
- **Member data**: byte-identical before and after SP1, by construction. Exports, member
  lists and the admin UI show exactly what the organizer entered.
- **Ordering with SP2**: SP1 merges and migrates before any certificate census can exist
  (`authMethod: "certificate"` does not parse yet); the publish guard covers the
  skipped-migration deploy hazard.

## Failure modes and security

- **Migration collision (letterless vs lettered twin)**: members `12345678` and `12345678Z`
  in one `nationalId` census collide after canonical hashing. 0034 skips the letterless
  participant (keeps its raw hash) and logs IDs. §3's exact-first lookup keeps each voter
  matching their own member — the letterless input hashes verbatim first and finds the skipped
  participant, never the twin. Validation reports the pair as
  `duplicate_after_normalization`. Note for SP2: certificate redeem only has the canonical
  value, so a collision-skipped letterless member cannot match by certificate; the warning is
  how the organizer learns that.
- **Derived-letter typo admits the wrong real person**: a digit typo in a letterless DNI
  (`12345679` for `12345678`) canonical-hashes to a different, possibly real, person
  (`12345679S`), and matches them at login or certificate redeem. **Owner-accepted
  (2026-11-04), explicitly without warning**: no derivation marker, no derived-letter warning,
  no confirmation. SP1 ships no mitigations; the Appendix B typo-neighbour vector stays in the
  normaliser's table test as documentation of the behaviour.
- **Untrusted input**: `NormalizeNationalID` takes arbitrary organizer/voter strings; the fuzz
  test asserts no panics, idempotency, and that canonical outputs re-validate. No regexp
  backtracking hazards (hand-rolled scans).
- **PII**: warnings now carry no nationalId values at all (classes + IDs/lines only).
  Migration and warning code log member/census IDs and counters only — never a value (stricter
  than 0015, which logs emails). The anonymous-jobs nulling (`api/jobs.go:60`) stays as
  defence in depth.
- **Partial migration failure**: an error aborts `RunMigrationsUp`, the version record is not
  written, startup fails loudly, and the rerun is safe (member docs unchanged → candidates
  re-derivable; per-participant no-op guard skips already-written hashes).
- **Concurrency**: the migration lock serialises 0034 across replicas for as long as it runs
  (per-batch renewal, abort on loss). The per-participant collision check is read-before-write
  under that lock; live writers on old replicas can still insert a colliding hash between
  check and write — the unique index then fails the migration and startup loudly, and the
  rerun is safe. New-code writers produce canonical hashes 0034's no-op guard skips.

## Acceptance criteria

Every criterion names its test; Docker-gated suites are marked (D).

- [ ] AC1: `internal.NormalizeNationalID` passes the full Appendix B vector table (R1
  letters), including the 23-letter sweep, padding, NIE prefixes, the typo-neighbour vector
  and Unicode lookalikes (as `\u` escapes, gate F2) —
  `go test -run TestNormalizeNationalID ./internal/`. Unchanged from the branch.
- [ ] AC2: idempotency, canonical-revalidation and no-panic properties hold —
  `FuzzNormalizeNationalID` seeds in `go test ./internal/`, plus
  `go test -fuzz=FuzzNormalizeNationalID -fuzztime=30s ./internal/` locally. Unchanged.
- [ ] AC3 (D): storage never rewrites: members created/updated with `12.345.678-z`,
  ` 12345678 `, `12345678A` (and an update echoing the same value) are stored exactly trimmed,
  and no stored document or API response carries `nationalIdOriginal` —
  `go test -run TestPrepareOrgMemberNationalID ./db/` (rewritten in
  `db/org_members_nationalid_test.go`).
- [ ] AC4 (D): hash-layer equality: a member stored as `2.345.678-t` is found by
  `CensusParticipantByLoginHash` for input `02345678T` and vice versa; a letterless-stored
  member is found from the lettered input and the letterless input —
  `go test -run TestCensusParticipantNationalIDVariants ./db/`.
- [ ] AC5 (D): `auth/0` with typed `12.345.678-Z`, ` 12345678 z ` and letterless `12345678`
  authenticates against a member stored as `12.345.678-Z` (raw) in a `nationalId` census,
  consuming one rate-limit unit per attempt; a non-canonicalisable stored value (`12345678A`)
  still matches exactly its identical typed input and nothing else —
  `go test -run TestCSPAuthNationalIDNormalization ./api/`.
- [ ] AC6 (D): migration 0034 recomputes the participant hashes of variant-stored members in
  nationalId censuses (the real login lookup then resolves them), leaves `orgMembers`
  documents byte-identical (asserted on the full decoded docs), skips non-canonicalisable and
  case-only-variant values, and a second run writes nothing (no-op guard) —
  `go test -run TestMigration0034NormalizeNationalIDs ./db/`
  (`db/migration_normalize_national_ids_test.go`, rewritten).
- [ ] AC7 (D): the collision case: letterless/lettered twins (`12345678`/`12345678Z`) in one
  census → the letterless participant's hash is skipped and logged (IDs only), the twin's
  other censuses still migrate (per-participant unit), validation reports
  `duplicate_after_normalization` for both — `go test -run TestMigration0034Collision ./db/`;
  and at `auth/0` each member authenticates as themselves (exact-first lookup), the letterless
  input never resolving to the twin —
  `go test -run TestCSPAuthNationalIDCollisionSkipped ./api/`.
- [ ] AC8 (D): db/migrations hash parity holds with raw-stored variant members
  (`12.345.678-z`, `2345678T`, letterless `12345678`, lowercase canonical `12345678z`,
  canonical, non-DNI) in the matrix — `go test -run TestRepairMatchesCanonicalHash ./db/`.
- [ ] AC9 (D): census validation returns 200 with `nationalIdWarnings` covering exactly
  `missing`, `letter_mismatch`, `unsupported_document`, `invalid` and
  `duplicate_after_normalization` (both of a pair listed); a letterless member alone yields
  **no** warning; no warning carries `original`/`derived`; warnings ride the 400 data when
  blocking problems coexist; none are emitted when `nationalId` is not an auth field —
  `go test -run TestValidateProcessCensusNationalIDWarnings ./api/`.
- [ ] AC10 (D): import reports line-numbered warnings sync and via `GET /jobs/{jobId}` async
  (through `JobResponse`), including batch-local `duplicate_after_normalization`, skipping
  `missing` and emitting nothing for letterless values; the async test asserts the warnings
  for a Manager/Admin poll and their absence for an anonymous poll of the same job —
  `go test -run TestAddMembersNationalIDWarnings ./api/`.
- [ ] AC11 (D): `MigrationApplied(migrations.NationalIDNormalizationVersion)` is false on a
  ledger without 0034 and true after `RunMigrationsUp` —
  `go test -run TestMigrationApplied ./db/`. Unchanged.
- [ ] AC12 (D): admin participant lookup finds a canonical-stored member from a
  separator-typed value (`12.345.678-Z`) and a verbatim-stored letterless member from that
  exact value — `go test -run TestVotingProcessParticipantsNationalIDLookup ./api/`
  (members seeded raw).
- [ ] AC13: the whole suite, lint and generated spec are green:
  `go test -failfast -timeout=40m ./...` (D), `make lint`, `./scripts/check-qt-patterns.sh`,
  and `make swagger` committed with the PR — the regenerated spec carries no
  `nationalIdOriginal`, no `derived_letter`, no `original`/`derived` warning fields.
- [ ] AC14 (D): the framework lock renewal works (extends `expiresat` for the holder, errors
  on takeover, no-op without a renewer; a long migration is still recorded) —
  `go test -run TestMigrationLockRenewal ./db/`. Unchanged.
- [ ] AC15 (D): 0034 processes more than one batch (`2*500+1` candidates), renews the lock
  once per batch, rehashes every candidate's participants exactly once, never writes
  `orgMembers`, and aborts without writing the batch when the lock was lost —
  `go test -run TestMigration0034Batches ./db/`.

(Former AC16, the `nationalIdOriginal` read-only guarantee, is obsolete: the field no longer
exists anywhere; AC3, AC6 and AC13 pin its absence.)

## Constraints

- Follow repo conventions (AGENTS.md): `fmt.Errorf` with `%w`, lowercase log/error messages,
  structured logging, gofumpt, lll 130, godoc on exported entities, quicktest matchers,
  Conventional Commit titles.
- **The system never writes a member's `nationalId`** (or any derived form of it) except when
  an organizer's own create/update/import carries it. Migration 0034 treats `orgMembers` as
  read-only.
- Never log or persist a nationalId value outside the member document itself. Warnings carry
  classes and IDs/lines only. Migration logs IDs and counters only.
- Do not touch `db.HashAuthTwoFaFields`, `internal.HashLoginFields` or
  `migrations/login_hash.go`: canonicalisation lives one level up, in
  `calculateParticipantHashes` / `recomputeParticipantHashes` / `computeParticipantHashes`,
  so the mirrored hash pair stays frozen.
- Do not import cert-check; its semantics enter only as test vectors (provenance commented).
- Migration number must exceed the highest registered migration on **both** `main` and
  `origin/lts` at merge time (both 33 today — re-verify on rebase); never renumber after it
  has shipped anywhere. (Renaming 0034 is allowed now because nothing is pushed; the ledger
  records only the version.)
- Existing blocking semantics of import, census build and validation are unchanged; every new
  signal in SP1 is warning-level.
- After touching handlers/apicommon types, run `make swagger`; after any change, run
  `make lint` and `go test` (Docker required) before completion.

## Open questions

- Owner, non-blocking (defaulted in §3 to "keep as implemented"): with raw storage, the admin
  participant lookup finds a member from a typed variant only when the member is stored
  canonically; a member stored as `12.345.678-Z` is found only by typing that exact value. Is
  the `$in` (typed + canonical) good enough for SP1, or should a Go-side variant-equivalent
  search be commissioned (SP4/SP6)?
