# DNI/NIE canonical form and login-hash migration (SP1): implementation plan

Spec: docs/specs/2026-11-03-nationalid-normalization-design.md (approved 2026-11-03, 632b0b1; rulings R1-R6 in .delivery/2026-11-03-nationalid-normalization/progress.md are folded in)
Class: architectural
Repos and branches: /Users/ferran/Repos/saas-backend → feat/nationalid-normalization (base main 6fae524; spec commits up to 632b0b1)
Verified: all tasks applied together on a scratch copy of 632b0b1 (2026-11-03) build, vet, pass golangci-lint v2.15.0 with 0 new issues and the qt gate, and pass the full suite with Docker (bar the macOS-only bash 3.2 failure noted in Task 10).

## Global Constraints
- Commit convention: Conventional Commits, `type(scope): lowercase summary` (e.g. `feat(db): ...`). NO `Co-Authored-By` or any other AI attribution trailer, in commits or the PR.
- Never push. Commit with explicit paths only: `git add <new files>` then `git commit -m "..." -- <every path of the task>`. Never `git add -A`, `git add .` or `git commit -a`.
- Build output and scratch copies under `~/.cache/saas-backend/`, never `/tmp`. Set `GOPRIVATE=github.com/vocdoni/*` for every go command.
- Tests that start MongoDB/Voconed containers (packages `db`, `api`, `csp/handlers`) need Docker; each such step says "Docker". `internal` and `migrations` tests need no Docker. The `api` suite is slow (~7 min): always narrow it with `-run`.
- No DNI/NIE value in any log line, error string or metric: log member/census ids and counters only. DNI values travel only in the member document, `nationalIdOriginal`, and warning payloads returned to a Manager/Admin of the org (and the org-scoped job document).
- Existing non-certificate censuses must keep matching (spec §3): a canonicalisable value matches from any variant; any value that is not a valid DNI/NIE (wrong letter, passports, arbitrary ids) is stored and hashed exactly as today (trimmed, unchanged).
- Do not touch `db.HashAuthTwoFaFields`, `internal.HashLoginFields` or `migrations/login_hash.go`: canonicalisation happens only in `internal.NormalizeNationalID` / `OrgMember.Normalized()`. Do not import cert-check.
- Every new signal is warning-level: import, census build and validation keep their blocking semantics exactly.
- Migration 0034 is version 34 because both `main` and `origin/lts` end at 33 today; re-verify with `git ls-tree origin/lts migrations/` before merging and never renumber after it ships.
- Repo conventions (AGENTS.md): `fmt.Errorf` with `%w`, lowercase log/error messages, structured logging, gofumpt, `lll` 130, godoc on exported names, every `switch` with a `default`.
- Quicktest: `qt.HasLen`, `qt.IsNil`, `qt.IsTrue`/`qt.IsFalse` (never `len()`/`== nil`/booleans with `qt.Equals`); `./scripts/check-qt-patterns.sh` must pass.
- After any change to a handler annotation or an `apicommon`/wire type, run `make swagger` (Task 10 does it once for the whole branch; put `$(go env GOPATH)/bin` on `PATH`, see AGENTS.md gotcha). Never edit `docs/swagger.yaml` by hand.

## Review Focus
Line numbers are those the code below produces once every task is applied on top of 632b0b1.
- `csp/handlers/auth.go:200-234` (`participantByLogin`, `verbatimNationalIDLogin`) and the call at `:410`: exact-first lookup only when normalisation rewrote a typed nationalId on a census that logs in with it; a real lookup error must not fall through to the canonical lookup; the failed-attempt claim (`:399`) is taken once, before both lookups; rate-limit keys and hash functions untouched.
- `db/types.go:445` (`Normalized()` now canonicalises the nationalId): this feeds every stored member and every `auth/0` input; must stay byte-identical for non-DNI values.
- `db/org_members.go:278-284` (`prepareOrgMember` marker) and `db/types.go:395-400` (`applyTo` clears it): `nationalIdOriginal` is only ever set by the server.
- `api/jobs.go:51-62` (`jobStatusHandler`): `resp.NationalIDWarnings` is nulled in the same branch as `resp.Errors`, so an anonymous or non-member poll of `GET /jobs/{jobId}` never sees DNI values.
- `migrations/0034_normalize_national_ids.go:125-206` (`normalizeMember`): only `canonical`/`derived_letter` values change; collision `CountDocuments` per census before any write; a collision skips the whole member and logs ids only; participants are written before the member.
- `migrations/0034_normalize_national_ids.go:95-123` (`batch`): per-batch context from `context.WithoutCancel`, lock renewed before the batch reads or writes anything, keyset pagination on `_id`.
- `db/migrations.go:65-82,135-151,163-174` (`migrationRunTimeout`, `renewMigrationLock`, the renewer on the `Up` context, `recordMigration` with its own context): renewal matches `{_id, holder}` and fails on `MatchedCount == 0`; other migrations keep the 10-minute bound.

## Acceptance criteria map
| AC | Task | Test |
|---|---|---|
| AC1 | 1 | `TestNormalizeNationalID` (internal) |
| AC2 | 1 | `FuzzNormalizeNationalID` (internal) |
| AC3 | 2 | `TestPrepareOrgMemberNationalID` (db) |
| AC4 | 2 | `TestCensusParticipantNationalIDVariants` (db) |
| AC5 | 3 | `TestCSPAuthNationalIDNormalization` (api) |
| AC6 | 8 | `TestMigration0034NormalizeNationalIDs` (db) |
| AC7 | 3, 8 | `TestCSPAuthNationalIDCollisionSkipped` (api), `TestMigration0034Collision` (db) |
| AC8 | 2 | `TestRepairMatchesCanonicalHash` (db, matrix extended) |
| AC9 | 5 | `TestValidateProcessCensusNationalIDWarnings` (api), `TestNationalIDWarnings` (db) |
| AC10 | 6 | `TestAddMembersNationalIDWarnings` (api), `TestJobStatusImportErrorsGatedByRole` (api, extended), `TestNationalIDImportWarnings` (db) |
| AC11 | 9 | `TestMigrationApplied` (db) |
| AC12 | 4 | `TestVotingProcessParticipantsNationalIDLookup` (api), `TestOrgMembersByFieldAnyOf` (db) |
| AC13 | 10 | full suites, `make lint`, qt gate, `make swagger` |
| AC14 | 7 | `TestMigrationLockRenewal` (db), `TestRenewLock` (migrations) |
| AC15 | 8 | `TestMigration0034Batches` (db) |
| AC16 | 2 | `TestOrgMemberNationalIDOriginalReadOnly` (api) |

## Task order and parallelism
| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 normaliser | none | internal/nationalid.go, internal/nationalid_test.go | 7 |
| 2 storage + marker + read-only API field | 1 | db/types.go, db/org_members.go, api/apicommon/types.go, db/org_members_nationalid_test.go, db/repair_login_hashes_test.go, api/org_members_nationalid_test.go | 7 |
| 3 auth/0 exact-first lookup | 2 | csp/handlers/auth.go, csp/handlers/auth_nationalid_test.go, api/csp_auth_nationalid_test.go | 7 |
| 4 admin lookup | 2 | db/org_members.go, api/processes_admin.go, db/org_members_nationalid_test.go, api/processes_participants_nationalid_test.go | 7 |
| 5 census validation warnings | 3, 4 | db/nationalid_warnings.go, db/types.go, db/census_missing_data.go, db/org_members_groups.go, api/apicommon/voting_processes.go, api/processes_census_validate.go, db/nationalid_warnings_test.go, api/processes_census_nationalid_test.go | 7 |
| 6 import warnings, sync + async | 5 | db/nationalid_warnings.go, db/org_members.go, db/jobs.go, db/types.go, api/org_members.go, api/apicommon/types.go, api/jobs.go, db/jobs_test.go, api/jobs_test.go, db/nationalid_warnings_test.go, api/org_members_nationalid_warnings_test.go | 7 |
| 7 migration lock renewal | none | migrations/lock.go, migrations/lock_test.go, db/migrations.go, db/migrations_lock_test.go | 1-6 |
| 8 migration 0034 | 1, 2, 5, 7 | migrations/0034_normalize_national_ids.go, db/migration_normalize_national_ids_test.go | none |
| 9 MigrationApplied | 8 | db/migrations.go, db/migration_applied_test.go | none |
| 10 swagger, lint, full suites | 1-9 | docs/swagger.yaml | none |

Tasks 2-6 and 8-9 share files, so run them one after another in the order above. Task 7 touches only `migrations/lock*.go`, `db/migrations.go` and its own test, so it may run alongside 1-6 (it must land before 8; 9 edits `db/migrations.go` after it).

## Tasks

### Task 1: DNI/NIE normaliser
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: internal/nationalid.go, internal/nationalid_test.go
Goal: one canonical DNI/NIE form, byte-identical to cert-check's `Document.Number`, with the letterless ruling built in (AC1, AC2).

Notes: the vector table uses the corrected Appendix B letters (R1: 55555555→K, 05555555→C, 55555565→P, Z7654321H canonical). Idempotency is asserted on `Normalized` (re-normalising a `derived_letter` result yields the same value with class `canonical`, so "the same result" cannot hold for the class). Non-ASCII vectors are written as `\u` escapes on purpose.

Step 1: write the failing test, `internal/nationalid_test.go`:
```go
package internal

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestNormalizeNationalID runs the shared DNI/NIE vector table (umbrella plan Appendix B, letters
// corrected per the SP1 spec AC1). The semantics come from cert-check's internal/identity/document.go
// @ be40f07 (Document.Number, ValidDNI, ValidNIE, ErrUnsupportedDocument); cert-check is not imported.
func TestNormalizeNationalID(t *testing.T) {
	c := qt.New(t)

	for _, tc := range []struct {
		name, in  string
		class     NationalIDClass
		typ       NationalIDType
		canonical string
	}{
		{"canonical dni", "12345678Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"lowercase letter", "12345678z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"dots and dash", "12.345.678-Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"spaces around and inside", " 12 345 678 z ", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"nbsp separator", "12345678\u00a0Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"letterless derives", "12345678", NationalIDDerivedLetter, NationalIDTypeDNI, "12345678Z"},
		{"typo neighbour derives its own letter", "12345679", NationalIDDerivedLetter, NationalIDTypeDNI, "12345679S"},
		{"seven digits padded", "2345678T", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"padded with separators lowercase", "2.345.678-t", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"already padded", "02345678T", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"short letterless padded", "1", NationalIDDerivedLetter, NationalIDTypeDNI, "00000001R"},
		{"55555555 is K", "55555555K", NationalIDCanonical, NationalIDTypeDNI, "55555555K"},
		{"05555555 is C", "5555555C", NationalIDCanonical, NationalIDTypeDNI, "05555555C"},
		{"55555565 is P", "55555565P", NationalIDCanonical, NationalIDTypeDNI, "55555565P"},
		{"wrong letter", "12345678A", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"letter outside the alphabet", "12345678I", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"55555555B is a wrong letter", "55555555B", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"nie x canonical", "X1234567L", NationalIDCanonical, NationalIDTypeNIE, "X1234567L"},
		{"nie y derives", "Y1234567", NationalIDDerivedLetter, NationalIDTypeNIE, "Y1234567X"},
		{"nie z canonical", "Z7654321H", NationalIDCanonical, NationalIDTypeNIE, "Z7654321H"},
		{"nie lowercase with separators", "x-1234567-l", NationalIDCanonical, NationalIDTypeNIE, "X1234567L"},
		{"nie short padded", "X123P", NationalIDCanonical, NationalIDTypeNIE, "X0000123P"},
		{"nie wrong letter", "X1234567A", NationalIDLetterMismatch, NationalIDTypeNIE, ""},
		{"k nif", "K1234567L", NationalIDUnsupportedDocument, "", ""},
		{"m nif", "M1234567A", NationalIDUnsupportedDocument, "", ""},
		{"cif shape", "B12345678", NationalIDUnsupportedDocument, "", ""},
		{"nine digits", "123456789", NationalIDInvalid, "", ""},
		{"nie with eight digits", "X12345678", NationalIDInvalid, "", ""},
		{"bare nie prefix", "Z", NationalIDInvalid, "", ""},
		{"passport", "PAB123456", NationalIDInvalid, "", ""},
		{"legacy test id", "DNI001", NationalIDInvalid, "", ""},
		{"only separators", "-.-", NationalIDInvalid, "", ""},
		{"fullwidth letter", "12345678\uff3a", NationalIDInvalid, "", ""},
		{"fullwidth digits", "\uff11\uff12\uff13\uff14\uff15\uff16\uff17\uff18Z", NationalIDInvalid, "", ""},
		{"cyrillic x lookalike", "\u04251234567L", NationalIDInvalid, "", ""},
		{"accented letter", "12345678\u00c9", NationalIDInvalid, "", ""},
		{"zero width space", "12345678\u200bZ", NationalIDInvalid, "", ""},
		{"empty", "", NationalIDMissing, "", ""},
		{"whitespace only", " \t\u00a0", NationalIDMissing, "", ""},
	} {
		c.Run(tc.name, func(c *qt.C) {
			got := NormalizeNationalID(tc.in)
			c.Assert(got.Class, qt.Equals, tc.class)
			c.Assert(got.Type, qt.Equals, tc.typ)
			c.Assert(got.Canonical, qt.Equals, tc.canonical)
			if tc.canonical != "" {
				c.Assert(got.Normalized, qt.Equals, tc.canonical)
			} else {
				// never rewritten: what a census keyed on it stores and hashes today
				c.Assert(got.Normalized, qt.Equals, strings.TrimSpace(tc.in))
			}
		})
	}

	c.Run("23-letter sweep", func(c *qt.C) {
		for _, letter := range nationalIDControlLetters {
			got := NormalizeNationalID("12345678" + string(letter))
			if letter == 'Z' {
				c.Assert(got.Class, qt.Equals, NationalIDCanonical)
				continue
			}
			c.Assert(got.Class, qt.Equals, NationalIDLetterMismatch, qt.Commentf("letter %c", letter))
		}
	})

	c.Run("certificate value matches every stored variant", func(c *qt.C) {
		// cert-check hands over 02345678T; the member was imported as 2.345.678-t
		c.Assert(NormalizeNationalID("2.345.678-t").Normalized, qt.Equals, NormalizeNationalID("02345678T").Normalized)
		// two members 2345678T and 02345678T share one canonical value (duplicate_after_normalization)
		c.Assert(NormalizeNationalID("2345678T").Canonical, qt.Equals, NormalizeNationalID("02345678T").Canonical)
	})
}

// FuzzNormalizeNationalID checks the properties the storage and login paths rely on: no panic,
// Normalized is idempotent, every canonical form re-validates as canonical, and a value without
// a canonical form is passed through trimmed.
func FuzzNormalizeNationalID(f *testing.F) {
	for _, seed := range []string{
		"12345678Z", "12345678", "12.345.678-z", "2345678T", "X1234567L", "Y1234567", "Z7654321H",
		"12345678A", "K1234567L", "B12345678", "123456789", "PAB123456", "", " ", "\u00a0", "-",
		"12345678\uff3a", "\u0425" + "1234567L", "X", "00000000", "99999999", "Z9999999",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := NormalizeNationalID(raw)
		again := NormalizeNationalID(got.Normalized)
		if again.Normalized != got.Normalized {
			t.Fatalf("not idempotent: %q -> %q -> %q", raw, got.Normalized, again.Normalized)
		}
		if got.Canonical == "" {
			if got.Normalized != strings.TrimSpace(raw) {
				t.Fatalf("%q without a canonical form was rewritten to %q", raw, got.Normalized)
			}
			return
		}
		if len(got.Canonical) != 9 || got.Normalized != got.Canonical {
			t.Fatalf("%q: bad canonical %q (normalized %q)", raw, got.Canonical, got.Normalized)
		}
		if again.Class != NationalIDCanonical || again.Canonical != got.Canonical {
			t.Fatalf("%q: canonical %q re-validates as %s %q", raw, got.Canonical, again.Class, again.Canonical)
		}
	})
}
```
Run: `GOPRIVATE=github.com/vocdoni/* go test -run 'TestNormalizeNationalID|FuzzNormalizeNationalID' ./internal/`
Expected: build failure, `undefined: NormalizeNationalID` (and the other new identifiers).

Step 2: implement, `internal/nationalid.go`:
```go
package internal

import (
	"strings"
	"unicode"
)

// NationalIDType is the kind of Spanish identity document a nationalId holds.
type NationalIDType string

const (
	// NationalIDTypeDNI is a Spanish national identity document: 8 digits and a check letter.
	NationalIDTypeDNI NationalIDType = "DNI"
	// NationalIDTypeNIE is a foreigner identity number: X, Y or Z, 7 digits and a check letter.
	NationalIDTypeNIE NationalIDType = "NIE"
)

// NationalIDClass classifies a raw nationalId value (see NormalizeNationalID).
type NationalIDClass string

const (
	// NationalIDMissing is an empty or whitespace-only value.
	NationalIDMissing NationalIDClass = "missing"
	// NationalIDCanonical is a DNI/NIE whose supplied check letter is right.
	NationalIDCanonical NationalIDClass = "canonical"
	// NationalIDDerivedLetter is a letterless DNI/NIE whose check letter was computed.
	NationalIDDerivedLetter NationalIDClass = "derived_letter"
	// NationalIDLetterMismatch is a DNI/NIE whose supplied check letter is wrong.
	NationalIDLetterMismatch NationalIDClass = "letter_mismatch"
	// NationalIDUnsupportedDocument is a 9-character tax id that is not a DNI/NIE (K/L/M NIFs, CIFs).
	NationalIDUnsupportedDocument NationalIDClass = "unsupported_document"
	// NationalIDInvalid is anything else, passports and arbitrary ids included.
	NationalIDInvalid NationalIDClass = "invalid"
)

// nationalIDControlLetters is the DNI/NIE check-letter table: the letter of a number n is
// nationalIDControlLetters[n%23].
const nationalIDControlLetters = "TRWAGMYFPDXBNJZSQVHLCKE"

// NationalIDResult is the classification of a raw nationalId value and its canonical form.
type NationalIDResult struct {
	Class NationalIDClass
	// Type is set for canonical, derived_letter and letter_mismatch values.
	Type NationalIDType
	// Canonical is the 9-character canonical form, set only for canonical and derived_letter values.
	Canonical string
	// Normalized is what to store and hash: Canonical when set, otherwise the trimmed raw value.
	Normalized string
}

// NormalizeNationalID classifies a raw DNI/NIE and returns its canonical form: 9 characters,
// uppercase, leading zeros kept, exactly what cert-check reports as Document.Number. Whitespace
// (NBSP included), dots and dashes are dropped, ASCII letters uppercased and short numbers
// left-padded; a missing check letter is derived (class derived_letter). A value that is not a
// DNI/NIE (wrong letter, other tax ids, passports, non-ASCII lookalikes) is never rewritten:
// Normalized is its trimmed form, so a census keyed on such values matches exactly as before.
// It never fails and scans by hand, so arbitrary input cannot make it backtrack or panic.
func NormalizeNationalID(raw string) NationalIDResult {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return NationalIDResult{Class: NationalIDMissing}
	}
	compact := make([]byte, 0, len(trimmed))
	for _, r := range trimmed {
		switch {
		case unicode.IsSpace(r), r == '.', r == '-':
			continue
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			compact = append(compact, byte(r))
		case r >= 'a' && r <= 'z':
			compact = append(compact, byte(r-'a'+'A'))
		default:
			// fullwidth digits, accented or Cyrillic lookalikes: warn, never guess
			return NationalIDResult{Class: NationalIDInvalid, Normalized: trimmed}
		}
	}
	return classifyNationalID(string(compact), trimmed)
}

// classifyNationalID applies the DNI/NIE grammar to compact, the separator-free uppercase ASCII
// form of trimmed.
func classifyNationalID(compact, trimmed string) NationalIDResult {
	invalid := NationalIDResult{Class: NationalIDInvalid, Normalized: trimmed}
	if compact == "" {
		return invalid
	}
	switch first := compact[0]; {
	case isASCIIDigit(first):
		digits, letter := splitCheckLetter(compact)
		if len(digits) > 8 || !allASCIIDigits(digits) {
			return invalid
		}
		number := strings.Repeat("0", 8-len(digits)) + digits
		return parsedNationalID{typ: NationalIDTypeDNI, body: number, number: number, letter: letter}.result(trimmed)
	case first == 'X' || first == 'Y' || first == 'Z':
		digits, letter := splitCheckLetter(compact[1:])
		if digits == "" || len(digits) > 7 || !allASCIIDigits(digits) {
			return invalid
		}
		padded := strings.Repeat("0", 7-len(digits)) + digits
		// the NIE prefix reads as a leading digit for the checksum: X=0, Y=1, Z=2
		prefixDigit := string(rune('0' + first - 'X'))
		return parsedNationalID{
			typ: NationalIDTypeNIE, body: string(first) + padded, number: prefixDigit + padded, letter: letter,
		}.result(trimmed)
	case len(compact) == 9 && isASCIIUpper(first) && allASCIIDigits(compact[1:8]):
		// K/L/M personal NIFs and CIF-shaped company ids: tax ids a certificate never carries as a DNI/NIE
		return NationalIDResult{Class: NationalIDUnsupportedDocument, Normalized: trimmed}
	default:
		return invalid
	}
}

// splitCheckLetter splits a trailing check letter off s; letter is 0 when s does not end in one.
func splitCheckLetter(s string) (digits string, letter byte) {
	if s != "" && isASCIIUpper(s[len(s)-1]) {
		return s[:len(s)-1], s[len(s)-1]
	}
	return s, 0
}

// parsedNationalID is a DNI/NIE that passed the grammar: body is its canonical form without the
// check letter, number the 8 digits the letter is computed from, and letter the supplied one (0
// when letterless).
type parsedNationalID struct {
	typ    NationalIDType
	body   string
	number string
	letter byte
}

// result derives or verifies the check letter. trimmed is the raw value, kept on a wrong letter.
func (p parsedNationalID) result(trimmed string) NationalIDResult {
	n := 0
	for i := range len(p.number) {
		n = n*10 + int(p.number[i]-'0')
	}
	expected := nationalIDControlLetters[n%23]
	canonical := p.body + string(rune(expected))
	switch p.letter {
	case 0:
		return NationalIDResult{Class: NationalIDDerivedLetter, Type: p.typ, Canonical: canonical, Normalized: canonical}
	case expected:
		return NationalIDResult{Class: NationalIDCanonical, Type: p.typ, Canonical: canonical, Normalized: canonical}
	default:
		return NationalIDResult{Class: NationalIDLetterMismatch, Type: p.typ, Normalized: trimmed}
	}
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func isASCIIUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

func allASCIIDigits(s string) bool {
	for i := range len(s) {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}
```
Run: `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestNormalizeNationalID|FuzzNormalizeNationalID' -v ./internal/ | grep -c -- '--- PASS'`
Expected: `65` (TestNormalizeNationalID: 1 + 39 vectors + 2 subtests = 42; FuzzNormalizeNationalID: 1 + 22 seeds = 23), and `ok  github.com/vocdoni/saas-backend/internal` without `-v`.
Run: `GOPRIVATE=github.com/vocdoni/* go test -run XXX -fuzz=FuzzNormalizeNationalID -fuzztime=30s ./internal/`
Expected: `PASS` / `ok`, no `testdata/fuzz` directory created (no crasher).
Run: `gofmt -l internal/ && go vet ./internal/`
Expected: no output.

Step 3: commit
`git add internal/nationalid.go internal/nationalid_test.go && git commit -m "feat(internal): add the DNI/NIE nationalId normaliser" -- internal/nationalid.go internal/nationalid_test.go`

Controller action outside this repo (AC1): correct Appendix B of the umbrella plan (`/Users/ferran/.herdr/worktrees/vocdoni-app/certificats-digitals/.delivery/certificats-digitals/umbrella-plan*.md`) with the R1 vectors: `55555555K`, `05555555C`, `55555565P`, `Z7654321H` among the canonical rows, plus the example family listed in AC1.

Interfaces this task provides to later tasks:
`internal.NormalizeNationalID(raw string) internal.NationalIDResult`; `NationalIDResult{Class NationalIDClass; Type NationalIDType; Canonical, Normalized string}`; classes `NationalIDMissing`, `NationalIDCanonical`, `NationalIDDerivedLetter`, `NationalIDLetterMismatch`, `NationalIDUnsupportedDocument`, `NationalIDInvalid` (string values `missing`, `canonical`, `derived_letter`, `letter_mismatch`, `unsupported_document`, `invalid`); types `NationalIDTypeDNI`, `NationalIDTypeNIE`.

### Task 2: canonical storage, the derived-letter marker and the read-only API field
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: db/types.go, db/org_members.go, api/apicommon/types.go, db/org_members_nationalid_test.go, db/repair_login_hashes_test.go, api/org_members_nationalid_test.go
Goal: members are stored with the canonical nationalId and both sides of the login hash go through it; `nationalIdOriginal` records derived letters and is read-only over the API (AC3, AC4, AC8, AC16).

Step 1: write the failing tests.

`db/org_members_nationalid_test.go` (new):
```go
package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

// TestPrepareOrgMemberNationalID pins how a member's nationalId is stored: canonicalized, with the
// imported value kept in NationalIDOriginal only when the check letter was derived, and that marker
// surviving every write that does not carry a new nationalId.
func TestPrepareOrgMemberNationalID(t *testing.T) {
	c := qt.New(t)
	org := &Organization{Address: common.Address{0x42}, Country: "ES"}
	prepare := func(m *OrgMember) *OrgMember {
		member, errs, err := prepareOrgMember(org, m, "salt", time.Now(), backgroundHasher)
		c.Assert(err, qt.IsNil)
		c.Assert(errs, qt.HasLen, 0)
		return member
	}

	for _, tc := range []struct{ in, stored, original string }{
		{"12345678", "12345678Z", "12345678"},
		{" 2345678 ", "02345678T", "2345678"},
		{"y1234567", "Y1234567X", "y1234567"},
		{"12.345.678-z", "12345678Z", ""},
		{"2345678t", "02345678T", ""},
		{"12345678A", "12345678A", ""},
		{" PAB123456 ", "PAB123456", ""},
		{"", "", ""},
	} {
		got := prepare(&OrgMember{NationalID: tc.in})
		c.Assert(got.NationalID, qt.Equals, tc.stored, qt.Commentf("input %q", tc.in))
		c.Assert(got.NationalIDOriginal, qt.Equals, tc.original, qt.Commentf("input %q", tc.in))
	}

	stored := *prepare(&OrgMember{NationalID: "12345678", Name: "Ana"})
	c.Assert(stored.NationalIDOriginal, qt.Equals, "12345678")

	c.Run("an update not carrying nationalId keeps the marker", func(c *qt.C) {
		name := "Ana Maria"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, Name: &name}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "12345678Z")
		c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")
	})
	c.Run("an explicit lettered update clears the marker", func(c *qt.C) {
		lettered := "12345678Z"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &lettered}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "12345678Z")
		c.Assert(got.NationalIDOriginal, qt.Equals, "")
	})
	c.Run("an explicit letterless update records the new value", func(c *qt.C) {
		letterless := "87654321"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &letterless}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "87654321X")
		c.Assert(got.NationalIDOriginal, qt.Equals, "87654321")
	})
	c.Run("clearing the nationalId clears the marker", func(c *qt.C) {
		empty := ""
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &empty}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "")
		c.Assert(got.NationalIDOriginal, qt.Equals, "")
	})
}

// TestCensusParticipantNationalIDVariants pins end-to-end hash equality: a member imported in one
// DNI variant is found by the login lookup from any other variant of the same document, because
// both sides go through OrgMember.Normalized (umbrella plan Appendix B, last row).
func TestCensusParticipantNationalIDVariants(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	// one census per row, so rows sharing a canonical value never meet in a census
	for _, tc := range []struct{ stored, typed string }{
		{"2.345.678-t", "02345678T"},
		{"02345678T", "2.345.678-t"},
		{"2345678T", " 02.345.678 t "},
		{"12345678", "12345678Z"},
		{"12345678Z", "12345678"},
	} {
		memberID, err := testDB.SetOrgMember("salt", &OrgMember{
			OrgAddress: testOrgAddress, NationalID: tc.stored, Weight: 1,
		})
		c.Assert(err, qt.IsNil)
		census := &Census{OrgAddress: testOrgAddress, AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}}
		_, _, err = testDB.PopulateMembersCensus(census, []string{memberID})
		c.Assert(err, qt.IsNil)

		input := (&OrgMember{OrgAddress: testOrgAddress, NationalID: tc.typed}).Normalized()
		found, err := testDB.CensusParticipantByLoginHash(*census, *input)
		c.Assert(err, qt.IsNil, qt.Commentf("stored %q, typed %q", tc.stored, tc.typed))
		c.Assert(found.ParticipantID, qt.Equals, memberID)
	}
}
```

`db/repair_login_hashes_test.go`, in `TestRepairMatchesCanonicalHash`, replace:
```go
		{
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname},
			OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
		},
	}
```
with:
```go
		{
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname},
			OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
		},
		{OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{}},
		{OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}},
	}
	// the last two combos hold a canonical DNI and a letterless one as stored before migration 34:
	// both must hash through the mirrored code exactly as through HashAuthTwoFaFields
	nationalIDs := map[int]string{len(combos) - 2: "12345678Z", len(combos) - 1: "12345678"}
```
and, right after the `member := OrgMember{...}` literal in the seeding loop (the one with `NationalID: "DnI00" + string(rune('1'+i))`), add:
```go
		if id, ok := nationalIDs[i]; ok {
			member.NationalID = id
		}
```

`api/org_members_nationalid_test.go` (new):
```go
package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestOrgMemberNationalIDOriginalReadOnly pins that nationalIdOriginal is read-only over the API: it
// is whatever the server derived when it filled in a DNI check letter, never what a client sends.
func TestOrgMemberNationalIDOriginalReadOnly(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{Email: "letterless@example.com", NationalID: "12345678", NationalIDOriginal: "forged"},
		apicommon.OrgMember{Email: "lettered@example.com", NationalID: "87654321x", NationalIDOriginal: "forged"},
	)
	byEmail := make(map[string]apicommon.OrgMember, len(members))
	for _, m := range members {
		byEmail[m.Email] = m
	}
	letterless, lettered := byEmail["letterless@example.com"], byEmail["lettered@example.com"]
	c.Assert(letterless.NationalID, qt.Equals, "12345678Z")
	c.Assert(letterless.NationalIDOriginal, qt.Equals, "12345678")
	c.Assert(lettered.NationalID, qt.Equals, "87654321X")
	c.Assert(lettered.NationalIDOriginal, qt.Equals, "")

	// an update that does not carry nationalId keeps the derived marker, whatever the body says
	requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token, map[string]any{
		"id": letterless.ID, "name": "Ana", "nationalIdOriginal": "forged",
	}, organizationMembersURL(orgAddress.String()))
	got := getOrgMember(t, token, orgAddress, letterless.ID)
	c.Assert(got.Name, qt.Equals, "Ana")
	c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")

	// a create carrying it with a lettered DNI stores no marker
	created := requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token, map[string]any{
		"email": "created@example.com", "nationalId": "11111111H", "nationalIdOriginal": "forged",
	}, organizationMembersURL(orgAddress.String()))
	got = getOrgMember(t, token, orgAddress, created.ID)
	c.Assert(got.NationalID, qt.Equals, "11111111H")
	c.Assert(got.NationalIDOriginal, qt.Equals, "")
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestPrepareOrgMemberNationalID|TestCensusParticipantNationalIDVariants|TestRepairMatchesCanonicalHash' ./db/`
Expected: build failure, `got.NationalIDOriginal undefined (type *OrgMember has no field or method NationalIDOriginal)`.

Step 2: implement.

`db/types.go`: append the field at the end of `OrgMember` (after `UpdatedAt`, after a blank line, so gofmt does not realign the struct):
```go
	CreatedAt       time.Time      `json:"createdAt" bson:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt" bson:"updatedAt"`

	// NationalIDOriginal is the nationalId as imported when its DNI/NIE check letter was derived
	// rather than supplied (NationalID then holds the derived canonical form), so the organizer can
	// review the derivation. Empty when the letter was supplied or the value is not a DNI/NIE. Only
	// prepareOrgMember sets it, and an explicit nationalId update clears it (OrgMemberUpdate.applyTo).
	NationalIDOriginal string `json:"nationalIdOriginal,omitempty" bson:"nationalIdOriginal,omitempty"`
}
```
In `OrgMemberUpdate.applyTo` replace `	set(&member.NationalID, u.NationalID)` with:
```go
	if u.NationalID != nil {
		member.NationalID = *u.NationalID
		// the stored marker described the replaced value; prepareOrgMember sets it again if the new
		// value is letterless too
		member.NationalIDOriginal = ""
	}
```
In the `Normalized` godoc replace
```go
// login hash reduced to its canonical form: surrounding whitespace trimmed, the
// email lowercased, the birthdate canonicalized to YYYY-MM-DD.
```
with
```go
// login hash reduced to its canonical form: surrounding whitespace trimmed, the
// email lowercased, the birthdate canonicalized to YYYY-MM-DD, and a DNI/NIE
// nationalId canonicalized by internal.NormalizeNationalID (separators dropped,
// uppercased, zero-padded, a missing check letter derived).
```
and in its body replace `	normalized.NationalID = strings.TrimSpace(normalized.NationalID)` with:
```go
	normalized.NationalID = internal.NormalizeNationalID(normalized.NationalID).Normalized
```

`db/org_members.go`, in the `prepareOrgMember` godoc add a bullet after `//   - Normalizing the fields that feed the CSP login hash (OrgMember.Normalized)`:
```go
//   - Recording the imported nationalId when its check letter was derived (NationalIDOriginal)
```
and right after `	member := *m.Normalized()` add:
```go
	// a letterless DNI/NIE was stored with a derived check letter: keep the value as imported, so the
	// organizer can review the derivation (census validation reports it). A value that is not
	// letterless leaves the marker as it is, so re-saving a member keeps it.
	if internal.NormalizeNationalID(m.NationalID).Class == internal.NationalIDDerivedLetter {
		member.NationalIDOriginal = strings.TrimSpace(m.NationalID)
	}
```

`api/apicommon/types.go`, in `OrgMember` replace
```go
	// Member's National ID No
	NationalID string `json:"nationalId,omitempty"`

	// Member's date of birth in format YYYY-MM-DD
```
with
```go
	// Member's National ID No. A Spanish DNI/NIE is stored in its canonical form: 9 characters,
	// uppercase, leading zeros kept, separators dropped, a missing check letter derived.
	NationalID string `json:"nationalId,omitempty"`

	// NationalIDOriginal is the nationalId as imported when its DNI/NIE check letter was derived
	// rather than supplied, so the derivation can be reviewed. Read-only: ignored on writes.
	NationalIDOriginal string `json:"nationalIdOriginal,omitempty"`

	// Member's date of birth in format YYYY-MM-DD
```
(The first `// Member's National ID No` in the file is the one in `OrgMember`; `UpsertOrgMemberRequest` has another with a `*string`: leave that one alone. `ToDB` must not copy `NationalIDOriginal`.) In `OrgMemberFromDb` add, after `Weight: fmt.Sprintf("%d", p.Weight),` and a blank line:
```go
		NationalIDOriginal: p.NationalIDOriginal,
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestPrepareOrgMember|TestCensusParticipantNationalIDVariants|TestRepairMatchesCanonicalHash|TestOrgMemberFieldsTrimmedOnWrite|TestUpsertOrgMember' -v ./db/ | grep -E '^--- (PASS|FAIL)'`
Expected: `--- PASS` for TestPrepareOrgMemberNationalID, TestPrepareOrgMemberHashingBusy, TestCensusParticipantNationalIDVariants, TestRepairMatchesCanonicalHash, TestOrgMemberFieldsTrimmedOnWrite and the six TestUpsertOrgMember* tests; no FAIL.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestOrgMemberNationalIDOriginalReadOnly|TestCSPAuthNormalizesInput|TestCSPAuthMissingData' ./api/`
Expected: `ok  github.com/vocdoni/saas-backend/api`.
Run: `go build ./... && go vet ./db/ ./api/...`
Expected: no output.

Step 3: commit
`git add db/org_members_nationalid_test.go api/org_members_nationalid_test.go && git commit -m "feat(members): store nationalIds in canonical DNI/NIE form" -- db/types.go db/org_members.go api/apicommon/types.go db/org_members_nationalid_test.go db/repair_login_hashes_test.go api/org_members_nationalid_test.go`

Interfaces this task provides to later tasks: `db.OrgMember.NationalIDOriginal` (bson/json `nationalIdOriginal`); `OrgMember.Normalized()` canonicalises the nationalId; `apicommon.OrgMember.NationalIDOriginal` (read-only).

### Task 3: auth/0 exact-first lookup for rewritten nationalIds
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: csp/handlers/auth.go, csp/handlers/auth_nationalid_test.go, api/csp_auth_nationalid_test.go
Goal: a typed nationalId that normalisation rewrote is looked up verbatim first, so a collision-skipped letterless member never resolves to its lettered twin, while every variant still reaches canonical members with one failed-attempt unit (AC5, AC7 auth part).

Step 1: write the failing tests.

`csp/handlers/auth_nationalid_test.go` (new):
```go
package handlers

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// TestVerbatimNationalIDLogin pins when auth/0 tries the typed nationalId before the normalized
// one: only on a census logging in with the nationalId, and only when normalization rewrote it.
func TestVerbatimNationalIDLogin(t *testing.T) {
	c := qt.New(t)
	withID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	withoutID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}}

	for _, tc := range []struct {
		typed    string
		verbatim string // "" when no verbatim lookup runs
	}{
		{"12345678", "12345678"},
		{" 12.345.678-Z ", "12.345.678-Z"},
		{"2345678T", "2345678T"},
		{"12345678Z", ""},
		{" 12345678z ", ""},
		{"12345678A", ""},
		{"12.345.678-A", ""},
		{"PAB123456", ""},
	} {
		input := (&db.OrgMember{NationalID: tc.typed, MemberNumber: "M-1"}).Normalized()
		got := verbatimNationalIDLogin(withID, input, tc.typed)
		if tc.verbatim == "" {
			c.Assert(got, qt.IsNil, qt.Commentf("typed %q", tc.typed))
		} else {
			c.Assert(got, qt.Not(qt.IsNil), qt.Commentf("typed %q", tc.typed))
			c.Assert(got.NationalID, qt.Equals, tc.verbatim)
			// only the nationalId differs from the normalized login
			c.Assert(got.MemberNumber, qt.Equals, input.MemberNumber)
		}
		c.Assert(verbatimNationalIDLogin(withoutID, input, tc.typed), qt.IsNil)
	}
}
```

`api/csp_auth_nationalid_test.go` (new; `authenticatedMember` and `insertRawMember` are reused by Tasks 4 and 5):
```go
package api

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// nationalIDCensus is the auth-only census of the nationalId tests: auth/0 resolves the participant
// and returns a token in one step, and the token names the member it authenticated.
var nationalIDCensus = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}

// authenticatedMember runs auth/0 with the given nationalId and returns the id of the member the
// token was issued to.
func authenticatedMember(t *testing.T, pid, nationalID string) string {
	t.Helper()
	token := postProcessAuth0(t, pid, &handlers.AuthRequest{NationalID: nationalID})
	auth, err := testDB.CSPAuth(token)
	qt.Assert(t, err, qt.IsNil)
	return auth.UserID.String()
}

// insertRawMember stores a member as a database written before nationalId normalization holds it,
// bypassing the write path that would canonicalize it.
func insertRawMember(t *testing.T, orgAddress common.Address, nationalID string) string {
	t.Helper()
	member := &db.OrgMember{
		ID: bson.NewObjectID(), OrgAddress: orgAddress, NationalID: nationalID, Weight: 1, CreatedAt: time.Now(),
	}
	insertLegacyDoc(t, "orgMembers", member)
	return member.ID.Hex()
}

// TestCSPAuthNationalIDNormalization pins that SP1 does not regress live nationalId censuses: any
// variant of a canonicalisable DNI typed at auth/0 matches the canonical stored member, a value
// that is not a DNI keeps matching exactly as before, and a rewritten input costs one unit of the
// failed-attempt budget however many lookups it takes.
func TestCSPAuthNationalIDNormalization(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "N-1", NationalID: "12345678Z"},
		apicommon.OrgMember{MemberNumber: "N-2", NationalID: "12345678A"},
	)
	byNumber := make(map[string]string, len(members))
	for _, m := range members {
		byNumber[m.MemberNumber] = m.ID
	}
	canonical, wrongLetter := byNumber["N-1"], byNumber["N-2"]
	pid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: []string{canonical, wrongLetter}}, 1)

	for _, typed := range []string{"12345678Z", "12.345.678-Z", " 12345678 z ", "12345678", "12 345 678"} {
		c.Assert(authenticatedMember(t, pid, typed), qt.Equals, canonical, qt.Commentf("typed %q", typed))
	}

	// a stored wrong letter is passed through on both sides: the same value, in any case, still
	// matches, and a separator variant fails exactly as it did before normalization
	c.Assert(authenticatedMember(t, pid, "12345678A"), qt.Equals, wrongLetter)
	c.Assert(authenticatedMember(t, pid, " 12345678a"), qt.Equals, wrongLetter)
	c.Assert(postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{NationalID: "12.345.678-A"}).Code,
		qt.Equals, errors.ErrCSPAuthFailed.Code)

	// a rewritten input matching nobody runs two lookups but spends one unit per attempt: the budget
	// runs out after exactly DefaultMaxHourlyAuthFailures attempts, not half as many
	nobody := &handlers.AuthRequest{NationalID: "87.654.321"}
	for range csp.DefaultMaxHourlyAuthFailures {
		c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code, qt.Equals, errors.ErrCSPAuthFailed.Code)
	}
	c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code, qt.Equals, errors.ErrVerificationMaxAttempts.Code)
}

// TestCSPAuthNationalIDCollisionSkipped pins the letterless/lettered twins migration 34 leaves alone
// (12345678 next to 12345678Z in one census): each member keeps authenticating as themselves with
// their own input, and the letterless input never resolves to the lettered twin.
func TestCSPAuthNationalIDCollisionSkipped(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	letterless := insertRawMember(t, orgAddress, "12345678")
	lettered := insertRawMember(t, orgAddress, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: []string{letterless, lettered}}, 1)

	c.Assert(authenticatedMember(t, pid, "12345678"), qt.Equals, letterless)
	c.Assert(authenticatedMember(t, pid, " 12345678 "), qt.Equals, letterless)
	c.Assert(authenticatedMember(t, pid, "12345678Z"), qt.Equals, lettered)
	c.Assert(authenticatedMember(t, pid, "12345678z"), qt.Equals, lettered)
	// a separator variant has no exact holder, so it falls back to the canonical member
	c.Assert(authenticatedMember(t, pid, "12.345.678-Z"), qt.Equals, lettered)
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestVerbatimNationalIDLogin ./csp/handlers/`
Expected: build failure, `undefined: verbatimNationalIDLogin`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestCSPAuthNationalID' -v ./api/`
Expected: `--- PASS: TestCSPAuthNationalIDNormalization` already (Task 2 made both sides canonical; the test pins that this task keeps it so and spends one budget unit per attempt); `--- FAIL: TestCSPAuthNationalIDCollisionSkipped` at `api/csp_auth_nationalid_test.go:98`, `values are not equal`: the letterless input `12345678` authenticated the lettered twin (got the `12345678Z` member's id). Verified on a scratch copy.

Step 2: implement, `csp/handlers/auth.go`. Replace
```go
	// Check the participant is in the census
	censusParticipant, err := c.mainDB.CensusParticipantByLoginHash(*census, *inputMember)
```
with
```go
	// Check the participant is in the census
	censusParticipant, err := c.participantByLogin(census, inputMember, req.NationalID)
```
and insert before `// memberPasswordMatches reports whether`:
```go
// participantByLogin finds the census participant a step-0 login names. input is the normalized
// login (OrgMember.Normalized) and typedNationalID the nationalId as the request carried it. When
// normalization rewrote that nationalId (a derived check letter, separators, padding), the participant
// holding the hash of the typed value as is goes first, and the normalized one is only tried when
// nobody holds it. A member migration 34 skipped on a collision keeps a letterless value next to its
// lettered twin: the exact lookup keeps each of them matching their own input, so a letterless login
// never resolves to the twin. Every other login is a single lookup of the normalized input.
func (c *CSPHandlers) participantByLogin(census *db.Census, input *db.OrgMember, typedNationalID string,
) (*db.CensusParticipant, error) {
	if verbatim := verbatimNationalIDLogin(census, input, typedNationalID); verbatim != nil {
		participant, err := c.mainDB.CensusParticipantByLoginHash(*census, *verbatim)
		if !errors.Is(err, db.ErrNotFound) {
			return participant, err
		}
	}
	return c.mainDB.CensusParticipantByLoginHash(*census, *input)
}

// verbatimNationalIDLogin returns input with its nationalId put back to the trimmed typed value,
// or nil when the census does not log in with the nationalId or normalization did not change it.
// A mere case change is no rewrite, since the login hash folds case. EqualFold agrees with the
// hash's strings.ToLower here: a value normalization rewrites is ASCII apart from whitespace, and
// one it does not rewrite is passed through byte for byte.
func verbatimNationalIDLogin(census *db.Census, input *db.OrgMember, typedNationalID string) *db.OrgMember {
	if !slices.Contains(census.AuthFields, db.OrgMemberAuthFieldsNationalID) {
		return nil
	}
	typed := strings.TrimSpace(typedNationalID)
	if strings.EqualFold(typed, input.NationalID) {
		return nil
	}
	verbatim := *input
	verbatim.NationalID = typed
	return &verbatim
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestVerbatimNationalIDLogin|TestAuthFirstStep' ./csp/handlers/`
Expected: `ok  github.com/vocdoni/saas-backend/csp/handlers`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestCSPAuthNationalID|TestCSPAuthNormalizesInput|TestCSPAuthAttemptBudget|TestCSPAuthMissingData|TestCSPAuthPassword' -v ./api/ | grep -E '^--- (PASS|FAIL)'`
Expected: `--- PASS` for TestCSPAuthNationalIDNormalization, TestCSPAuthNationalIDCollisionSkipped, TestCSPAuthNormalizesInput, TestCSPAuthAttemptBudget, TestCSPAuthMissingData and TestCSPAuthPassword; no FAIL.

Step 3: commit
`git add csp/handlers/auth_nationalid_test.go api/csp_auth_nationalid_test.go && git commit -m "fix(csp): look up a rewritten nationalId verbatim before its canonical form" -- csp/handlers/auth.go csp/handlers/auth_nationalid_test.go api/csp_auth_nationalid_test.go`

Interfaces this task provides to later tasks: test helpers `authenticatedMember(t, pid, nationalID) string` and `insertRawMember(t, orgAddress, nationalID) string` in package `api`.

### Task 4: admin participant lookup matches canonical and verbatim values
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: db/org_members.go, api/processes_admin.go, db/org_members_nationalid_test.go, api/processes_participants_nationalid_test.go
Goal: `GET /processes/{processId}/participants?field=nationalId` finds canonical members from any variant and verbatim-stored ones from their exact value (AC12).

Step 1: write the failing tests.

Append to `db/org_members_nationalid_test.go`:
```go
// TestOrgMembersByFieldAnyOf pins the []string form of OrgMembersByField: a member matching any of
// the values is returned, and an empty list or value is refused.
func TestOrgMembersByFieldAnyOf(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	a, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "12345678Z"})
	c.Assert(err, qt.IsNil)
	b, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "PAB123456"})
	c.Assert(err, qt.IsNil)

	got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID,
		[]string{"12.345.678-Z", "12345678Z", "PAB123456"})
	c.Assert(err, qt.IsNil)
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID.Hex())
	}
	c.Assert(ids, qt.ContentEquals, []string{a, b})

	for _, bad := range [][]string{{}, {"12345678Z", ""}} {
		_, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID, bad)
		c.Assert(err, qt.ErrorIs, ErrInvalidData)
	}
}
```

`api/processes_participants_nationalid_test.go` (new):
```go
package api

import (
	"net/http"
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestVotingProcessParticipantsNationalIDLookup pins the admin nationalId lookup: a member stored
// canonical is found from any variant, and a value stored verbatim (a member migration 34 skipped
// on a collision) is still found from that exact value.
func TestVotingProcessParticipantsNationalIDLookup(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	imported := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{MemberNumber: "P-1", NationalID: "87.654.321-x"})
	c.Assert(imported, qt.HasLen, 1)
	c.Assert(imported[0].NationalID, qt.Equals, "87654321X")
	canonical := imported[0].ID
	letterless := insertRawMember(t, orgAddress, "12345678")
	lettered := insertRawMember(t, orgAddress, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		MemberIDs:  []string{canonical, letterless, lettered},
	}, 1)

	lookup := func(value string) []string {
		t.Helper()
		resp := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
			"processes", pid, "participants?field="+string(db.OrgMemberLookupFieldNationalID)+"&value="+url.QueryEscape(value))
		ids := make([]string, 0, len(resp.Participants))
		for _, p := range resp.Participants {
			ids = append(ids, p.MemberID)
		}
		return ids
	}

	c.Assert(lookup("87.654.321-X"), qt.DeepEquals, []string{canonical})
	c.Assert(lookup(" 87654321x "), qt.DeepEquals, []string{canonical})
	c.Assert(lookup("87654321X"), qt.DeepEquals, []string{canonical})
	// the letterless value finds the skipped member verbatim, and its canonical twin too
	c.Assert(lookup("12345678"), qt.ContentEquals, []string{letterless, lettered})
	c.Assert(lookup("12345678Z"), qt.DeepEquals, []string{lettered})
	c.Assert(lookup("00000000T"), qt.HasLen, 0)
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestOrgMembersByFieldAnyOf ./db/`
Expected: `--- FAIL: TestOrgMembersByFieldAnyOf`, at the first `c.Assert(err, qt.IsNil)`: the `[]string` value is refused today with `ErrInvalidData`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestVotingProcessParticipantsNationalIDLookup ./api/`
Expected: `--- FAIL`: `lookup("87.654.321-X")` returns `[]string{}`, want the canonical member.

Step 2: implement.

`db/org_members.go`, in `OrgMembersByField`: replace the godoc sentence
```go
// For OrgMemberLookupFieldPhone, the caller must pass an already-hashed value
// (HashedPhone bytes); for the other fields the value is matched verbatim as a
// string. Returns an empty slice (not an error) when there is no match.
```
with
```go
// For OrgMemberLookupFieldPhone, the caller must pass an already-hashed value
// (HashedPhone bytes); for the other fields the value is matched verbatim as a
// string, or a []string matching any of its (non-empty) values. Returns an empty
// slice (not an error) when there is no match.
```
replace
```go
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, ErrInvalidData
		}
	case HashedPhone:
```
with
```go
	match := value
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, ErrInvalidData
		}
	case []string:
		if len(v) == 0 || slices.Contains(v, "") {
			return nil, ErrInvalidData
		}
		match = bson.M{"$in": v}
	case HashedPhone:
```
and in the filter replace `		field.bsonField(): value,` with `		field.bsonField(): match,`.

`api/processes_admin.go`: replace
```go
	// phone is stored hashed, so hash the plaintext before looking up.
	var lookupValue any = value
	if field == db.OrgMemberLookupFieldPhone {
```
with
```go
	// phone is stored hashed, so hash the plaintext before looking up.
	var lookupValue any = value
	if field == db.OrgMemberLookupFieldNationalID {
		lookupValue = nationalIDLookupValues(value)
	}
	if field == db.OrgMemberLookupFieldPhone {
```
insert before `// votingProcessParticipantsHandler godoc`:
```go
// nationalIDLookupValues returns what an admin nationalId lookup matches: the trimmed value, plus
// its canonical DNI/NIE form when that differs, so a member stored canonical is found from any
// variant while a value stored verbatim (e.g. a member migration 34 skipped) still matches exactly.
func nationalIDLookupValues(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value // nothing to normalize: matched verbatim, as before
	}
	if normalized := internal.NormalizeNationalID(trimmed).Normalized; normalized != trimmed {
		return []string{trimmed, normalized}
	}
	return trimmed
}
```
and in its swag annotation replace
```go
//	@Description	pass the plaintext number; it is hashed server-side. Requires Manager/Admin of the
//	@Description	owning organization.
```
with
```go
//	@Description	pass the plaintext number; it is hashed server-side. For `nationalId` a Spanish DNI/NIE
//	@Description	also matches its canonical form, so separator, case and padding variants find the member.
//	@Description	Requires Manager/Admin of the owning organization.
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestOrgMembersByField' ./db/`
Expected: `ok  github.com/vocdoni/saas-backend/db` (TestOrgMembersByField, TestOrgMembersByFieldTrackedSuite, TestOrgMembersByFieldAnyOf pass).
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestVotingProcessParticipantsNationalIDLookup|TestProcessCSP$' ./api/`
Expected: `ok  github.com/vocdoni/saas-backend/api`.

Step 3: commit
`git add api/processes_participants_nationalid_test.go && git commit -m "feat(processes): match nationalId variants in the admin participant lookup" -- db/org_members.go api/processes_admin.go db/org_members_nationalid_test.go api/processes_participants_nationalid_test.go`

Interfaces this task provides to later tasks: `OrgMembersByField` accepts a `[]string` value (`$in`).

### Task 5: nationalId warnings in census validation
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: db/nationalid_warnings.go, db/types.go, db/census_missing_data.go, db/org_members_groups.go, api/apicommon/voting_processes.go, api/processes_census_validate.go, db/nationalid_warnings_test.go, api/processes_census_nationalid_test.go
Goal: `POST /processes/census/validation` reports per-member nationalId warnings in a 200 JSON body and in the 400 data, only when nationalId is an auth field, without changing what blocks (AC9).

Step 1: write the failing tests.

`db/nationalid_warnings_test.go` (new):
```go
package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestNationalIDWarnings pins the warning collector both census validation and import use: one
// entry per value that is not a supplied-letter DNI/NIE, derived letters carrying both values, and
// a duplicate entry for every holder of a shared canonical value.
func TestNationalIDWarnings(t *testing.T) {
	c := qt.New(t)

	empty := newNationalIDWarnings().result()
	c.Assert(empty, qt.IsNotNil)
	c.Assert(empty, qt.HasLen, 0)

	n := newNationalIDWarnings()
	for _, m := range []struct{ id, value, original string }{
		{"a", "12345678Z", "12345678"}, // stored canonical with its import-time marker
		{"b", "12345678", ""},          // stored letterless (a member migration 34 skipped)
		{"c", "12345678A", ""},
		{"d", "K1234567L", ""},
		{"e", "PAB123456", ""},
		{"f", "", ""},
		{"g", "02345678T", ""},
		{"h", "2345678T", ""},
		{"i", "X1234567L", ""},
	} {
		n.add(NationalIDWarning{MemberID: m.id}, m.value, m.original)
	}
	c.Assert(n.result(), qt.DeepEquals, []NationalIDWarning{
		{MemberID: "a", Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: "b", Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: "c", Class: "letter_mismatch"},
		{MemberID: "d", Class: "unsupported_document"},
		{MemberID: "e", Class: "invalid"},
		{MemberID: "f", Class: "missing"},
		{MemberID: "a", Class: NationalIDWarningDuplicate},
		{MemberID: "b", Class: NationalIDWarningDuplicate},
		{MemberID: "g", Class: NationalIDWarningDuplicate},
		{MemberID: "h", Class: NationalIDWarningDuplicate},
	})
}
```

`api/processes_census_nationalid_test.go` (new):
```go
package api

import (
	"encoding/json"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestValidateProcessCensusNationalIDWarnings pins the nationalId warnings of POST
// /processes/census/validation: reported per class in the 200 body and in the 400 data alike, only
// when nationalId is an auth field, and never blocking on their own.
func TestValidateProcessCensusNationalIDWarnings(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	all := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "V-1", NationalID: "12345678"},
		apicommon.OrgMember{MemberNumber: "V-2", NationalID: "X1234567A"},
		apicommon.OrgMember{MemberNumber: "V-3", NationalID: "PAB123456"},
		apicommon.OrgMember{MemberNumber: "V-4", NationalID: "K1234567L"},
		apicommon.OrgMember{MemberNumber: "V-5", NationalID: "02345678T"},
		apicommon.OrgMember{MemberNumber: "V-6", NationalID: "2.345.678-t"},
		apicommon.OrgMember{MemberNumber: "V-7"},
	)
	id := make(map[string]string, len(all))
	for _, m := range all {
		id[m.MemberNumber] = m.ID
	}
	// letterless/lettered twins as migration 34 leaves them after a collision: distinct hashes
	skipped := insertRawMember(t, orgAddress, "87654321")
	twin := insertRawMember(t, orgAddress, "87654321X")

	byNationalID := apicommon.CensusSpec{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	// validate asserts the status code and returns the warnings, plus the blocking duplicates of a 400
	validate := func(code int, spec apicommon.CensusSpec, memberIDs ...string) db.OrgMemberAggregationResults {
		t.Helper()
		spec.MemberIDs = memberIDs
		req := &apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec}
		if code == http.StatusOK {
			resp := requestAndParse[apicommon.ValidateProcessCensusResponse](t, http.MethodPost, token, req,
				"processes", "census", "validation")
			c.Assert(resp.NationalIDWarnings, qt.IsNotNil) // an empty array, never null
			return db.OrgMemberAggregationResults{NationalIDWarnings: resp.NationalIDWarnings}
		}
		apiErr := requestAndParseWithAssertCode[errors.Error](code, t, http.MethodPost, token, req,
			"processes", "census", "validation")
		data, err := json.Marshal(apiErr.Data)
		c.Assert(err, qt.IsNil)
		var results db.OrgMemberAggregationResults
		c.Assert(json.Unmarshal(data, &results), qt.IsNil)
		return results
	}

	c.Run("each class is reported and the census stays valid", func(c *qt.C) {
		got := validate(http.StatusOK, byNationalID, id["V-1"], id["V-2"], id["V-3"], id["V-4"])
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-1"], Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
			{MemberID: id["V-2"], Class: "letter_mismatch"},
			{MemberID: id["V-3"], Class: "invalid"},
			{MemberID: id["V-4"], Class: "unsupported_document"},
		})
	})

	c.Run("no warnings when nationalId is not an auth field", func(c *qt.C) {
		byNumber := apicommon.CensusSpec{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}}
		c.Assert(validate(http.StatusOK, byNumber, id["V-1"], id["V-2"], id["V-3"]).NationalIDWarnings, qt.HasLen, 0)
	})

	c.Run("a canonical pair is both blocking duplicates and warnings", func(c *qt.C) {
		got := validate(http.StatusBadRequest, byNationalID, id["V-5"], id["V-6"])
		c.Assert(got.Duplicates, qt.HasLen, 2)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-5"], Class: db.NationalIDWarningDuplicate},
			{MemberID: id["V-6"], Class: db.NationalIDWarningDuplicate},
		})
	})

	c.Run("a collision-skipped pair is only a warning", func(c *qt.C) {
		got := validate(http.StatusOK, byNationalID, skipped, twin)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: skipped, Class: "derived_letter", Original: "87654321", Derived: "87654321X"},
			{MemberID: skipped, Class: db.NationalIDWarningDuplicate},
			{MemberID: twin, Class: db.NationalIDWarningDuplicate},
		})
	})

	c.Run("missing data still blocks and is reported as missing", func(c *qt.C) {
		got := validate(http.StatusBadRequest, byNationalID, id["V-7"], id["V-1"])
		c.Assert(got.MissingData, qt.HasLen, 1)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-7"], Class: "missing"},
			{MemberID: id["V-1"], Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		})
	})
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestNationalIDWarnings ./db/`
Expected: build failure, `undefined: newNationalIDWarnings`.

Step 2: implement.

`db/nationalid_warnings.go` (new):
```go
package db

import (
	"strings"

	"github.com/vocdoni/saas-backend/internal"
)

// NationalIDWarningDuplicate is the NationalIDWarning class of members (or import lines) whose
// nationalIds normalize to the same canonical DNI/NIE. Every holder of the value gets one.
const NationalIDWarningDuplicate = "duplicate_after_normalization"

// NationalIDWarning is a non-blocking problem with a member's nationalId, reported by census
// validation (MemberID set) and by member import (Line set). Class is an internal.NationalIDClass
// other than canonical, or NationalIDWarningDuplicate. Original and Derived are set only for
// derived_letter: the value as imported and the canonical value with the derived check letter.
// They carry DNI values, so they only travel in Manager/Admin responses, never in logs.
type NationalIDWarning struct {
	MemberID string `json:"memberId,omitempty" bson:"memberId,omitempty"`
	// Line is the 1-based position of the member in the submitted import list.
	Line     int    `json:"line,omitempty" bson:"line,omitempty"`
	Class    string `json:"class" bson:"class"`
	Original string `json:"original,omitempty" bson:"original,omitempty"`
	Derived  string `json:"derived,omitempty" bson:"derived,omitempty"`
}

// nationalIDWarnings collects the nationalId warnings of a set of members, in the order they are
// added, followed by one duplicate entry for every holder of a canonical value held more than once.
type nationalIDWarnings struct {
	warnings []NationalIDWarning
	// holders lists, per canonical value, the identity (MemberID or Line) of each holder
	holders map[string][]NationalIDWarning
	// order is the canonical values in the order they were first seen, so the output is stable
	order []string
}

func newNationalIDWarnings() *nationalIDWarnings {
	return &nationalIDWarnings{holders: make(map[string][]NationalIDWarning)}
}

// add classifies one member's nationalId. id carries the member's identity (MemberID or Line);
// original is its NationalIDOriginal, empty on the import path. A canonical value with a marker
// is reported as derived_letter, like the letterless value it was derived from.
func (n *nationalIDWarnings) add(id NationalIDWarning, value, original string) {
	r := internal.NormalizeNationalID(value)
	if r.Canonical != "" {
		if _, seen := n.holders[r.Canonical]; !seen {
			n.order = append(n.order, r.Canonical)
		}
		n.holders[r.Canonical] = append(n.holders[r.Canonical], id)
	}
	w := id
	switch r.Class {
	case internal.NationalIDCanonical:
		if original == "" {
			return
		}
		w.Class, w.Original, w.Derived = string(internal.NationalIDDerivedLetter), original, r.Canonical
	case internal.NationalIDDerivedLetter:
		w.Class, w.Original, w.Derived = string(r.Class), strings.TrimSpace(value), r.Canonical
	default:
		w.Class = string(r.Class)
	}
	n.warnings = append(n.warnings, w)
}

// result returns the collected warnings, never nil.
func (n *nationalIDWarnings) result() []NationalIDWarning {
	out := append(make([]NationalIDWarning, 0, len(n.warnings)), n.warnings...)
	for _, canonical := range n.order {
		if holders := n.holders[canonical]; len(holders) > 1 {
			for _, id := range holders {
				id.Class = NationalIDWarningDuplicate
				out = append(out, id)
			}
		}
	}
	return out
}
```

`db/types.go`, in `OrgMemberAggregationResults` add after the `NotFound` field:
```go
	// NationalIDWarnings are the non-blocking nationalId problems of the members, reported only when
	// nationalId is an auth field. They never make the census invalid.
	NationalIDWarnings []NationalIDWarning `json:"nationalIdWarnings,omitempty" bson:"nationalIdWarnings,omitempty"`
```

`db/census_missing_data.go`: add `"slices"` to the imports; in `censusMembers` add after `holders`:
```go
	// nationalIDWarnings are the members' nationalId warnings, collected only when the census logs
	// in with the nationalId (nil otherwise). Missing-data members are included.
	nationalIDWarnings []NationalIDWarning
```
in `classifyCensusMembers` replace
```go
	cm := &censusMembers{missing: make([]bson.ObjectID, 0), holders: make(map[loginKey][]string)}
	seen := make(map[string]bool)
	for cur.Next(ctx) {
		m := &OrgMember{}
		if err := cur.Decode(m); err != nil {
			return nil, fmt.Errorf("decoding member: %w", err)
		}
```
with
```go
	cm := &censusMembers{missing: make([]bson.ObjectID, 0), holders: make(map[loginKey][]string)}
	var nationalIDs *nationalIDWarnings
	if slices.Contains(census.AuthFields, OrgMemberAuthFieldsNationalID) {
		nationalIDs = newNationalIDWarnings()
	}
	seen := make(map[string]bool)
	for cur.Next(ctx) {
		m := &OrgMember{}
		if err := cur.Decode(m); err != nil {
			return nil, fmt.Errorf("decoding member: %w", err)
		}
		if nationalIDs != nil {
			nationalIDs.add(NationalIDWarning{MemberID: m.ID.Hex()}, m.NationalID, m.NationalIDOriginal)
		}
```
and after the `cur.Err()` check add:
```go
	if nationalIDs != nil {
		cm.nationalIDWarnings = nationalIDs.result()
	}
```

`db/org_members_groups.go`: in `aggregateMemberFields` replace
```go
		NotFound:    make([]bson.ObjectID, 0),
	}
```
with
```go
		NotFound:    make([]bson.ObjectID, 0),

		NationalIDWarnings: cm.nationalIDWarnings,
	}
```
and in `findMemberFieldsCursor` replace the godoc's second line `// _id/orgAddress plus the requested auth/2FA fields.` with
```go
// _id/orgAddress plus the requested auth/2FA fields, and the nationalId marker (NationalIDOriginal)
// along with the nationalId, which the nationalId warnings report.
```
and the auth-field loop with:
```go
	for _, f := range authFields {
		proj = append(proj, bson.E{Key: authFieldBSONKey(f), Value: 1})
		if f == OrgMemberAuthFieldsNationalID {
			proj = append(proj, bson.E{Key: "nationalIdOriginal", Value: 1})
		}
	}
```

`api/apicommon/voting_processes.go`, after `ValidateProcessCensusRequest`:
```go
// ValidateProcessCensusResponse is the 200 answer of POST /processes/census/validation: the census
// is usable, and these are its non-blocking nationalId warnings (empty unless nationalId is an auth
// field and some member's value is missing, not a valid DNI/NIE, letter-derived or shared).
type ValidateProcessCensusResponse struct {
	NationalIDWarnings []db.NationalIDWarning `json:"nationalIdWarnings"`
}
```

`api/processes_census_validate.go`: in the annotation replace
```go
//	@Description	is a 400. Requires Manager/Admin role of the organization.
//	@Description
```
with
```go
//	@Description	is a 400. Requires Manager/Admin role of the organization.
//	@Description
//	@Description	When `nationalId` is an auth field, both answers carry `nationalIdWarnings` (in the 400 under
//	@Description	`data`): one entry per member whose nationalId is missing, a DNI/NIE with a wrong check
//	@Description	letter (`letter_mismatch`), another tax id (`unsupported_document`), not a DNI/NIE at all
//	@Description	(`invalid`) or letterless with a derived letter (`derived_letter`, with `original` and
//	@Description	`derived`), plus a `duplicate_after_normalization` entry for every member sharing a canonical
//	@Description	DNI/NIE with another. Warnings never make the census invalid.
//	@Description
```
replace `//	@Success		200		{string}	string									"OK"` with
```go
//	@Success		200		{object}	apicommon.ValidateProcessCensusResponse	"Usable census, with its nationalId warnings"
```
and replace the final `	apicommon.HTTPWriteOK(w)` with:
```go
	warnings := results.NationalIDWarnings
	if warnings == nil {
		warnings = []db.NationalIDWarning{}
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ValidateProcessCensusResponse{NationalIDWarnings: warnings})
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestNationalIDWarnings|TestClassifyCensusMembers|TestCensusMembersEmptyValues|TestOrganizationMemberGroup' ./db/`
Expected: `ok  github.com/vocdoni/saas-backend/db`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestValidateProcessCensus' -v ./api/ | grep -E '^--- (PASS|FAIL)'`
Expected: `--- PASS: TestValidateProcessCensus`, `--- PASS: TestValidateProcessCensusNationalIDWarnings` (5 subtests); no FAIL.

Step 3: commit
`git add db/nationalid_warnings.go db/nationalid_warnings_test.go api/processes_census_nationalid_test.go && git commit -m "feat(processes): report nationalId warnings in census validation" -- db/nationalid_warnings.go db/types.go db/census_missing_data.go db/org_members_groups.go api/apicommon/voting_processes.go api/processes_census_validate.go db/nationalid_warnings_test.go api/processes_census_nationalid_test.go`

Interfaces this task provides to later tasks: `db.NationalIDWarning{MemberID string; Line int; Class, Original, Derived string}`; `db.NationalIDWarningDuplicate = "duplicate_after_normalization"`; `newNationalIDWarnings()`, `(*nationalIDWarnings).add(id NationalIDWarning, value, original string)`, `.result() []NationalIDWarning` (never nil); `OrgMemberAggregationResults.NationalIDWarnings`; `apicommon.ValidateProcessCensusResponse`.

### Task 6: nationalId warnings in member import, sync and async
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: db/nationalid_warnings.go, db/org_members.go, db/jobs.go, db/types.go, api/org_members.go, api/apicommon/types.go, api/jobs.go, db/jobs_test.go, api/jobs_test.go, db/nationalid_warnings_test.go, api/org_members_nationalid_warnings_test.go
Goal: `POST /organizations/{orgAddress}/members` reports line-numbered warnings in the sync response and through `GET /jobs/{jobId}` for an async import, never to anonymous or non-member pollers (AC10).

Note: an empty nationalId is not reported at import (only census validation reports `missing`); otherwise every member of an organization that does not use the nationalId would carry a warning.

Step 1: write the failing tests.

Append to `db/nationalid_warnings_test.go`:
```go
// TestNationalIDImportWarnings pins the import flavour: line-numbered, empty values skipped, and
// duplicates checked within the submitted list only.
func TestNationalIDImportWarnings(t *testing.T) {
	c := qt.New(t)
	members := []*OrgMember{
		{NationalID: "12345678"},
		{NationalID: "12.345.678-z"},
		{NationalID: ""},
		{NationalID: "12345678A"},
		{NationalID: "87654321X"},
	}
	c.Assert(nationalIDImportWarnings(members), qt.DeepEquals, []NationalIDWarning{
		{Line: 1, Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{Line: 4, Class: "letter_mismatch"},
		{Line: 1, Class: NationalIDWarningDuplicate},
		{Line: 2, Class: NationalIDWarningDuplicate},
	})
	c.Assert(nationalIDImportWarnings([]*OrgMember{{NationalID: "87654321X"}, {}}), qt.HasLen, 0)
}
```

`api/org_members_nationalid_warnings_test.go` (new):
```go
package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestAddMembersNationalIDWarnings pins the import warnings: line-numbered, non-blocking, in the
// sync response and, for an async import, on GET /jobs/{jobId} for a Manager/Admin only.
func TestAddMembersNationalIDWarnings(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	submit := func(prefix string) *apicommon.AddMembersRequest {
		return &apicommon.AddMembersRequest{Members: []apicommon.OrgMember{
			{MemberNumber: prefix + "1", NationalID: "12345678"},
			{MemberNumber: prefix + "2", NationalID: "12.345.678-z"},
			{MemberNumber: prefix + "3", NationalID: "12345678A"},
			{MemberNumber: prefix + "4", NationalID: "K1234567L"},
			{MemberNumber: prefix + "5", NationalID: "PAB123456"},
			{MemberNumber: prefix + "6"},
			{MemberNumber: prefix + "7", NationalID: "X1234567L"},
		}}
	}
	want := []db.NationalIDWarning{
		{Line: 1, Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{Line: 3, Class: "letter_mismatch"},
		{Line: 4, Class: "unsupported_document"},
		{Line: 5, Class: "invalid"},
		{Line: 1, Class: db.NationalIDWarningDuplicate},
		{Line: 2, Class: db.NationalIDWarningDuplicate},
	}

	c.Run("sync", func(c *qt.C) {
		resp := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token, submit("S-"),
			organizationMembersURL(orgAddress.String()))
		c.Assert(resp.Added, qt.Equals, uint32(7)) // warnings never block
		c.Assert(resp.Errors, qt.HasLen, 0)
		c.Assert(resp.NationalIDWarnings, qt.DeepEquals, want)
	})

	c.Run("async", func(c *qt.C) {
		resp := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token, submit("A-"),
			organizationMembersURL(orgAddress.String())+"?async=true")
		c.Assert(resp.JobID, qt.Not(qt.HasLen), 0)

		// an anonymous poll sees the job finish, but not the DNI values in its warnings
		anonymous := pollJob(t, resp.JobID.String())
		c.Assert(anonymous.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(anonymous.NationalIDWarnings, qt.IsNil)

		admin := requestAndParse[apicommon.JobResponse](t, http.MethodGet, token, nil, "jobs", resp.JobID.String())
		c.Assert(admin.NationalIDWarnings, qt.DeepEquals, want)

		// the org job list is Manager/Admin-only and carries them too
		listed := pollOrgJob(t, token, orgAddress.String(), resp.JobID.String())
		c.Assert(listed.NationalIDWarnings, qt.DeepEquals, want)
	})
}
```

`api/jobs_test.go`, in `TestJobStatusImportErrorsGatedByRole`: replace
```go
	c.Assert(testDB.CompleteJob(jobID, 2, []string{importErr}), qt.IsNil)
```
with
```go
	// the nationalId warnings carry DNI values, so they are gated exactly like the errors
	const piiDNI = "12345678Z"
	warnings := []db.NationalIDWarning{{Line: 1, Class: "derived_letter", Original: "12345678", Derived: piiDNI}}
	c.Assert(testDB.CompleteJob(jobID, 2, []string{importErr}, warnings), qt.IsNil)
```
after the `piiPhone` leak assertion in `assertStripped` add
```go
		c.Assert(strings.Contains(string(body), piiDNI), qt.IsFalse, qt.Commentf("%s body leaked a DNI: %s", who, body))
```
after `c.Assert(resp.Errors, qt.HasLen, 0, qt.Commentf("%s got error detail", who))` add
```go
		c.Assert(resp.NationalIDWarnings, qt.HasLen, 0, qt.Commentf("%s got nationalId warnings", who))
```
after `c.Assert(adminJob.Errors, qt.Contains, importErr)` add
```go
	c.Assert(adminJob.NationalIDWarnings, qt.DeepEquals, warnings)
```
and after the final `c.Assert(found.Errors, qt.Contains, importErr)` add
```go
	c.Assert(found.NationalIDWarnings, qt.DeepEquals, warnings)
```

`db/jobs_test.go`: replace `	err = testDB.CompleteJob(jobID, added, errors)` with `	err = testDB.CompleteJob(jobID, added, errors, nil)`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestNationalIDImportWarnings|TestJobOperations' ./db/`
Expected: build failure, `undefined: nationalIDImportWarnings` and `too many arguments in call to testDB.CompleteJob`.

Step 2: implement.

Append to `db/nationalid_warnings.go`:
```go
// nationalIDImportWarnings returns the nationalId warnings of a member import, Line being each
// member's 1-based position in members. Duplicates are checked within members only; checking
// them against the members already stored is census validation's job. An empty nationalId is
// not reported: it is only a problem for a census logging in with it, which validation reports.
func nationalIDImportWarnings(members []*OrgMember) []NationalIDWarning {
	n := newNationalIDWarnings()
	for i, m := range members {
		if strings.TrimSpace(m.NationalID) == "" {
			continue
		}
		n.add(NationalIDWarning{Line: i + 1}, m.NationalID, "")
	}
	return n.result()
}
```

`db/org_members.go`: in `BulkOrgMembersJob` add after `MemberIDs []string`:
```go
	// NationalIDWarnings are the non-blocking nationalId problems of the submitted members, Line
	// being each one's 1-based position in the submitted list. Set before the first batch runs.
	NationalIDWarnings []NationalIDWarning
```
and in `addOrgMemberBatches` replace
```go
	progress := &bulkOrgMembersProgress{job: BulkOrgMembersJob{Total: total, Errors: []error{}}}
```
with
```go
	progress := &bulkOrgMembersProgress{job: BulkOrgMembersJob{
		Total:              total,
		Errors:             []error{},
		NationalIDWarnings: nationalIDImportWarnings(orgMembers),
	}}
```

`db/types.go`, at the end of `Job` (after `HeartbeatAt`, after a blank line, so gofmt does not realign the struct):
```go
	HeartbeatAt time.Time `json:"-" bson:"heartbeatAt,omitempty"`

	// NationalIDWarnings are the nationalId warnings of a member import job (JobTypeOrgMembers).
	// They carry DNI values: GET /jobs/{jobId} hides them from anonymous callers, like Errors.
	NationalIDWarnings []NationalIDWarning `json:"nationalIdWarnings,omitempty" bson:"nationalIdWarnings,omitempty"`
}
```

`db/jobs.go`: replace the `CompleteJob` godoc and signature with
```go
// CompleteJob updates a job with final results when it completes. warnings are the nationalId
// warnings of a member import job, nil for any other.
func (ms *MongoStorage) CompleteJob(jobID string, added int, errors []string, warnings []NationalIDWarning) error {
```
and its `$set` with
```go
			"added":              added,
			"errors":             errors,
			"nationalIdWarnings": warnings,
			"completedAt":        time.Now(),
```

`api/org_members.go`: the sync response becomes
```go
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{
			Added:              uint32(lastProgress.Added),
			Errors:             append(lastProgress.ErrorsAsStrings(), propagated.Errors...),
			CensusJobIDs:       propagated.JobIDs,
			NationalIDWarnings: lastProgress.NationalIDWarnings,
		})
```
and the async completion
```go
			if err := a.db.CompleteJob(jobID.String(), lastProgress.Added, lastProgress.ErrorsAsStrings(),
				lastProgress.NationalIDWarnings); err != nil {
```

`api/apicommon/types.go`: in `AddMembersResponse` add after `CensusJobIDs`:
```go

	// NationalIDWarnings are the non-blocking nationalId problems of the submitted members (sync
	// path; an async import reports them on GET /jobs/{jobId}). line is the member's 1-based
	// position in the submitted list. duplicate_after_normalization only compares the submitted
	// members with each other.
	NationalIDWarnings []db.NationalIDWarning `json:"nationalIdWarnings,omitempty"`
```
in `JobResponse` add after `Result`:
```go
	// NationalIDWarnings are the nationalId warnings of a member import job, returned only to a
	// Manager/Admin of the job's organization (like the import errors).
	NationalIDWarnings []db.NationalIDWarning `json:"nationalIdWarnings,omitempty"`
```
and in `JobResponseFromDB` replace `	resp := JobResponse{JobID: job.JobID, Type: job.Type, Status: status, Errors: errs}` with
```go
	resp := JobResponse{
		JobID: job.JobID, Type: job.Type, Status: status, Errors: errs, NationalIDWarnings: job.NationalIDWarnings,
	}
```

`api/jobs.go`, in `jobStatusHandler`: the PII block becomes
```go
	// Member-import error strings can embed row PII (emails/phones/birthdates of the failing
	// members), and the nationalId warnings carry DNI values. Reveal that per-row detail only to an
	// authenticated manager/admin of the job's org (the same gate as GET /jobs); anonymous callers
	// (e.g. tx-job pollers, which use this endpoint without a token) and non-members get status +
	// counters only. tx-job errors are chain failures (no PII) and are always kept.
	if job.Type == db.JobTypeOrgMembers || job.Type == db.JobTypeCensusParticipants {
		u := a.optionalUser(r)
		if u == nil || (!u.HasRoleFor(job.OrgAddress, db.ManagerRole) && !u.HasRoleFor(job.OrgAddress, db.AdminRole)) {
			resp.Errors = nil
			resp.NationalIDWarnings = nil
		}
	}
```
and in its annotation replace
```go
//	@Description	data. Per-row import error detail (which may reference member data) is returned only
//	@Description	to an authenticated manager/admin of the job's organization (as on GET /jobs);
//	@Description	anonymous callers get status and counters only.
```
with
```go
//	@Description	data. Per-row import error detail and nationalId warnings (which may reference member
//	@Description	data) are returned only to an authenticated manager/admin of the job's organization (as on
//	@Description	GET /jobs); anonymous callers get status and counters only.
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestNationalIDImportWarnings|TestNationalIDWarnings|TestJobOperations|TestOrgMembers$|TestOrgMemberFieldsTrimmedOnWrite' ./db/`
Expected: `ok  github.com/vocdoni/saas-backend/db`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestAddMembersNationalIDWarnings|TestJobStatusImportErrorsGatedByRole|TestOrganizationMembers$' -v ./api/ | grep -E '^--- (PASS|FAIL)'`
Expected: `--- PASS` for TestAddMembersNationalIDWarnings (sync, async), TestJobStatusImportErrorsGatedByRole and TestOrganizationMembers; no FAIL.

Step 3: commit
`git add api/org_members_nationalid_warnings_test.go && git commit -m "feat(members): report nationalId warnings on member import" -- db/nationalid_warnings.go db/org_members.go db/jobs.go db/types.go api/org_members.go api/apicommon/types.go api/jobs.go db/jobs_test.go api/jobs_test.go db/nationalid_warnings_test.go api/org_members_nationalid_warnings_test.go`

Interfaces this task provides to later tasks: `CompleteJob(jobID string, added int, errors []string, warnings []NationalIDWarning) error`; `BulkOrgMembersJob.NationalIDWarnings`; `db.Job.NationalIDWarnings`; `apicommon.AddMembersResponse.NationalIDWarnings`, `apicommon.JobResponse.NationalIDWarnings`.

### Task 7: migration lock renewal
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: migrations/lock.go, migrations/lock_test.go, db/migrations.go, db/migrations_lock_test.go
Goal: a migration can renew the cross-instance lock and outlive the 10-minute run timeout and still be recorded; every other migration keeps the bound (AC14).

Step 1: write the failing tests.

`migrations/lock_test.go` (new):
```go
package migrations

import (
	"context"
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestRenewLock pins the renewer plumbing: a no-op without a renewer, the renewer's error passed
// through, and the renewer surviving context.WithoutCancel, which a migration uses to drop the run
// timeout.
func TestRenewLock(t *testing.T) {
	c := qt.New(t)
	c.Assert(RenewLock(context.Background()), qt.IsNil)

	calls := 0
	ctx := WithLockRenewer(context.Background(), func(context.Context) error {
		calls++
		return nil
	})
	c.Assert(RenewLock(ctx), qt.IsNil)
	c.Assert(calls, qt.Equals, 1)

	lost := fmt.Errorf("lock lost")
	ctx = WithLockRenewer(context.Background(), func(context.Context) error { return lost })
	c.Assert(RenewLock(ctx), qt.ErrorIs, lost)
	c.Assert(RenewLock(context.WithoutCancel(ctx)), qt.ErrorIs, lost)
}
```

`db/migrations_lock_test.go` (new):
```go
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestMigrationLockRenewal pins the lock renewal a long migration relies on: it extends the lock
// for its holder, fails once another instance took the lock over, does nothing without a renewer,
// and a migration that outlives the run timeout is still recorded in the ledger.
func TestMigrationLockRenewal(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()

	lock := func() (holder string, expiresAt time.Time) {
		var doc struct {
			Holder    string    `bson:"holder"`
			ExpiresAt time.Time `bson:"expiresat"`
		}
		c.Assert(testDB.migrations.FindOne(ctx, bson.M{"_id": migrationLockID}).Decode(&doc), qt.IsNil)
		return doc.Holder, doc.ExpiresAt
	}
	setExpiry := func(at time.Time) {
		_, err := testDB.migrations.UpdateOne(ctx, bson.M{"_id": migrationLockID},
			bson.M{"$set": bson.M{"expiresat": at}})
		c.Assert(err, qt.IsNil)
	}
	renewing := func(holder string) context.Context {
		return migrations.WithLockRenewer(ctx, func(renewCtx context.Context) error {
			return testDB.renewMigrationLock(renewCtx, holder)
		})
	}

	c.Run("extends the lock for its holder", func(c *qt.C) {
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-a"), qt.IsNil)
		defer testDB.releaseMigrationLock("holder-a")
		setExpiry(time.Now().Add(time.Minute))

		c.Assert(migrations.RenewLock(renewing("holder-a")), qt.IsNil)
		holder, expiresAt := lock()
		c.Assert(holder, qt.Equals, "holder-a")
		c.Assert(time.Until(expiresAt) > migrationLockTTL-time.Minute, qt.IsTrue,
			qt.Commentf("lock expires at %s", expiresAt))
	})

	c.Run("fails once another instance took the lock over", func(c *qt.C) {
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-a"), qt.IsNil)
		setExpiry(time.Now().Add(-time.Second))
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-b"), qt.IsNil)
		defer testDB.releaseMigrationLock("holder-b")

		c.Assert(migrations.RenewLock(renewing("holder-a")), qt.Not(qt.IsNil))
		holder, _ := lock()
		c.Assert(holder, qt.Equals, "holder-b")
	})

	c.Run("does nothing without a renewer", func(c *qt.C) {
		c.Assert(migrations.RenewLock(ctx), qt.IsNil)
	})

	c.Run("a migration outliving the run timeout is recorded", func(c *qt.C) {
		saved := migrationRunTimeout
		migrationRunTimeout = 2 * time.Second
		defer func() { migrationRunTimeout = saved }()

		migs := migrations.SortedByVersionAsc()
		version := migs[len(migs)-1].Version + 1
		renewErr := fmt.Errorf("RenewLock not called")
		migrations.AddMigration(version, "test_outlives_run_timeout", func(ctx context.Context, _ *mongo.Database) error {
			<-ctx.Done() // the run timeout passes while the migration still works
			renewErr = migrations.RenewLock(context.WithoutCancel(ctx))
			return nil
		}, func(context.Context, *mongo.Database) error { return nil })
		defer migrations.DelMigration(version)
		defer func() {
			_, err := testDB.migrations.DeleteOne(ctx, bson.M{"version": version})
			c.Assert(err, qt.IsNil)
		}()

		c.Assert(testDB.RunMigrationsUp(), qt.IsNil)
		c.Assert(renewErr, qt.IsNil)
		recorded, err := testDB.migrations.CountDocuments(ctx, bson.M{"version": version})
		c.Assert(err, qt.IsNil)
		c.Assert(recorded, qt.Equals, int64(1))
	})
}
```
Run: `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestRenewLock ./migrations/`
Expected: build failure, `undefined: RenewLock` / `undefined: WithLockRenewer`.

Step 2: implement.

`migrations/lock.go` (new):
```go
package migrations

import "context"

// lockRenewerKey is the context key of the function renewing the cross-instance migration lock.
type lockRenewerKey struct{}

// WithLockRenewer returns a copy of ctx carrying renew, which extends the cross-instance migration
// lock held by the run applying the migration. db.RunMigrationsUp sets it on the context it hands
// to every Up function.
func WithLockRenewer(ctx context.Context, renew func(context.Context) error) context.Context {
	return context.WithValue(ctx, lockRenewerKey{}, renew)
}

// RenewLock extends the cross-instance migration lock through the renewer ctx carries, failing
// when the lock was lost (it expired and another instance took it over). A migration that may run
// longer than the lock TTL calls it before each unit of work and stops on error, so two instances
// never migrate at once. Without a renewer (a test calling Up directly, RunMigrationsDown) it does
// nothing.
func RenewLock(ctx context.Context) error {
	renew, ok := ctx.Value(lockRenewerKey{}).(func(context.Context) error)
	if !ok || renew == nil {
		return nil
	}
	return renew(ctx)
}
```

`db/migrations.go`: replace the `migrationLockTTL` comment's last line `	// migration context timeout, so a live holder can never outlive its lock.` with
```go
	// migration context timeout, so a live holder can never outlive its lock;
	// a migration that opts out of that timeout renews the lock instead
	// (migrations.RenewLock).
```
insert before `// releaseMigrationLock releases`:
```go
// migrationRunTimeout bounds a RunMigrationsUp run. A migration may opt out of it for its own
// work, renewing the lock as it goes (migrations.RenewLock); a variable so a test can shorten it.
var migrationRunTimeout = 10 * time.Minute

// renewMigrationLock moves the expiry of the lock holder holds to migrationLockTTL from now. It
// fails when holder no longer holds the lock: it expired and another instance took it over.
func (ms *MongoStorage) renewMigrationLock(ctx context.Context, holder string) error {
	res, err := ms.migrations.UpdateOne(ctx,
		bson.M{"_id": migrationLockID, "holder": holder},
		bson.M{"$set": bson.M{"expiresat": time.Now().Add(migrationLockTTL)}})
	if err != nil {
		return fmt.Errorf("failed to renew migration lock: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("migration lock lost: another instance holds it")
	}
	return nil
}
```
in `RunMigrationsUp` replace `	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)` with
```go
	ctx, cancel := context.WithTimeout(context.Background(), migrationRunTimeout)
```
(only in `RunMigrationsUp`; `RunMigrationsDown` keeps its literal), and replace the apply loop, from `	// Apply pending migrations` through the ledger `InsertOne` error check, with:
```go
	// a migration that outlives ctx (migrations.RenewLock) renews the lock through this
	upCtx := migrations.WithLockRenewer(ctx, func(renewCtx context.Context) error {
		return ms.renewMigrationLock(renewCtx, holder)
	})

	// Apply pending migrations
	for _, migration := range migs {
		if migration.Version <= lastMigration {
			continue
		}

		log.Infow("applying migration", "version", migration.Version, "name", migration.Name)

		if err := migration.Up(upCtx, ms.DBClient.Database(ms.database)); err != nil {
			return fmt.Errorf("failed to apply migration %d (%s): %w", migration.Version, migration.Name, err)
		}

		if err := ms.recordMigration(migration.Version); err != nil {
			return err
		}
```
(the `log.Infow("migration applied successfully", ...)` line and the closing of the loop stay), and insert before `// lockMigrations takes the cross-instance`:
```go
// recordMigration writes the ledger record of an applied migration. It has its own context, as
// releaseMigrationLock does: a migration that renewed the lock past the run timeout must still be
// recorded, or it would run again on the next start.
func (ms *MongoStorage) recordMigration(version int) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	record := MigrationRecord{Version: version, AppliedAt: time.Now()}
	if _, err := ms.migrations.InsertOne(ctx, record); err != nil {
		return fmt.Errorf("failed to record migration %d: %w", version, err)
	}
	return nil
}
```
Run: `GOPRIVATE=github.com/vocdoni/* go test -count=1 ./migrations/`
Expected: `ok  github.com/vocdoni/saas-backend/migrations`.
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestMigrationLockRenewal|TestMigrations$' -v ./db/ | grep -E '^(--- |    --- )(PASS|FAIL)'`
Expected: `--- PASS: TestMigrationLockRenewal` with 4 subtests PASS (the last takes ~2 s), `--- PASS: TestMigrations`; no FAIL.

Step 3: commit
`git add migrations/lock.go migrations/lock_test.go db/migrations_lock_test.go && git commit -m "feat(migrations): let a long migration renew the migration lock" -- migrations/lock.go migrations/lock_test.go db/migrations.go db/migrations_lock_test.go`

Interfaces this task provides to later tasks: `migrations.WithLockRenewer(ctx, func(context.Context) error) context.Context`; `migrations.RenewLock(ctx) error` (nil without a renewer); `(*MongoStorage).renewMigrationLock(ctx, holder) error`; `(*MongoStorage).recordMigration(version) error`; package var `migrationRunTimeout`.

### Task 8: migration 0034 normalize_national_ids
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: migrations/0034_normalize_national_ids.go, db/migration_normalize_national_ids_test.go
Goal: canonicalise every stored DNI/NIE and recompute the affected login hashes, skipping collisions, in batches that renew the lock (AC6, AC7 migration part, AC15).

Notes: only participants of censuses whose auth fields include `nationalId` are rehashed (their hashes are the only ones that change). `AddMigration` must keep an integer literal (`TestMigrationVersionsAreUnique`), hence the separate exported constant.

Step 1: write the failing test, `db/migration_normalize_national_ids_test.go`:
```go
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// runNationalIDMigration invokes migration 0034's Up directly against the test database. ctx may
// carry a lock renewer (migrations.WithLockRenewer).
func runNationalIDMigration(ctx context.Context, c *qt.C) error {
	mig := migrationByVersion(c, migrations.NationalIDNormalizationVersion)
	return mig.Up(ctx, testDB.DBClient.Database(testDB.database))
}

// seedRawMember stores a member the way a database written before migration 34 holds it,
// bypassing the write path that would canonicalize its nationalId.
func seedRawMember(c *qt.C, nationalID string) OrgMember {
	id := bson.NewObjectID()
	m := OrgMember{
		ID: id, OrgAddress: testOrgAddress, NationalID: nationalID,
		Email: id.Hex() + "@example.com", MemberNumber: "M-" + id.Hex(), Weight: 1, CreatedAt: time.Now(),
	}
	_, err := testDB.orgMembers.InsertOne(context.Background(), m)
	c.Assert(err, qt.IsNil)
	return m
}

// seedCensus stores a census and returns it with its id set.
func seedCensus(c *qt.C, auth OrgMemberAuthFields, twoFa OrgMemberTwoFaFields) *Census {
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: twoFa, CreatedAt: time.Now()}
	id, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	census.ID, err = bson.ObjectIDFromHex(id)
	c.Assert(err, qt.IsNil)
	return census
}

// seedParticipant stores m as a participant of census, hashed from its stored values.
func seedParticipant(c *qt.C, census *Census, m OrgMember) {
	_, err := testDB.censusParticipants.InsertOne(context.Background(), &CensusParticipant{
		ParticipantID: m.ID.Hex(), CensusID: census.ID.Hex(),
		LoginHash: HashAuthTwoFaFields(m, census.AuthFields, census.TwoFaFields), CreatedAt: time.Now(),
	})
	c.Assert(err, qt.IsNil)
}

func storedMember(c *qt.C, id bson.ObjectID) OrgMember {
	var m OrgMember
	c.Assert(testDB.orgMembers.FindOne(context.Background(), bson.M{"_id": id}).Decode(&m), qt.IsNil)
	return m
}

func storedLoginHash(c *qt.C, census *Census, m OrgMember) []byte {
	var p CensusParticipant
	c.Assert(testDB.censusParticipants.FindOne(context.Background(),
		bson.M{"participantID": m.ID.Hex(), "censusId": census.ID.Hex()}).Decode(&p), qt.IsNil)
	return p.LoginHash
}

func TestMigration0034NormalizeNationalIDs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	byID := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
	byNumber := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber}, OrgMemberTwoFaFields{})

	separators := seedRawMember(c, "2.345.678-t")
	letterless := seedRawMember(c, "12345678")
	wrongLetter := seedRawMember(c, "12345678A")
	passport := seedRawMember(c, "PAB 123456")
	canonical := seedRawMember(c, "X1234567L")
	for _, m := range []OrgMember{separators, letterless, wrongLetter, passport, canonical} {
		seedParticipant(c, byID, m)
	}
	seedParticipant(c, byNumber, separators)
	byNumberHash := storedLoginHash(c, byNumber, separators)

	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)

	for _, tc := range []struct {
		member           OrgMember
		stored, original string
		typed            string
	}{
		{separators, "02345678T", "", "02345678T"},
		{letterless, "12345678Z", "12345678", "12.345.678-z"},
		{wrongLetter, "12345678A", "", "12345678a"},
		{passport, "PAB 123456", "", "PAB 123456"},
		{canonical, "X1234567L", "", "x-1234567-l"},
	} {
		got := storedMember(c, tc.member.ID)
		c.Assert(got.NationalID, qt.Equals, tc.stored)
		c.Assert(got.NationalIDOriginal, qt.Equals, tc.original)
		// the stored hash is the canonical one, and the real login lookup resolves any variant
		c.Assert(storedLoginHash(c, byID, got), qt.DeepEquals, HashAuthTwoFaFields(got, byID.AuthFields, byID.TwoFaFields))
		input := (&OrgMember{NationalID: tc.typed, Email: got.Email}).Normalized()
		found, err := testDB.CensusParticipantByLoginHash(*byID, *input)
		c.Assert(err, qt.IsNil, qt.Commentf("typed %q", tc.typed))
		c.Assert(found.ParticipantID, qt.Equals, got.ID.Hex())
	}
	// a census not logging in with the nationalId is not rewritten
	c.Assert(storedLoginHash(c, byNumber, separators), qt.DeepEquals, byNumberHash)

	// idempotent: a second run finds nothing to do
	before := storedMember(c, letterless.ID)
	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)
	c.Assert(storedMember(c, letterless.ID), qt.DeepEquals, before)
}

func TestMigration0034Collision(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	census := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{})
	letterless := seedRawMember(c, "12345678")
	lettered := seedRawMember(c, "12345678Z")
	seedParticipant(c, census, letterless)
	seedParticipant(c, census, lettered)
	letterlessHash := storedLoginHash(c, census, letterless)

	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)

	// the letterless member is skipped whole: value, marker and hash as before
	got := storedMember(c, letterless.ID)
	c.Assert(got.NationalID, qt.Equals, "12345678")
	c.Assert(got.NationalIDOriginal, qt.Equals, "")
	c.Assert(storedLoginHash(c, census, got), qt.DeepEquals, letterlessHash)
	c.Assert(storedMember(c, lettered.ID).NationalID, qt.Equals, "12345678Z")

	// each still resolves from their own stored value
	found, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{NationalID: "12345678"})
	c.Assert(err, qt.IsNil)
	c.Assert(found.ParticipantID, qt.Equals, letterless.ID.Hex())

	// validation reports the pair, without blocking on it (their hashes differ)
	results, err := testDB.CheckMembersFields(testOrgAddress, []string{letterless.ID.Hex(), lettered.ID.Hex()},
		census.AuthFields, census.TwoFaFields)
	c.Assert(err, qt.IsNil)
	c.Assert(results.Duplicates, qt.HasLen, 0)
	c.Assert(results.NationalIDWarnings, qt.ContentEquals, []NationalIDWarning{
		{MemberID: letterless.ID.Hex(), Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: letterless.ID.Hex(), Class: NationalIDWarningDuplicate},
		{MemberID: lettered.ID.Hex(), Class: NationalIDWarningDuplicate},
	})
}

func TestMigration0034Batches(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	const candidates = 2*500 + 1 // nationalIDBatchSize is 500: two full batches and one more

	seed := func(c *qt.C) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		docs := make([]any, candidates)
		for i := range docs {
			docs[i] = OrgMember{
				ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: fmt.Sprintf("%08d", 10000000+i),
				CreatedAt: time.Now(),
			}
		}
		_, err := testDB.orgMembers.InsertMany(context.Background(), docs)
		c.Assert(err, qt.IsNil)
	}
	derived := func(c *qt.C) int64 {
		n, err := testDB.orgMembers.CountDocuments(context.Background(), bson.M{"nationalIdOriginal": bson.M{"$exists": true}})
		c.Assert(err, qt.IsNil)
		return n
	}

	c.Run("every candidate is normalized once, renewing the lock per batch", func(c *qt.C) {
		seed(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			return nil
		})
		c.Assert(runNationalIDMigration(ctx, c), qt.IsNil)
		c.Assert(renewals, qt.Equals, 3)
		c.Assert(derived(c), qt.Equals, int64(candidates))
		left, err := testDB.orgMembers.CountDocuments(context.Background(),
			bson.M{"nationalId": bson.M{"$not": bson.Regex{Pattern: "^[0-9]{8}[A-Z]$"}}})
		c.Assert(err, qt.IsNil)
		c.Assert(left, qt.Equals, int64(0))
	})

	c.Run("a lost lock aborts before the batch writes", func(c *qt.C) {
		seed(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			if renewals == 2 {
				return fmt.Errorf("migration lock lost: another instance holds it")
			}
			return nil
		})
		c.Assert(runNationalIDMigration(ctx, c), qt.ErrorMatches, ".*migration lock lost.*")
		c.Assert(derived(c), qt.Equals, int64(500)) // the first batch only
	})
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestMigration0034 ./db/`
Expected: build failure, `undefined: migrations.NationalIDNormalizationVersion`.

Step 2: implement, `migrations/0034_normalize_national_ids.go`:
```go
package migrations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// NationalIDNormalizationVersion is the version of migration 0034, which canonicalizes stored
// nationalIds. The certificate census publish guard asks db.MigrationApplied for it, since a
// branch merge could absorb it under a higher ledger version and skip it silently.
const NationalIDNormalizationVersion = 34

const (
	// nationalIDBatchSize is how many candidate members one batch of migration 0034 reads.
	nationalIDBatchSize = 500
	// nationalIDBatchTimeout bounds one batch of migration 0034. It is well under the migration
	// lock TTL, which every batch renews first, so the lock cannot expire under a live run.
	nationalIDBatchTimeout = 2 * time.Minute
)

func init() {
	AddMigration(34, "normalize_national_ids", upNormalizeNationalIDs, downNormalizeNationalIDs)
}

// nationalIDCandidates selects the members whose nationalId may not be canonical yet, after lastID
// in _id order: a non-empty string that is not already shaped like an uppercase DNI/NIE. It is a
// coarse prefilter; NormalizeNationalID decides in Go.
func nationalIDCandidates(lastID bson.ObjectID) bson.M {
	filter := bson.M{"nationalId": bson.M{
		"$type": "string",
		"$ne":   "",
		"$not":  bson.Regex{Pattern: "^([0-9]{8}|[XYZ][0-9]{7})[A-Z]$"},
	}}
	if !lastID.IsZero() {
		filter["_id"] = bson.M{"$gt": lastID}
	}
	return filter
}

// nationalIDNormalizer holds the state of one run of migration 0034.
type nationalIDNormalizer struct {
	members      *mongo.Collection
	participants *mongo.Collection
	censuses     *mongo.Collection
	// censusCache maps a census hex id to its auth configuration, nil when it does not exist
	censusCache                  map[string]*censusHashDoc
	normalized, derived, skipped int
}

// upNormalizeNationalIDs canonicalizes every stored nationalId that is a DNI/NIE in a
// non-canonical form (separators, lowercase, missing zero padding, missing check letter) and
// recomputes the login hashes of the member's participants in every census logging in with the
// nationalId. Values that are not a valid DNI/NIE are left exactly as stored.
//
// It follows migration 0015: a member whose new hash would collide with another participant of a
// census is skipped whole and logged (by id only, never the value), so it keeps matching its own
// input and is re-detected on a later run; and the participants are written before the member, so
// an interrupted member is re-detected too.
//
// It works in keyset-paginated batches, each under its own timeout instead of the run's, and
// renews the migration lock before each one, so it does not depend on finishing within the run
// timeout or the lock TTL. A lost lock aborts it before the batch writes anything.
func upNormalizeNationalIDs(ctx context.Context, database *mongo.Database) error {
	n := &nationalIDNormalizer{
		members:      database.Collection("orgMembers"),
		participants: database.Collection("censusParticipants"),
		censuses:     database.Collection("census"),
		censusCache:  make(map[string]*censusHashDoc),
	}
	lastID := bson.NilObjectID
	for {
		next, more, err := n.batch(ctx, lastID)
		if err != nil {
			return err
		}
		if !more {
			break
		}
		lastID = next
	}
	log.Infow("normalized member nationalIds", "normalized", n.normalized, "derived", n.derived, "skipped", n.skipped)
	return nil
}

// batch normalizes the next nationalIDBatchSize candidates after lastID, returning the last id it
// visited and whether a full batch was read (so more may follow).
func (n *nationalIDNormalizer) batch(parent context.Context, lastID bson.ObjectID) (bson.ObjectID, bool, error) {
	// drop the run timeout, keep the context values (the lock renewer)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), nationalIDBatchTimeout)
	defer cancel()
	if err := RenewLock(ctx); err != nil {
		return lastID, false, fmt.Errorf("failed to renew the migration lock: %w", err)
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetLimit(nationalIDBatchSize)
	cur, err := n.members.Find(ctx, nationalIDCandidates(lastID), opts)
	if err != nil {
		return lastID, false, fmt.Errorf("failed to list members: %w", err)
	}
	var members []memberHashDoc
	if err := cur.All(ctx, &members); err != nil {
		return lastID, false, fmt.Errorf("failed to decode members: %w", err)
	}
	for _, m := range members {
		if err := n.normalizeMember(ctx, m); err != nil {
			return lastID, false, err
		}
		lastID = m.ID
	}
	return lastID, len(members) == nationalIDBatchSize, nil
}

// normalizeMember canonicalizes one member's nationalId and its participants' login hashes,
// unless the value has no canonical form, is canonical already, or a hash would collide.
func (n *nationalIDNormalizer) normalizeMember(ctx context.Context, m memberHashDoc) error {
	r := internal.NormalizeNationalID(m.NationalID)
	if r.Canonical == "" || r.Canonical == m.NationalID {
		return nil // letter_mismatch, unsupported_document, invalid: left exactly as stored
	}
	memberHex := m.ID.Hex()
	canonical := m
	canonical.NationalID = r.Canonical

	pcur, err := n.participants.Find(ctx, bson.M{"participantID": memberHex})
	if err != nil {
		return fmt.Errorf("failed to find participants for member %s: %w", memberHex, err)
	}
	var participants []participantHashDoc
	if err := pcur.All(ctx, &participants); err != nil {
		return fmt.Errorf("failed to decode participants for member %s: %w", memberHex, err)
	}

	type participantUpdate struct {
		filter bson.M
		hashes bson.M
	}
	updates := make([]participantUpdate, 0, len(participants))
	for _, p := range participants {
		census, err := n.census(ctx, p.CensusID)
		if err != nil {
			return err
		}
		if census == nil || !slices.Contains(census.AuthFields, "nationalId") {
			continue // the nationalId takes no part in this census's hashes
		}
		hashes := recomputeParticipantHashes(canonical, *census)
		findHashes := make([]bson.M, 0, len(hashes))
		for k, v := range hashes {
			findHashes = append(findHashes, bson.M{k: v})
		}
		count, err := n.participants.CountDocuments(ctx, bson.M{
			"participantID": bson.M{"$ne": p.ParticipantID},
			"censusId":      p.CensusID,
			"$or":           findHashes,
		})
		if err != nil {
			return fmt.Errorf("failed to check collisions for member %s in census %s: %w", memberHex, p.CensusID, err)
		}
		if count > 0 {
			// ids only: the value is a DNI
			log.Warnw("skipping member nationalId normalization due to login hash collision",
				"memberID", memberHex, "censusID", p.CensusID)
			n.skipped++
			return nil
		}
		updates = append(updates, participantUpdate{
			filter: bson.M{"participantID": p.ParticipantID, "censusId": p.CensusID},
			hashes: hashes,
		})
	}

	// participants first, then the member: an interrupted member keeps its old value and is
	// re-detected on the next run
	now := time.Now()
	for _, u := range updates {
		set := bson.M{"updatedAt": now}
		for k, v := range u.hashes {
			set[k] = v
		}
		if _, err := n.participants.UpdateOne(ctx, u.filter, bson.M{"$set": set}); err != nil {
			return fmt.Errorf("failed to update participant hashes for member %s: %w", memberHex, err)
		}
	}
	set := bson.M{"nationalId": r.Canonical, "updatedAt": now}
	if r.Class == internal.NationalIDDerivedLetter {
		set["nationalIdOriginal"] = strings.TrimSpace(m.NationalID)
		n.derived++
	}
	if _, err := n.members.UpdateOne(ctx, bson.M{"_id": m.ID}, bson.M{"$set": set}); err != nil {
		return fmt.Errorf("failed to update nationalId for member %s: %w", memberHex, err)
	}
	n.normalized++
	return nil
}

// census returns the auth configuration of a census, cached, or nil when it does not exist.
func (n *nationalIDNormalizer) census(ctx context.Context, hexID string) (*censusHashDoc, error) {
	if c, ok := n.censusCache[hexID]; ok {
		return c, nil
	}
	oid, err := bson.ObjectIDFromHex(hexID)
	if err != nil {
		n.censusCache[hexID] = nil
		return nil, nil
	}
	var c censusHashDoc
	err = n.censuses.FindOne(ctx, bson.M{"_id": oid}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		n.censusCache[hexID] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load census %s: %w", hexID, err)
	}
	n.censusCache[hexID] = &c
	return &c, nil
}

// downNormalizeNationalIDs is intentionally a no-op: the normalization is lossy (the original
// separators, case and padding cannot be recovered), like migrations 0015 and 0025.
func downNormalizeNationalIDs(_ context.Context, _ *mongo.Database) error {
	return nil
}
```
Run: `GOPRIVATE=github.com/vocdoni/* go test -count=1 ./migrations/ && go vet ./migrations/`
Expected: `ok  github.com/vocdoni/saas-backend/migrations` (TestMigrationVersionsAreUnique sees 34 once).
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestMigration0034|TestMigrations$|TestNormalizeMemberEmailsMigration|TestRepair' -v ./db/ | grep -E '^(--- |    --- )(PASS|FAIL)'`
Expected: `--- PASS` for TestMigration0034NormalizeNationalIDs, TestMigration0034Collision, TestMigration0034Batches (2 subtests, ~2 s), TestMigrations, TestNormalizeMemberEmailsMigration and the TestRepair* tests; no FAIL.

Step 3: commit
`git add migrations/0034_normalize_national_ids.go db/migration_normalize_national_ids_test.go && git commit -m "feat(migrations): canonicalise stored nationalIds and their login hashes" -- migrations/0034_normalize_national_ids.go db/migration_normalize_national_ids_test.go`

Interfaces this task provides to later tasks: `migrations.NationalIDNormalizationVersion = 34`.

### Task 9: MigrationApplied
Model: haiku   Repo: /Users/ferran/Repos/saas-backend   Files: db/migrations.go, db/migration_applied_test.go
Goal: SP2's publish guard can ask whether migration 34 is in the ledger (AC11).

Step 1: write the failing test, `db/migration_applied_test.go`:
```go
package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMigrationApplied pins the ledger check the certificate census publish guard relies on: false
// for a migration the ledger lacks, true once RunMigrationsUp applied it.
func TestMigrationApplied(t *testing.T) {
	c := qt.New(t)
	// a database of its own: the test rewrites its ledger
	store, err := New(mongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	defer store.Close()
	version := migrations.NationalIDNormalizationVersion

	applied, err := store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsTrue)

	// a ledger that never ran it (nor any later migration, so RunMigrationsUp runs it again)
	_, err = store.migrations.DeleteMany(context.Background(), bson.M{"version": bson.M{"$gte": version}})
	c.Assert(err, qt.IsNil)
	applied, err = store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsFalse)

	c.Assert(store.RunMigrationsUp(), qt.IsNil)
	applied, err = store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsTrue)

	applied, err = store.MigrationApplied(9999)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsFalse)
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run TestMigrationApplied ./db/`
Expected: build failure, `store.MigrationApplied undefined`.

Step 2: implement. In `db/migrations.go` insert before `// lastAppliedMigration returns the last applied migration version.`:
```go
// MigrationApplied reports whether the migration with the given version has a record in the
// migrations ledger (RunMigrationsUp writes one per applied migration). The ledger only compares
// its highest version when deciding what to run, so a migration a branch merge absorbed under a
// higher version is skipped silently; a feature that depends on a migration's data checks this.
func (ms *MongoStorage) MigrationApplied(version int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	n, err := ms.migrations.CountDocuments(ctx, bson.M{"version": version}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to check migration %d: %w", version, err)
	}
	return n > 0, nil
}
```
Run (Docker: run by the controller): `GOPRIVATE=github.com/vocdoni/* go test -count=1 -run 'TestMigrationApplied|TestMigrations$' ./db/`
Expected: `ok  github.com/vocdoni/saas-backend/db`.

Step 3: commit
`git add db/migration_applied_test.go && git commit -m "feat(db): report whether a migration is in the ledger" -- db/migrations.go db/migration_applied_test.go`

Interfaces this task provides to later tasks: `(*MongoStorage).MigrationApplied(version int) (bool, error)`.

### Task 10: swagger, lint and full suites
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: docs/swagger.yaml
Goal: generated spec, lint, quicktest gate and the whole suite green (AC13).

Step 1: regenerate the spec.
Run: `PATH="$(go env GOPATH)/bin:$PATH" GOPRIVATE=github.com/vocdoni/* make swagger`
Expected: `Swagger documentation generated successfully at docs/swagger.yaml`; `git diff --stat` shows only `docs/swagger.yaml` (about +61/-8): new `db.NationalIDWarning` and `apicommon.ValidateProcessCensusResponse` definitions, `nationalIdWarnings` on `AddMembersResponse` and `JobResponse`, `nationalIdOriginal` on `OrgMember`, the census-validation 200 now `$ref: '#/definitions/apicommon.ValidateProcessCensusResponse'`, and the updated descriptions. If `swag fmt` touched any `.go` file, stop and report it.

Step 2: static checks.
Run: `GOPRIVATE=github.com/vocdoni/* golangci-lint run --new-from-rev=632b0b1 ./...`
Expected: `0 issues.` (a plain `make lint` also lists 5 pre-existing `unhandled-error` reports in `db/csp_counters.go:56-60`, untouched by this branch).
Run: `./scripts/check-qt-patterns.sh`
Expected: `✅ No qt anti-patterns found!`
Run: `go mod tidy && git diff --exit-code go.mod go.sum`
Expected: exit 0, no diff.

Step 3: full suites (Docker: run by the controller).
Run: `GOPRIVATE=github.com/vocdoni/* go test -count=1 -timeout=40m ./...` (no `-failfast`, so one environment failure does not hide the rest; `api` alone took ~28 min on the Mac)
Expected: every package `ok` (or `[no test files]`). On macOS only, `scripts/integrationtest` fails `TestStickyDerivation` with `declare: -A: invalid option` (bash 3.2; environment, untouched by this branch, passes in CI).

Step 4: commit
`git commit -m "docs(swagger): regenerate for the nationalId warnings" -- docs/swagger.yaml`

## Merge and release
1. Full suites: `GOPRIVATE=github.com/vocdoni/* go test -count=1 -timeout=40m ./...` → every package `ok` (bar the macOS-only `TestStickyDerivation` bash 3.2 failure; CI runs `go test -failfast -timeout=30m ./...` on Linux); `golangci-lint run --new-from-rev=632b0b1 ./...` → `0 issues.`; `./scripts/check-qt-patterns.sh` → no anti-patterns; `make swagger` leaves no diff.
2. Merge order: re-verify the ledger first (`git fetch origin && git ls-tree origin/main migrations/ && git ls-tree origin/lts migrations/`: both must still end at 0033; if not, renumber 0034 and `NationalIDNormalizationVersion` before anything ships). Then one PR `feat/nationalid-normalization` → `main` (AGENTS.md: `main` → `stage` → `lts`). PR title `feat(members): canonical DNI/NIE nationalIds and login-hash migration`; description per the owner's convention (opener line, under ~15 lines, no emojis, no attribution footer): what changed and why, the one breaking wire change (census validation 200 is now a JSON body), the later-migration-after-a-long-0034 note (spec §5.7), how it was tested, and the docs/SDK impact as suggested follow-ups (vocdoni.io `stage` pages for census validation, member import and jobs; integrator-sdk `@vocdoni/api-types` for `ValidateProcessCensusResponse`, `NationalIDWarning`, `nationalIdOriginal`).
3. Release: no version bump or tag in this repo; it ships through the normal `main` → `stage` → `lts` promotion PRs.
4. **Owner OK required before:** pushing the branch; opening the PR; merging it; promoting to `stage`; promoting to `lts` (production).
5. Pre-deploy (owner/ops, before stg and before prod; does not block the merge): on a recent production dump, count 0034's candidates with `db.orgMembers.countDocuments({nationalId: {$type: "string", $ne: "", $not: /^([0-9]{8}|[XYZ][0-9]{7})[A-Z]$/}})` and time the migration against that dump, so the deploy window and the startup-probe grace come from a real number (replicas waiting on the lock fail startup after 10 minutes and are restarted by the orchestrator).
6. Post-release checks: the startup log has `normalized member nationalIds` with its `normalized`/`derived`/`skipped` counters and no nationalId value; every `skipping member nationalId normalization` warning is reviewed by member/census id; `MigrationApplied(34)` is true; a smoke `auth/0` on an existing `nationalId` census succeeds with both the canonical and a separator variant.

