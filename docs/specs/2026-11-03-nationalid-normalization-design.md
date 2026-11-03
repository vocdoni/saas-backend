# DNI/NIE canonical form and login-hash migration (SP1): design

Status: draft (2026-11-03)   Class: architectural
Owner decisions so far (2026-11-03, umbrella plan §7/§12 + progress.md):
- Letterless DNI/NIE: derive the check letter; derived entries are a warning-level bucket the
  organizer must explicitly confirm (umbrella §7 ruling). Never phrased as success.
- Umbrella plan approved with decisions 1–7 (providers FNMT+idCAT, certificate-only fallback,
  every plan, natural person only, holder display, 443 TCP front, verify-at-redeem). None of
  those are SP1 scope, but SP1's canonical form must equal what cert-check hands SP2.
- SP1 runs first and alone: it is the riskiest single change (live login hashes).

## Problem

The certificate flow (SP2) will identify voters by the DNI/NIE from a qualified certificate,
which cert-check always delivers in one exact shape: 9 characters, uppercase, leading zeros
kept (`Document.Number`, cert-check `internal/identity/document.go:37`). Stored member
`nationalId`s are nothing like that today:

- Storage only trims whitespace: `OrgMember.Normalized()` applies `strings.TrimSpace` to
  `NationalID` (`db/types.go:432`), unlike email (lowercased) and birthdate (canonicalised).
- The login hash lowercases both sides (`db.HashAuthTwoFaFields`, `db/types.go:547-556`,
  via `memberAuthFieldValue`, `db/census_missing_data.go:64`), so matching is case-insensitive
  but separator- and padding-sensitive: stored `55.555.555-B` can never match a certificate's
  `55555555B`, and stored `5555555B` (7 digits) can never match `05555555B`.
- Organizers' spreadsheets really contain `55.555.555-B`, `5555555B`, letterless `55555555`,
  and wrong letters; nothing validates or reports any of that at import
  (`prepareOrgMember`, `db/org_members.go:267-335`, validates email/phone/birthdate only) or
  at census validation (`POST /processes/census/validation`,
  `api/processes_census_validate.go:36`, reports only duplicates/missing/notFound).

Without one canonical form applied at storage, in the login-hash input, and to stored data via
a migration, SP2's certificate matching would silently exclude every member whose ID was
imported in any non-canonical variant.

## Goals

1. One canonical DNI/NIE form, byte-identical to cert-check's `Identity.Document.Number`,
   produced by a single function `internal.NormalizeNationalID`, with the letterless ruling
   (derive, classify as warning) built in.
2. The canonical form is what gets stored for members and what feeds the login hash on both
   sides, so a voter typing `55.555.555-B` into an existing (non-certificate) `nationalId`
   census still matches after the change — the typed input at `auth/0` is normalised through
   the same function.
3. A migration canonicalises every stored `nationalId` and recomputes the affected census
   participant login hashes, with collision detection, following the 0015 pattern.
4. Import and census validation report nationalId problems as structured, non-blocking
   warnings (missing, letter mismatch, not a DNI/NIE, invalid, duplicate after normalisation,
   and derived letter carrying the original and derived value), shaped for SP4's buckets.
5. SP2 can ask "has the nationalId migration run?" (publish guard input) through a db-level
   check.

## Non-goals

- The cert-check TLS listener, nonce hosts, transaction store, redeem endpoint, census
  `authMethod: "certificate"`, and the publish-time rejection itself: SP2. SP1 only provides
  the migration-applied check SP2 will call.
- Blocking anything new. Existing blocking semantics (duplicate login hashes, missing data,
  `ErrPasswordNeedsLookupField`) stay exactly as they are; every new signal is warning-level.
- UI, copy, i18n, the organizer confirmation checkbox: SP4 (it consumes the warning shape
  defined here).
- A TypeScript mirror of the normaliser (SP4 import-time hints): out of this repo. Appendix B
  of the umbrella plan is the shared vector table both sides test against.
- No new plan gating (owner decision 3: every plan), no new feature flags.

## Current state

All verified on `feat/nationalid-normalization` @ 6fae524:

- `OrgMember.NationalID` is stored trimmed only (`db/types.go:427-437`); plain text, indexed
  ascending (`migrations/0004_rename_nationalid.go:32`).
- `nationalId` is already a first-class auth field (`OrgMemberAuthFieldsNationalID`,
  `db/types.go:465`) accepted by `OrgMemberAuthField.UnmarshalJSON` (`db/types.go:442`).
- Login hash: `db.HashAuthTwoFaFields` (`db/types.go:547`) lowercases each auth-field value
  and feeds `internal.HashLoginFields` (`internal/utils.go:58`, sorted, length-prefixed,
  sha256). Participant lookup is `CensusParticipantByLoginHash`
  (`db/census_participant.go:169`) over the unique indexes from migration 0008.
- Both sides already share one normaliser: members are normalised in `prepareOrgMember`
  (`db/org_members.go:274`), and `auth/0` builds an `inputMember` and calls `.Normalized()`
  (`csp/handlers/auth.go:335-344`). Goal 2 therefore falls out of changing `Normalized()`,
  and is pinned by tests, not left implicit.
- Migration prior art: `migrations/0015_normalize_member_emails.go` (canonicalise a member
  field + recompute participant hashes + collision skip + no-op down);
  `migrations/0025_rehash_login_hashes.go` (RepairLoginHashes applied);
  `migrations/login_hash.go` `hashMemberFields` mirrors `db.HashAuthTwoFaFields` byte for
  byte (migrations cannot import db — db imports migrations), guarded by
  `db.TestRepairMatchesCanonicalHash` (`db/repair_login_hashes_test.go:419`);
  `migrations/repair_login_hashes.go` (`computeParticipantHashes`, collision detection,
  reports carry IDs only, never field values) and its CLI `scripts/repairlogins`.
  The `migrations` package already imports `internal` directly (0015 calls
  `internal.NormalizeEmail`), so the new normaliser needs **no** mirror copy.
- Migration ledger: one `MigrationRecord{Version, AppliedAt}` document per applied migration
  (`db/migrations.go:18-21`, insert at `db/migrations.go:131-139`); `RunMigrationsUp` runs at
  startup inside `MongoStorage.init` (`db/helpers.go:44-52`) under a cross-instance lock, and
  compares only the highest applied version (`lastAppliedMigration`, `db/migrations.go:208`).
  Registered migrations go up to **0033** (`migrations/0033_metered_votes_org_index.go`), on
  `origin/lts` too (verified with `git ls-tree origin/lts migrations/`). **The umbrella plan's
  "migration 0016" is stale: 0016 is taken (`0016_organizations_managed_by_index.go`); this
  migration is 0034.**
- Census validation: `validateProcessCensusHandler` (`api/processes_census_validate.go:36`)
  → `CheckGroupMembersFields`/`CheckMembersFields` (`db/org_members_groups.go:377,411`) →
  `aggregateMemberFields` → `classifyCensusMembers` (`db/census_missing_data.go:122`), over a
  cursor projected to the auth/2FA fields (`getGroupMembersFields`,
  `db/org_members_groups.go:497`; `getMembersFields` `:536`; `findMemberFieldsCursor` `:568`).
  Success writes a bare `200` (`HTTPWriteOK`, `api/apicommon/helpers.go:51` — a newline, no
  JSON); failure is `400` with `db.OrgMemberAggregationResults` as error data.
- Import: `addOrganizationMembersHandler` (`api/org_members.go:211`) → `AddBulkOrgMembers`;
  sync returns `apicommon.AddMembersResponse{Added, Errors []string}`
  (`api/apicommon/types.go:1299`); async persists a `db.Job` (`db/types.go:1075`) finished by
  `CompleteJob(jobID, added, errors []string)` (`db/jobs.go:325`). Per-member validation
  errors are line-prefixed strings (`createOrgMemberBulkOperations`, `db/org_members.go:337`).
- Admin participant lookup matches `nationalId` verbatim against the stored value
  (`votingProcessParticipantsHandler`, `api/processes_admin.go:149-180`,
  `OrgMembersByField`, `db/org_members.go:1207`).
- cert-check (read-only clone `~/.cache/vocdoni-app/ext/cert-check` @ be40f07, private
  module — **not imported**): `Document.Number` is "9 characters, uppercase, leading zeros
  kept" (`internal/identity/document.go:37`); `ValidDNI` = 8 digits + `controlLetters[n%23]`
  with `controlLetters = "TRWAGMYFPDXBNJZSQVHLCKE"` (`document.go:42-58`); `ValidNIE` = X/Y/Z
  + 7 digits + letter, prefix read as 0/1/2 (`document.go:60-77`); K/L/M-led personal NIFs are
  `ErrUnsupportedDocument` (`document.go:96-107`).

## Design

### 1. Canonical form and `internal.NormalizeNationalID`

New file `internal/nationalid.go`. The canonical form is exactly cert-check's
`Document.Number` shape, re-implemented locally (a dozen lines of checksum logic; importing
the private cert-check module is explicitly ruled out):

- DNI: `[0-9]{8}` + mod-23 control letter from `TRWAGMYFPDXBNJZSQVHLCKE`.
- NIE: `[XYZ][0-9]{7}` + control letter, computed with the prefix read as 0/1/2.

```go
// internal/nationalid.go
type NationalIDType string  // "DNI" | "NIE" | ""

type NationalIDClass string

const (
    NationalIDMissing             NationalIDClass = "missing"
    NationalIDCanonical           NationalIDClass = "canonical"
    NationalIDDerivedLetter       NationalIDClass = "derived_letter"
    NationalIDLetterMismatch      NationalIDClass = "letter_mismatch"
    NationalIDUnsupportedDocument NationalIDClass = "unsupported_document"
    NationalIDInvalid             NationalIDClass = "invalid"
)

// NationalIDResult classifies a raw nationalId value and carries its canonical form.
type NationalIDResult struct {
    Class     NationalIDClass
    Type      NationalIDType // set for canonical, derived_letter and letter_mismatch
    Canonical string         // 9-char canonical form; set only for canonical and derived_letter
    Normalized string        // what to store and hash: Canonical when set, else TrimSpace(raw)
}

func NormalizeNationalID(raw string) NationalIDResult
```

Pipeline (in order):

1. Trim Unicode whitespace. Empty → `missing` (`Normalized: ""`).
2. Strip interior separators: every Unicode whitespace rune (NBSP included), `.` and `-`.
3. Uppercase ASCII letters.
4. If any remaining rune is outside `[0-9A-Z]` (fullwidth `Ｂ`, accented letters, …) →
   `invalid`, pass-through (Appendix B allows "stripped/folded **or warning**"; we warn —
   no NFKC folding, no guessing).
5. Grammar, longest-match:
   - `^[0-9]{1,8}$` → left-pad to 8 digits, derive the letter → `derived_letter`, DNI.
   - `^[0-9]{1,8}[A-Z]$` → pad the digits, check the letter against the **padded** number →
     `canonical` or `letter_mismatch`, DNI. (A trailing letter outside the control alphabet,
     e.g. `I`, can never match → `letter_mismatch`.)
   - `^[XYZ][0-9]{1,7}$` → pad digits to 7, derive → `derived_letter`, NIE.
   - `^[XYZ][0-9]{1,7}[A-Z]$` → pad, check → `canonical` or `letter_mismatch`, NIE.
   - 9 characters, first letter `K`/`L`/`M` + 7 digits + letter (other personal NIF), or any
     other leading letter + 7 digits + `[0-9A-Z]` (CIF shape) → `unsupported_document`.
   - Anything else (9+ digits, mixed garbage) → `invalid`.
6. For `letter_mismatch`, `unsupported_document` and `invalid`: `Canonical` is empty and
   `Normalized` is the **trimmed raw input**, unchanged. This preserves
   `OrgMember.Normalized()`'s documented contract ("invalid values are passed through in
   their trimmed form", `db/types.go:423-426`) and keeps every existing non-DNI census
   (passports, arbitrary IDs in the nationalId column) matching exactly as today.

Properties (tested): idempotent (`NormalizeNationalID(r.Normalized)` yields the same result);
every `Canonical` output re-validates as `canonical`; the full 23-letter sweep of
`12345678?` yields `canonical` exactly for `12345678Z` (= letter for 12345678) and
`letter_mismatch` for the other 22.

### 2. Storage, the derived-letter marker, and the hash input

- `OrgMember` gains one field (`db/types.go:337` block):
  `NationalIDOriginal string \`json:"nationalIdOriginal,omitempty" bson:"nationalIdOriginal,omitempty"\``
  — the as-imported value whenever the stored canonical letter was **derived** rather than
  supplied. Empty means the letter was supplied (or the value is not a DNI/NIE). This is what
  lets census validation show the "was 55555565 — will match 55555565X" diff months after
  import, when the original would otherwise be gone.
- `OrgMember.Normalized()` (`db/types.go:427`) sets
  `NationalID = internal.NormalizeNationalID(m.NationalID).Normalized`. Nothing else changes:
  the hash functions (`HashAuthTwoFaFields`, `hashMemberFields`) stay byte-identical — they
  keep lowercasing the (now canonical) value, so both the stored and the typed side hash the
  lowercase canonical form.
- `prepareOrgMember` (`db/org_members.go:267`) classifies the incoming value once; when the
  class is `derived_letter` it sets `NationalIDOriginal` to the trimmed input. It never
  clears the marker (so re-saving a member without touching the ID preserves it).
- `OrgMemberUpdate.applyTo` (`db/types.go:378`) clears `NationalIDOriginal` when
  `u.NationalID != nil`: an explicit nationalId update supersedes the marker; prepare then
  re-sets it if the new value is again letterless. A partial update that does not carry
  `nationalId` keeps both the canonical value and the marker.
- `apicommon.OrgMember` (`api/apicommon/types.go:1111` block) and `OrgMemberFromDb` expose
  `nationalIdOriginal`, so SP4 can show the diff in member lists too.

### 3. `auth/0` typed input (existing non-certificate censuses)

No new code: `authFirstStep` already funnels the request through `Normalized()`
(`csp/handlers/auth.go:335-344`), so after this change a voter typing `55.555.555-B`, `
55555555 b `, or letterless `55555555` into a census with `authFields: ["nationalId"]`
hashes to the same canonical form as the migrated stored value and still matches. A stored
value that is not canonicalisable (e.g. wrong letter `55555555A`) passes through trimmed on
both sides, so the voter who types the same wrong value keeps matching exactly as today.
This is stated as a goal and pinned by `TestCSPAuthNationalIDNormalization` because SP1 must
not regress any live census.

Consistency fix in the same spirit: `votingProcessParticipantsHandler`
(`api/processes_admin.go:154-161`) normalises the lookup `value` with
`NormalizeNationalID(...).Normalized` when `field == nationalId`, so admins can find the
migrated (canonical) member by typing any variant.

### 4. Warnings: one shape for import and census validation

```go
// db (wire type, like OrgMemberAggregationResults)
type NationalIDWarning struct {
    MemberID string `json:"memberId,omitempty" bson:"memberId,omitempty"` // validation path
    Line     int    `json:"line,omitempty"     bson:"line,omitempty"`     // import path, 1-based
    Class    string `json:"class"              bson:"class"`
    Original string `json:"original,omitempty" bson:"original,omitempty"` // derived_letter only
    Derived  string `json:"derived,omitempty"  bson:"derived,omitempty"`  // derived_letter only
}
```

`Class` is one of `missing`, `derived_letter`, `letter_mismatch`, `unsupported_document`,
`invalid`, `duplicate_after_normalization`. Warnings never block; all existing blocking
behaviour (unique-login-hash duplicates, missing data) is untouched.

**Census validation** (`POST /processes/census/validation`): computed only when
`census.AuthFields` contains `nationalId` (the certificate census of SP2 will be exactly
that; other censuses get no nationalId warnings).

- The member projection (`getGroupMembersFields`/`getMembersFields`/`findMemberFieldsCursor`,
  `db/org_members_groups.go:497,536,568`) additionally projects `nationalIdOriginal` when
  `nationalId` is an auth field.
- `classifyCensusMembers` (`db/census_missing_data.go:122`) collects, per member: class from
  `NormalizeNationalID(m.NationalID)`; a stored canonical value with a non-empty
  `NationalIDOriginal` reports `derived_letter` with `Original = NationalIDOriginal`,
  `Derived = m.NationalID` (a stored letterless value — possible for migration collision
  skips — reports `derived_letter` live, with `Original` = stored, `Derived` = computed).
  Members sharing a canonical value each get a `duplicate_after_normalization` entry (both
  of a pair listed, matching the existing `Duplicates` semantics).
- `db.OrgMemberAggregationResults` (`db/types.go:572`) gains
  `NationalIDWarnings []NationalIDWarning \`json:"nationalIdWarnings,omitempty"\``,
  populated by `aggregateMemberFields` and so present in the `400` error data as today.
- The success path stops writing a bare newline: it returns `200` with
  `apicommon.ValidateProcessCensusResponse{ NationalIDWarnings []db.NationalIDWarning }`
  (empty array when clean). This is the warnings API shape SP3/SP4 consume; the swagger
  annotations change accordingly (`make swagger`). This is the one observable change for
  existing API consumers; it is recorded for the vocdoni.io / integrator-sdk follow-ups in
  the PR description per AGENTS.md.

**Import** (`POST /organizations/{orgAddress}/members`): the bulk path classifies each
submitted member's nationalId (same function) and emits warnings with `Line` set (1-based
position in the submitted list, same convention as the line-prefixed error strings,
`db/org_members.go:337-345`). `duplicate_after_normalization` at import is scoped to the
submitted batch (two lines normalising to the same canonical value); cross-checks against
already-stored members remain census validation's job. Plumbing:

- `db.BulkOrgMembersJob` (`db/org_members.go:162`) gains
  `NationalIDWarnings []NationalIDWarning`.
- `apicommon.AddMembersResponse` (`api/apicommon/types.go:1299`) gains
  `NationalIDWarnings []db.NationalIDWarning \`json:"nationalIdWarnings,omitempty"\``
  (sync path).
- `db.Job` (`db/types.go:1075`) gains the same field (bson + json), and `CompleteJob`
  (`db/jobs.go:325`) takes the warnings (signature extended; the compiler finds the few
  callers). `GET /jobs/{jobId}` therefore carries them for async imports (job responses
  already serialize `db.Job`; authorization by `OrgAddress` unchanged).

Warnings carry DNI values (original/derived) only in Manager/Admin-authorized responses and
in the org-scoped job document — data the organizer already owns. Logs never carry them
(IDs only; see Failure modes).

### 5. Migration 0034 `normalize_national_ids`

`migrations/0034_normalize_national_ids.go`, registered with
`AddMigration(34, "normalize_national_ids", ...)`. **Not 0016** (taken since long before this
plan); 34 is the next free number on both `main` and `origin/lts` ledgers (both end at 33),
satisfying the forward-merge rule documented in `migrations/repair_login_hashes.go:16-24`.
Exported alongside it: `const NationalIDNormalizationVersion = 34`.

Shape = 0015 verbatim, with these substitutions:

1. Coarse server-side prefilter: members with a non-empty `nationalId` (a cheap regex
   prefilter for values that cannot already be canonical, e.g. not `^[0-9]{8}[A-Z]$` and not
   `^[XYZ][0-9]{7}[A-Z]$`, is an allowed optimisation); the exact check is in Go:
   `r := internal.NormalizeNationalID(m.NationalID)`; skip unless `r.Normalized`
   differs from the stored value (only `canonical` and `derived_letter` classes can differ;
   `letter_mismatch`/`unsupported_document`/`invalid` values are left exactly as stored).
2. For each changing member, recompute all its census participants' hashes with the existing
   `computeParticipantHashes`/`recomputeParticipantHashes`
   (`migrations/repair_login_hashes.go:417`, `migrations/0015_normalize_member_emails.go:30`)
   over the member doc with the canonical nationalId. No new hash code: `hashMemberFields`
   is reused untouched, so `TestRepairMatchesCanonicalHash` keeps guarding parity.
3. Collision check before writing anything (0015's `CountDocuments` over
   `participantID != p, censusId, $or hashes`): any collision skips the **whole member**
   (keeps its old value and hashes, logged with member/census IDs only — unlike 0015, never
   the value — and re-detected on a future run).
4. Write order per member: participants' hashes first, then the member document
   (`$set nationalId`, plus `nationalIdOriginal` when the class was `derived_letter`, plus
   `updatedAt`), so a crash leaves the member re-detectable.
5. Log counters only: `normalized`, `derived`, `skipped`.
6. `down` is a no-op with a comment: the normalisation is lossy (original separators, case
   and padding are unrecoverable), same stance as 0015 and 0025.

Idempotent: canonical values re-normalise to themselves; a second run finds nothing.

### 6. Migration-applied signal for SP2's publish guard

`db` gains:

```go
// MigrationApplied reports whether the migration with the given version has a record in the
// migrations ledger. (Per-version records exist for every applied migration, db/migrations.go:131.)
func (ms *MongoStorage) MigrationApplied(version int) (bool, error)
```

SP2's publish guard calls `ms.MigrationApplied(migrations.NationalIDNormalizationVersion)`
and rejects publishing a certificate census while it is false. SP1 ships only the check (and
its test); the guard, its error code and any API exposure are SP2. The signal matters despite
migrations running at startup because the ledger tracks only the highest applied version
(`db/migrations.go:89`): a branch merge that absorbs 0034 under an already-higher ledger
would skip it silently, and the guard is the backstop that turns that into a visible refusal
instead of a census whose hashes never match.

### Alternatives considered

| Option | Why not |
|---|---|
| Dual lookup at auth (try old hash, then canonical) instead of a migration | Leaves two hash dialects live forever; SP2's redeem has only the certificate's canonical value, so letterless/separator members still never match; doubles every lookup. |
| Canonicalise inside the hash functions only, keep stored values raw | Two sources of truth (display vs hash); `hashMemberFields` and `HashAuthTwoFaFields` would both grow DNI logic (more mirrored code to drift); migration still needed for the hashes anyway. |
| Mirror `NormalizeNationalID` byte-for-byte into `migrations` (umbrella SP1 wording) | Unnecessary: `migrations` already imports `internal` directly (0015 uses `internal.NormalizeEmail`); only `db` functions need mirrors. One shared function beats a guarded copy. |
| Require the letter (reject letterless) or match on digits only | Ruled out by the owner-approved §7 ruling: requiring locks out the target members; digits-only destroys the letter's typo-detection signal entirely. |
| Store the original raw value for every member (not just derived) | Doubles stored PII for no consumer; only the derived-letter diff needs the original (umbrella §7). |
| Make nationalId warnings blocking at validate/import | Umbrella SP1 criteria say warnings must not block; organizers with mixed non-DNI data in the column must keep using email/OTP censuses untouched. |
| Put the warnings only in log lines / strings | SP4 needs structured classes and the original→derived pair; strings would be parsed by the app. |

## Compatibility and rollout

- **Wire changes**: `POST /processes/census/validation` 200 body changes from a bare newline
  to a JSON object (`nationalIdWarnings`); `AddMembersResponse`, `db.Job` (GET /jobs) and
  `apicommon.OrgMember` gain optional fields; the 400 error data gains
  `nationalIdWarnings`. All additive except the 200 body, which SP3 (SDK types) and SP4
  consume; `make swagger` regenerates the spec, and the PR description records the
  vocdoni.io + integrator-sdk impact (suggested follow-ups, per AGENTS.md).
- **Data migration**: 0034 as above, at startup, under the existing cross-instance lock.
- **Rolling deploy window**: while old replicas serve a migrated database, a voter typing a
  separator variant of a previously separator-stored value fails on old replicas (old code
  trims only). The window is the migration's duration; voters typing the canonical or the
  exact previously-stored form keep working. Same accepted pattern as 0015/0025.
- **Old binary on migrated data (rollback by redeploy)**: canonical-typed logins keep
  matching (the hash lowercases); separator-typed input stops matching until the new binary
  returns. `RunMigrationsDown` for 0034 is a no-op (lossy); remediation is fix-forward. The
  repair tooling stays consistent: `scripts/repairlogins` recomputes hashes from stored
  (now canonical) values and would write the same hashes; its whitespace phase
  (`trimmedMemberFields`, `migrations/repair_login_hashes.go:40`) is untouched.
- **Fresh databases**: migrations run in order at startup; `prepareOrgMember` canonicalises
  everything from the first insert, so 0034 finds nothing.
- **Existing non-certificate `nationalId` censuses**: matching gets strictly more forgiving
  for canonicalisable values and stays byte-identical for everything else (see §3). The one
  behavioural seam is the collision case, documented below.
- **Ordering with SP2**: SP1 merges and migrates before any certificate census can exist
  (`authMethod: "certificate"` does not parse yet); the publish guard covers the
  skipped-migration deploy hazard.

## Failure modes and security

- **Migration collision (letterless vs lettered twin)**: members `55555555` and `55555555B`
  in one `nationalId` census collide after derivation. 0034 skips the letterless member
  (keeps value and hashes) and logs IDs. Consequence: that voter's letterless typed input now
  derives the letter client-side of the hash and **matches the other member** (reaching their
  2FA contact, or their auth-only token). This is the pre-existing "two members, one number"
  data error made visible: the skipped pair is logged by the migration, reported as
  `duplicate_after_normalization` by validation, and the lettered member keeps working. The
  collision test pins exactly this.
- **Derived-letter typo admits the wrong real person**: inherent to the owner-approved
  ruling (umbrella §7, risk 7); SP1's job is to make it visible (`derived_letter` warnings
  with the diff, persisted marker); SP4 adds the confirmation gate. Appendix B's
  typo-neighbour vector (`55555565` → `55555565X`) is in the table test.
- **Untrusted input**: `NormalizeNationalID` takes arbitrary organizer/voter strings; the
  fuzz test asserts no panics, idempotency, and that canonical outputs re-validate. No
  regexp backtracking hazards (hand-rolled scans like cert-check's).
- **PII in logs**: migration and warning code log member/census IDs and counters only —
  never a nationalId value (stricter than 0015, which logs emails). Warning values travel
  only in Manager/Admin responses and the org-scoped job document.
- **Partial migration failure**: an error aborts `RunMigrationsUp`, the version record is
  not written, startup fails loudly, and the rerun is safe (idempotent; per-member write
  order makes interrupted members re-detectable).
- **Concurrency**: the existing migration lock (`db/migrations.go:40-61`) serialises 0034
  across replicas; per-member collision checks use the same read-before-write pattern as
  0015, acceptable because the lock excludes concurrent migrators and live writers go
  through `prepareOrgMember`, which now writes canonical values that 0034 would skip.

## Acceptance criteria

- [ ] AC1: `internal.NormalizeNationalID` passes the full Appendix B vector table, including
  the 23-letter sweep, padding, NIE prefixes, the typo-neighbour vector and Unicode
  lookalikes — `go test -run TestNormalizeNationalID ./internal/`.
- [ ] AC2: idempotency, canonical-revalidation and no-panic properties hold under fuzzing —
  `FuzzNormalizeNationalID` seeds pass in `go test ./internal/`, and
  `go test -fuzz=FuzzNormalizeNationalID -fuzztime=30s ./internal/` finds no crasher locally.
- [ ] AC3: storage canonicalises and the marker behaves — `TestPrepareOrgMemberNationalID`
  (derived sets `nationalIdOriginal`; explicit lettered update clears it; an update not
  carrying nationalId preserves it) — `go test -run TestPrepareOrgMemberNationalID ./db/`.
- [ ] AC4: end-to-end hash equality: member stored as `5.555.555-b` is found by
  `CensusParticipantByLoginHash` for input `55555555B` and vice versa (Appendix B last row)
  — `go test -run TestCensusParticipantNationalIDVariants ./db/`.
- [ ] AC5: `auth/0` with typed `55.555.555-B` (and letterless `55555555`) authenticates
  against a canonical-stored member in a `nationalId` census, and an invalid stored value
  still matches its identical typed input —
  `go test -run TestCSPAuthNationalIDNormalization ./api/`.
- [ ] AC6: migration 0034 canonicalises stored values, derives letters with the marker set,
  recomputes participant hashes that the real login lookup then resolves, and is idempotent
  — `go test -run TestMigration0034NormalizeNationalIDs ./db/` (pattern:
  `db/migration_normalize_emails_test.go`).
- [ ] AC7: the collision case: letterless/lettered twins in one census → letterless member
  skipped and logged, lettered member still authenticates, validation reports
  `duplicate_after_normalization` — `go test -run TestMigration0034Collision ./db/`.
- [ ] AC8: migration/db hash parity still holds with canonical and derived nationalId
  members added to the matrix — `go test -run TestRepairMatchesCanonicalHash ./db/`.
- [ ] AC9: census validation returns 200 with per-class `nationalIdWarnings` (derived
  entries carrying original+derived; duplicates listing both members), includes them in the
  400 data when blocking problems coexist, and emits none when `nationalId` is not an auth
  field — `go test -run TestValidateProcessCensusNationalIDWarnings ./api/`.
- [ ] AC10: import reports line-numbered warnings sync and via `GET /jobs/{jobId}` async,
  including batch-local `duplicate_after_normalization` —
  `go test -run TestAddMembersNationalIDWarnings ./api/`.
- [ ] AC11: `MigrationApplied(migrations.NationalIDNormalizationVersion)` is false on a
  ledger without 0034 and true after `RunMigrationsUp` —
  `go test -run TestMigrationApplied ./db/`.
- [ ] AC12: admin participant lookup finds the canonical member from a separator-typed
  value — `go test -run TestVotingProcessParticipantsNationalIDLookup ./api/`.
- [ ] AC13: the whole suite, lint and generated spec are green: `go test -failfast ./...`,
  `make lint`, `./scripts/check-qt-patterns.sh`, and `make swagger` committed with the PR.

## Constraints

- Follow repo conventions (AGENTS.md): `fmt.Errorf` with `%w`, lowercase log/error messages,
  structured logging, gofumpt, lll 130, godoc on exported entities, quicktest matchers (no
  `len()+Equals`), Conventional Commit titles.
- Never log or persist a nationalId value outside the member document, the marker, and the
  Manager/Admin-visible warning payloads. Migration logs IDs and counters only.
- Do not touch `db.HashAuthTwoFaFields`, `internal.HashLoginFields` or
  `migrations/login_hash.go`: canonicalisation happens strictly in `Normalized()` /
  `NormalizeNationalID`, so the mirrored hash pair stays frozen.
- Do not import cert-check; its semantics enter only as test vectors (provenance commented).
- Migration number must exceed the highest registered migration on **both** `main` and
  `origin/lts` at merge time (both 33 today — re-verify on rebase); never renumber after it
  has shipped anywhere.
- Existing blocking semantics of import, census build and validation are unchanged; every
  new signal in SP1 is warning-level.
- After touching handlers/apicommon types, run `make swagger`; after any change, run
  `make lint` and `go test` (Docker required) before completion.

## Open questions

None.
