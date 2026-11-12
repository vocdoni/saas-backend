# Integrator-attested census for the SaaS CSP: implementation plan

Spec: docs/specs/2026-11-09-integrator-attestation-design.md
Class: architectural
Repos and branches:
- vocdoni/saas-backend → `feat/integrator-attestation` (base `main` b5ea5b0)
- vocdoni/decidim-secure_elections → `feat/integrator-attestation` (base `demo/integrated-booth` 34e3476)

## Decisions

- verifyingContract is the address of the CSP ECDSA root key of the environment (`CSP.AttestationDomain`); the SaaS echoes it in `census.attested.verifyingContract` on process reads and in the audit `domain`, and the gem caches it in the process metadata on first use.
- The SaaS writes the attestation record before signing each question (single sign: 500 if the write fails). The record keeps the first attestation seen for the uid; a question is listed even if its signature then fails.
- Auth-only (no 2FA) attested voters reuse the last verified token issued under the same signer; new ones count against the daily challenge counter, so the cap still bounds token minting.
- Resend checks the canonical contact and the contactHash before claiming a resend slot (spec order).
- `PUT /processes/{id}/census/size` works on published processes only (409 for drafts, whose size is edited through the process update) and is paid like census growth.
- The audit endpoint treats a zero EndDate as ended once every question is ENDED or RESULTS.
- Gem: signer key and voterId secret are stored with `Decidim::AttributeEncryptor` (secret `vocdoni-attestation`); `census_mode` comes from `VOCDONI_CENSUS_MODE` (default `memberbase`) and is fixed when the SaaS process is created; `maxCensusSize = max(ceil(eligible × 1.2), 1)` in integer arithmetic; one issued-count row per voterId; email is the only 2FA channel (the gem holds no phone); the SDK passes request bodies through, so no SDK change.
- Gem voter check: a voter proves who they are through the upstream census check (`new_election_census_check_path`) and then reads `GET …/vocdoni_attestation`; the receipt note links to it.

## Spec deviations (accepted)

1. CSPAuth stores a nested `attestation` subdocument (`voterId, contactHash, contactKind, expiresAt, signature, signer`) instead of flat `ContactHash/ContactKind/Signer` fields on the token.
2. A published attestation record may list a question whose signature failed after the record was written (see Decisions); §4.5 reads as "questions it signed for".
3. The optional refusal of pins past `Census.Size` is not implemented: the on-chain census size already bounds votes per question.
4. The admin comparison (G6) shows issued vs published voterIds, unknown voterIds and signed voterIds per question against `maxCensusSize`; it does not read votes on chain per question (§7 "votes on chain ≤ voterIds signed"). That check stays with the explorer/results data.

## Global Constraints

- Never log or print secrets, contacts, salts or contactHashes.
- Memberbase censuses behave exactly as before: every attested branch is gated on `census.Attested != nil` (SaaS) or `process.attested?` (gem).
- SaaS lint: `golangci-lint run --new-from-rev=<base>` clean. Gem lint: RuboCop and ESLint clean on touched files.

## Review Focus

- `csp/attestation/attestation.go:98` Recover and `:132` Verify: low-s, v normalisation, address comparison; `:147` CheckExpiry window.
- `csp/attestation/contact.go:33` CheckCanonical and `:61` MatchContact (constant-time compare).
- `csp/handlers/attested.go:66` verifyAttestation (check order, 2FA ↔ contact/salt), `:124` attestedResend (checks before claiming), `:173` attestedSignContext (signer revocation, anonymous refusal), `:208` recordAttested.
- `csp/auth.go:47` AttestedAuthOnlyToken (token reuse per uid+signer, daily cap).
- `db/csp_attestations.go:35` RecordAttestedSignature (upsert race), `:105` RotateAttestationSigner, `:139` GrowAttestedCensusSize.
- `api/processes_census.go:47` attestedCensusProblem; `api/processes_attested.go:89` growAttestedCensus (lock + payment); `api/processes_attestations.go:184` attestationsPublic gate.
- Gem `lib/decidim/elections/vocdoni/attestation.rb:80` sign and `:118` rfc6979_k.
- Gem `app/models/decidim/elections/vocdoni/process.rb:76` ensure_attestation_secrets!, `:98` rotate_attestation_signer!, `:108` voter_id_for.
- Gem `app/services/decidim/elections/vocdoni/voter_attestation.rb:37` call, `:55` contact_and_salt, `:66` verifying_contract.
- Gem `app/controllers/decidim/elections/vocdoni/attestations_controller.rb:25` create (census access gate) and `:49` signed_state.
- Gem `app/packs/src/decidim/elections/vocdoni/voter/auth_session.js:112` attested branch of authenticate.

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| S1 EIP-712 attestation verification in Go | — | saas: 5 files | G1-G6 (other repo) |
| S2 Storage: attested census, attested tokens, attestation records | S1 | saas: 8 files | G1-G6 (other repo) |
| S3 Create and publish with `census.attested` | S2 | saas: 5 files | G1-G6 (other repo) |
| S4 Grow the census size, rotate the signer | S3 | saas: 7 files | G1-G6 (other repo) |
| S5 auth/0, auth/1 and resend for attested voters | S2, S3 | saas: 6 files | G1-G6 (other repo) |
| S6 sign / sign-batch / check for attested voters | S5 | saas: 3 files | G1-G6 (other repo) |
| S7 Participant lookup by voterId and the public audit endpoint | S6 | saas: 6 files | G1-G6 (other repo) |
| S8 Swagger | S7 | saas: 2 files | G1-G6 (other repo) |
| G1 EIP-712 attestation signing in Ruby | S1 (vectors file) | gem: 4 files | S1-S8 (other repo; gem specs stub the SaaS) |
| G2 Per-election signer key, voterId secret, issued counts | G1 | gem: 4 files | S1-S8 (other repo; gem specs stub the SaaS) |
| G3 Publish attested processes; client calls; signer rotation task | G2, S3, S4, S7 | gem: 6 files | S1-S8 (other repo; gem specs stub the SaaS) |
| G4 Attestation endpoint for the booth | G3, S5 | gem: 6 files | S1-S8 (other repo; gem specs stub the SaaS) |
| G5 Booth adapter authenticates with the attestation | G4 | gem: 6 files | S1-S8 (other repo; gem specs stub the SaaS) |
| G6 Voter signature check and admin comparison | G4, S7 | gem: 16 files | S1-S8 (other repo; gem specs stub the SaaS) |

Within a repo the tasks are stacked: each patch applies on the previous one (`git am`). G4-G6 only need the SaaS changes at integration time (demo), not to pass their specs: the gem specs stub the SaaS with WebMock.

The code for each task is the matching commit on the feature branches (S1–S8 in saas-backend,
G1–G6 in decidim-secure_elections, in order).

## Pre-flight audit

An independent review of every task's code against the spec found no critical issue, one major and
three minor findings; all four are fixed on the branches:

- Major: a census without 2FA accepted a non-zero `contactHash`, so its published record could not be
  re-verified. Fixed in saas-backend (`fix(csp): refuse a contact hash on attested censuses without 2fa`).
- Minor (gem): the verifying-contract cache write now runs under a row lock; a voter may request at
  most 50 attestations per election (429 afterwards); an attestations page without pagination raises
  instead of under-reporting.

The review found the Go and Ruby EIP-712 encodings byte-identical on the shared vectors, the SaaS ↔
gem wire format consistent field by field, and judged the four spec deviations above acceptable.

## Merge and release

1. Full suites:
   - SaaS: `go test ./... -count=1 -timeout 60m` → 24 packages `ok`, exit 0 ; `golangci-lint run --new-from-rev=7f30c20` → no new issues.
   - Gem: `bundle exec rspec` → `207 examples, 0 failures, 1 pending`; `npx jest` → `218 passed`; `bundle exec rubocop` → only the 7 offenses already on 34e3476.
2. Merge order: saas-backend `feat/integrator-attestation` → `main` first; then decidim-secure_elections `feat/integrator-attestation` → `demo/integrated-booth`. The gem defaults to `VOCDONI_CENSUS_MODE=memberbase`, so merging it changes nothing until an environment opts in.
3. Release: SaaS goes main → stage → lts through the usual promotion; LTS gets it only as an owner-chosen backport. No gem version bump in this delivery.
4. **Owner OK required before:** each push of either branch; opening each PR; each merge; any deploy of the SaaS branch to dev/stg; setting `VOCDONI_CENSUS_MODE=attested` on any Decidim environment; the spec deviations above.
5. Post-release checks: on the demo/dev stack, run one attested election end to end (create → publish → booth auth with email OTP → vote → end → `GET /processes/{id}/attestations` → voter check page and admin card agree); check the SaaS logs carry no contact, salt or contactHash.
