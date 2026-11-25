# SaaS mailing templates redesign: design

Status: audited (2026-11-25, Fable: approve with changes, all 15 findings folded in)   Class: architectural
Issue: #817
Owner decisions so far (2026-11-25):
- One shared layout + per-email content + one string file per language (not 24 hand-kept HTML files, not MJML).
- All 10 app locales: ca, de, el, en, es, eu, fr, it, pt, pt-br. ca/es/en reviewed in depth; the other seven are
  translated from the reviewed English and need a native review before release (not blocking the merge).
- Visual system: the app's new Fil theme (vocdoni-app `feat/fil-voter-flow`), accent configurable per deployment.
  Design proposal by Fable, approved from mockups (see Design references).
- System fonts only, no web fonts.
- Logos: Vocdoni PNG variants rendered from the app's `Logo.tsx` SVG, served by this backend. Organization logo beside
  Vocdoni's in organization mail (invite_admin and members_import_done).
- Invite email: no invitation code, button + copy-paste link only.
- Product name in running text: "Vocdoni App", in every language.
- Support subject: "New ticket · {Type} · {Title}" (localized).
- Members import: at most 10 issues listed, then "and N more"; the button says "Review the member base" and opens the
  app's member base. No CSV attachment. Completion shown as a date only, no time.
- Ignore the unpublished local branch `worktree-email-logo-import-limits` (commit 1e5193a); this work starts from main.

## Problem

The backend sends eight kinds of email, each as three hand-kept HTML files plus three YAML files (`assets/*.html`,
`assets/*.yaml`, 48 files). Today:

- **Broken in dark mode.** Black outer background, no `color-scheme` meta, dark-mode overrides only in a `<style>`
  block. Gmail and Outlook partially invert it and render dark text on black (screenshot in #817).
- **Fragile in common clients.** Layout is `div` + class CSS (`assets/verification_account_en.html:15-260`); Gmail with
  non-Google accounts strips `<style>`, Outlook desktop ignores `max-width`/`border-radius`/`div` layout. The preheader
  sits inside `<head>` (`:11-13`) where clients drop it. A Google Fonts `<link>` is loaded that most clients ignore.
- **Logo depends on a third-party IPFS gateway** (`mypinata.cloud` URL in every template), white artwork that vanishes
  if the background turns white. `assets/images/vocdoni_logo_mailing.png` is embedded but unused.
- **Wrong or misleading copy.**
  - `forgot_password_*.html` says the link expires in 5 days; the code expires with `otpExpiry` (default 15 min,
    `api/users.go:1038`, `cmd/service/main.go:99`).
  - `support_*.html` tells the reader "Do not reply to this email"; the mail goes to support@vocdoni.org with the user
    in CC and Reply-To (`api/organizations.go:476-500`).
  - `members_import_done_*` says "completed successfully" even with errors, lists every error (thousands of lines for
    a bad file) and prints `CompletedAt` as Go's `time.Time.String()` (`api/org_members.go:46-55`).
  - The OTP email receives `ExpiryTime` but never shows it, and it is built with `Duration.String()` ("15m0s",
    `csp/auth.go:80`).
  - Title Case in Catalan/Spanish/English subjects, "Siusplau"/"Si us plau" mixed, "fes click", gendered "convidat",
    "Vocdoni App" / "aplicació Vocdoni" / "plataforma Vocdoni" used interchangeably.
- **Only en/es/ca.** `apicommon.SupportedLangs` (`api/apicommon/lang.go:12`) has three languages; the app ships ten.
  A Basque or French voter gets the OTP email in English.
- **Duplication.** The same ~250 lines of CSS are copied into 24 files and have drifted.
- **MIME.** Both parts are declared `Content-Transfer-Encoding: 7bit` but carry UTF-8 with accents and long lines
  (`notifications/smtp/smtp.go:233-245`), which violates RFC 2045 and lets relays mangle long lines.
- **No way to look at an email** without sending it.

## Goals

1. Every email renders correctly and legibly in light and dark mode in Apple Mail (macOS/iOS), Gmail (web, iOS,
   Android, Google and non-Google accounts), Outlook (desktop Windows, Outlook.com, iOS/Android) and Yahoo, at widths
   from 320px to 600px+, following the approved Fil mockups.
2. One layout and one set of components; each email is a content block; all copy lives in one file per language.
3. Reviewed, consistent copy in ca/es/en and complete translations in the 10 app locales.
4. Every email is bounded in size and length whatever the data (the edge-case rules below).
5. Vocdoni logos served by this backend; organization logos shown only when a mail client can display them.
6. Correct MIME encoding.
7. A preview command and tests that render every email in every language and state.

## Non-goals

- Per-organization time zones, an import-errors page in the app, CSV or any other attachment.
- New kinds of email, marketing mail, unsubscribe headers, open/click tracking (stays disabled).
- Stripe's own invoice emails.
- Changing when or to whom each email is sent, rate limits, code formats, or the notification queue.
- The SMS provider and its `[Label]` prefix logic (only the OTP text that feeds it changes).

## Current state

- **Inventory** (`notifications/mailtemplates/definitions.go`):

  | Template | Recipient | Trigger | Call site |
  |---|---|---|---|
  | `verification_account` | new user | sign-up, resend | `api/users.go:108,364,391` |
  | `forgot_password` | user | password reset | `api/users.go:866` via `mailUserCode` |
  | `password_setup` | OAuth-only user | add a password | `api/users_password_setup.go:50` |
  | `verify_email_change` | user, new address | email change | `api/users.go:618` |
  | `invite_admin` | invitee | admin invitation | `api/organization_users.go:194,470` |
  | `members_import_done` | admin who imported | bulk import finished | `api/org_members.go:57` |
  | `support` | support@vocdoni.org, user in CC | support ticket | `api/organizations.go:476` |
  | `verification_code_otp` | voter | CSP email/SMS challenge | `csp/notifications/challenge.go:114` |

- **Loading and rendering** (`notifications/mailtemplates/templates.go`): `Load()` pairs `assets/<name>_<lang>.html`
  with `<name>_<lang>.yaml` from the embedded FS (`embed.go`); `MailTemplate.Localized(lang)` falls back to
  `DefaultLang` ("en"); `ExecTemplate(data)` renders the HTML with `html/template` and the YAML `subject`/`body` with
  `text/template`, returning `notifications.Notification{Subject, Body, PlainBody}`. `ExecTemplate` is also used for
  SMS, whose senders read only `PlainBody`; `ExecPlain` is exercised only by tests. `Load()` runs only inside the
  SMTP-configured block of `cmd/service/main.go:270-298`.
- **Default OTP logo**: `csp/handlers.DefaultOrgLogo` (`csp/handlers/handlers.go:14-18`) is the same IPFS gateway URL,
  passed as the OTP organization logo when an organization has none (`csp/handlers/processes.go:1137-1143`).
- **Stored logos**: `meta["logo"]` is a multilingual map read through `db.Organization.LogoURL()`
  (`db/types.go:204-208`). Logos uploaded through this backend are absolute `{ServerURL}/storage/{md5}.{png|jpeg}`
  URLs (`objectstorage/handlers.go:188-190`, `objectstorage/objectstorage.go:198-215`); `objectstorage` `LocalName`
  (`objectstorage/objectstorage.go:143-165`) recognises them.
- **Tests and SMTP output**: `csp/testutil/smtp.go:79-95` returns MailHog's raw MIME body and many tests regex it
  (`api/api_test.go:96`, `api/lang_test.go`, `csp/auth_test.go`, `csp/notifications/*_test.go`).
- **Language** (`api/apicommon/lang.go:27`): `?lang=` on public endpoints, else `org.DefaultLang`, else "en".
  `IsValidLang` also validates an organization's default language (`api/organizations.go:38`) and the supported list
  is exposed by an API response (`api/organizations.go:362`).
- **Sending** (`api/notifications.go:17-50`): `sendMail` localizes, renders and pushes onto the notification queue.
  The support and OTP paths render directly. SMTP composition is in `notifications/smtp/smtp.go:193-255`.
- **SMS** (`notifications/twilio/sms.go:126-142`): the OTP `PlainBody` is the SMS text, optionally prefixed with
  `[Label] `.
- **URLs**: `ServerURL` (public URL of this API, `cmd/service/main.go:61`, normalized in `api/api.go:286`) and
  `WebAppURL` (`:66`). Organization logos come from `org.Meta["logo"]` (`api/apicommon/types.go:254`).
- **Tests**: `notifications/mailtemplates/mailtemplates_test.go` (663 lines) checks loading and rendering per template.

## Design

### Files

```
assets/mail/
  layout.html            html/template: document shell, header, card, footer, dark-mode CSS, MSO fallbacks
  components.html        {{define}} blocks: heading, paragraph, code box, button (bulletproof/VML), fallback link,
                         key-value table, notice, issue list, org logo tile
  <name>.html            one per email: only {{define "content"}} composed from components
  i18n/<lang>.yaml       one per locale: per-email subject, preheader, strings and plain-text body; shared strings
  images/                vocdoni-{wordmark,mark}-{black,white,black-halo}@{2x,3x}.png
scripts/mail-logos/      the generator for images/ from vocdoni-app's Logo.tsx paths (not run at build time)
```

The 48 old `assets/*.html|*.yaml` files, the IPFS URL and `assets/images/vocdoni_logo_mailing.png` are removed.
`csp/handlers.DefaultOrgLogo` is removed too: `orgNameAndLogo` returns an empty logo when the organization has none,
and the OTP email shows the monogram/no-logo layout (the `DefaultOrgName` "Vocdoni" fallback stays).

### Rendering

- Public Go surface stays: `mailtemplates.<X>Notification.Localized(lang).ExecTemplate(data)` returns
  `*notifications.Notification{Subject, Body, PlainBody}` and `ExecPlain(data)` stays for SMS. `Load()` also loads
  and validates the layout, components and every locale at start-up and fails if anything is missing or malformed.
- `mailtemplates.Load()` moves out of the SMTP-only block in `cmd/service/main.go` and runs unconditionally at
  start-up whenever email or SMS notifications are configured; a load or validation failure is fatal.
- The SMS challenge path (`csp/notifications/challenge.go:114`) uses `ExecPlain` for `SMSChallenge`, skipping the
  HTML render; `ExecPlain` stays public.
- Translators never write HTML. String values may interpolate fields (`{{.Organization}}`). Emphasis is `*text*` →
  `<strong>text</strong>`; a literal asterisk is `\*`; nesting, unmatched markers or any other markup in a string fail
  `Load()`. All interpolated data is HTML-escaped. A string missing in a locale falls back to English for that string,
  and a test fails if any locale is missing a key.
- Brand values (accent colour, derived dark accent text, logo base URL, site URL, support address) are held by the
  mailtemplates package: `Load()` becomes `Load(brand mailtemplates.Brand)`, and `mailtemplates.DefaultBrand()` returns
  the defaults (accent `#1d62e9`, dark accent text `#6a99f1`) so existing test callers change mechanically to
  `Load(mailtemplates.DefaultBrand())`. `ExecTemplate`/`ExecPlain` merge the brand into the template context under a
  reserved key (`.Brand`), so call-site data structs are untouched. `cmd/service/main.go` fills the Brand from the new
  flags; `cmd/mailpreview` from `-accent`.
- New configuration (flags/env, with defaults that need no change in existing deployments):
  - `mailAccentColor`: `#rrggbb`, default `#1d62e9`. The dark-mode link colour is derived so it keeps ≥ 4.5:1 on the
    dark card; the button label is white or black, whichever passes 4.5:1 on the accent.
  - `mailAssetsURL`: base URL of the logo PNGs, default `{ServerURL}/mail/assets`.
- This backend serves `assets/mail/images/*` at `GET /mail/assets/{file}` (public, no auth, immutable cache headers,
  only files present in the embedded directory). Caching: `Cache-Control: public, max-age=86400` plus a strong `ETag`
  (content hash); if a logo's artwork ever changes, it ships under a new filename.

### Visual system (normative values from the approved mockups)

- Table layout, 600px max width, all styling inline; `<style>` only for `prefers-color-scheme: dark`,
  `[data-ogsc]/[data-ogsb]` (Outlook.com) and the ≤ 480px media query. `<meta name="color-scheme" content="light dark">`
  and `supported-color-schemes`. Preheader hidden in `<body>`. `lang` set to the locale. `role="presentation"` tables.
  MSO conditional ghost table and a VML button for Outlook desktop.
- Colours (Fil token → light / dark): page `bg.subtle` #fafafa / #0a0a0a; card `bg.panel` #ffffff / #171717 with a 1px
  ring #e5e5e5 / #262626; text `fg` #171717 / #ededed; muted #4d4d4d / #b4b4b4; captions #6b6b6b / #999999; code box
  field #ffffff / #0a0a0a with ring #8f8f8f / #707070 (never used as text); accent fill (button) in both modes; links
  accent / dark accent text, not underlined, weight 500; error text #ca2a30 / #f97770. Card radius 12px, controls 6px.
  No shadows. All text pairs ≥ WCAG AA.
- Type: system stack `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Helvetica, Arial,
  sans-serif`; mono `ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', 'Courier New', monospace`.
  Title 24/30 semibold (22/28 mobile), body 16/24, secondary 14/20, caption 13/19, code 28/36 mono 600 (24 mobile),
  button 15/20 semibold with ≥ 44px tap height.
- Header: Vocdoni wordmark `black-halo` by default, swapped to `white` by the dark-mode CSS. Organization mail
  (invite_admin, members_import_done): co-brand header, Vocdoni wordmark left and the organization logo on a white tile
  with a 1px ring right. OTP: the organization logo (or monogram) and name centred above the card as protagonists;
  Vocdoni only in the footer ("secured by Vocdoni").
- Footer: why the reader got the email, "Vocdoni, …" line linking https://vocdoni.io, support@vocdoni.org.

### Per-email content

Final Catalan subjects, preheaders and body copy are in the mockups' DESIGN.md and HTML (Design references); English is
the source for the other locales. Behaviour that changes:

- Every code email shows the code, its expiry in words ("15 minuts", "5 dies") from the real duration, and an
  "if it wasn't you" line. Durations are formatted per locale by a helper, never `Duration.String()`: whole minutes,
  hours or days (rounding up), with unit words in the shared-strings section of each `i18n/<lang>.yaml` as
  `duration.{minute,hour,day}.{one,other}`. No new dependency.
- `forgot_password` states the real `otpExpiry`.
- `invite_admin`: no code; button + fallback link; expiry (5 days); co-brand header.
- `members_import_done`: four states chosen in Go, not in the template: all added; added with issues; nothing added
  (all rows failed, never says "completed"); empty file. Shows processed / added / issues counts, at most 10 issues
  then "and N more", a "Review the member base" button to the existing `/admin/memberbase/` link, and the completion
  date (no time).
- `support`: an internal ticket summary that also reads well for the user in CC; no "do not reply" line; subject
  "New ticket · {Type} · {Title}".
- `verification_code_otp`: shows `ExpiryTime` formatted from the TTL; plain text doubles as SMS (limits below).

### Edge-case rules (requirements)

| Case | Rule |
|---|---|
| Import with many issues | Go passes at most 10 issues plus the remaining count; the template never ranges over the full list |
| Long issue text | each issue truncated to 140 characters (rune-safe) with "…" |
| All rows failed / zero rows | own subject and heading, never "completed"/"successfully"; zero rows says the file had no rows |
| Empty user name | greeting without a name |
| Missing organization name | the "at {Org}" clause is omitted from subject and body |
| Email size | rendered HTML ≤ 60 KB for every fixture, including the worst cases (Gmail clips at ~102 KB) |
| Long unbroken values (emails, URLs, tokens) | wrap inside the card at 320px; never widen the layout |
| Subjects | ≤ 78 characters after interpolation; interpolated user text is truncated with "…" to fit |
| Support description | line breaks preserved; capped at 1,200 characters with a note that the full text is on the ticket |
| Organization logo | the URL is `org.LogoURL()`. Shown only if (a) `objectstorage` `LocalName(url)` reports a local storage reference, rendered as the canonical `{ServerURL}/storage/{name}` URL, or (b) it is an absolute https URL; in both cases the path ends in .png/.jpg/.jpeg/.gif/.webp. Never SVG, `data:` or plain http. Rendered with `height` 32 (44 in the OTP tile) and `style="max-width:140px;width:auto"`. Otherwise the no-logo layout; never a broken-image placeholder for a rejected URL |
| Organization logo in Outlook desktop | the org logo and its tile are wrapped in an MSO conditional hide, so Outlook desktop (Word engine, which cannot size an image of unknown aspect ratio) shows the Vocdoni-only header; the mockups' fixed width/height pairs were per-sample and are not reproducible server-side |
| Empty link (URL build failure, `api/org_members.go:41-44`) | the button and fallback link are omitted; the mail still sends |
| OTP SMS | ≤ 134 characters including the `[Label] ` prefix when configured (2 UCS-2 segments); organization name truncated to 40 characters |
| Dates | locale-formatted date only, in Europe/Madrid (fixed). The mailtemplates package imports `time/tzdata` so rendering never depends on host tzdata; a `LoadLocation` failure is a `Load()` failure |
| Unknown or malformed language | falls back to English (today's behaviour) |

### Languages

Language rule (unchanged, owner-confirmed): mail about an organization goes in the organization's `DefaultLang`; the
voter OTP goes in the voting page's language (`?lang=`) when the page sends one, else the organization's, else English
(`apicommon.NotificationLang`). The only change is which values are valid.

`SupportedLangs` grows to the app's ten locale codes, exactly the lowercase folder names of vocdoni-app
`src/i18n/locales` (`ca de el en es eu fr it pt pt-br`). Language codes are lowercased once at the boundaries:
`setLang` stores `strings.ToLower` of `?lang=`, `validateDefaultLang` lowercases before validating and the lowercased
value is what is stored, and `NotificationLang` lowercases `org.DefaultLang` on read for pre-existing rows. Everything
past those points (`IsValidLang`, the i18n file lookup) is exact-match lowercase. This widens what an organization may
set as its default language and the list `GET /organizations/languages` returns (`api/organizations.go:38,356-364`),
which is intended; no endpoint or field changes.

Follow-up outside this repo (not blocking): confirm the new voter page in vocdoni-app passes the page language to the
CSP auth calls (the integrator SDK's `ApiClient` has a `lang` option); if it does not, the voter gets the
organization's language until vocdoni-app sets it.

### MIME

Text and HTML parts use `Content-Transfer-Encoding: quoted-printable` (Go `mime/quotedprintable`). The message gets
`Date` and `Message-ID` headers. Everything else (multipart/alternative, headers, tracking filter) is unchanged.

### Preview and tests

- `go run ./cmd/mailpreview -out <dir>` renders every email × locale × fixture state (including the edge cases above)
  to HTML, the plain text and the subject, plus an `index.html`, using the default brand and optional `-accent`.
- Tests: every locale has every key and no extra ones; every template renders for every locale and fixture without
  error; no rendered email exceeds 60 KB; subjects ≤ 78 chars; OTP plain text ≤ 134 chars with a 40-char org name;
  interpolated HTML is escaped (a `<script>` in a support description arrives escaped); issue list capped at 10; logo
  URL filter cases; MIME parts decode back to the original UTF-8.
- `csp/testutil/smtp.go` `FindEmail` parses the returned message (`net/mail` + `mime/multipart` +
  `mime/quotedprintable`) and returns the decoded text and HTML parts; existing assertions that regex the body
  (`api/api_test.go:96`, `api/lang_test.go`, `api/users_test.go:55-70`, `csp/auth_test.go`, `csp/notifications/*_test.go`)
  keep matching decoded UTF-8 and are not rewritten one by one.

### Alternatives considered

| Option | Why not |
|---|---|
| Keep 24 hand-kept HTML files, redesign each | owner chose the shared layout; duplication is what made them drift |
| MJML with a compile step | adds Node to a Go build; the component set is small enough to hand-write once |
| Web fonts (Commissioner/Inter) | most clients ignore them; owner chose system fonts |
| Embed logos as CID attachments | bigger messages, some clients show them as attachments; hosting on our own API is enough |
| Check organization logos over the network before sending | network dependency and SSRF surface in the send path; the URL rule is enough |
| CSV of all import issues | owner decided against attachments |

## Compatibility and rollout

- No database or persisted format change. Notifications already queued keep their rendered bodies.
- API: the supported-language list and accepted organization default languages grow from 3 to 10 codes; existing
  values stay valid. New public route `GET /mail/assets/{file}`.
- New flags `mailAccentColor`, `mailAssetsURL`, all with defaults; no deployment needs new config.
- API consumers can observe the longer language list, so run `make swagger` if any annotation names the languages,
  and the PR description records the docs/SDK impact (suggest a vocdoni.io `stage` docs note and an
  integrator-SDK check of any language enum; the owner decides whether to open them).
- Ships in one PR. Rollback: revert the PR.
- Release note: the seven new locales are machine-assisted until a native review.

## Failure modes and security

- Untrusted input in emails: support title/description, organization names, member import error strings, user names,
  organization logo URLs. All are escaped by `html/template`; the logo URL passes the allow-list rule above; nothing
  untrusted reaches a `style`, `href` scheme or header unescaped. Subject lines keep `encodeHeader` (CR/LF stripped).
- A missing string or template must fail at `Load()` (start-up), not when a user triggers the mail.
- `/mail/assets/` serves only embedded files (no path traversal, no directory listing).
- Size and length bounds are enforced in Go before rendering, so a hostile import file cannot produce a huge email.

## Acceptance criteria

- [ ] AC1: `go test ./notifications/... ./api/...` passes, including the new locale-completeness, size, subject-length,
      SMS-length, escaping, issue-cap, logo-filter and MIME round-trip tests.
- [ ] AC2: `go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview` renders 8 emails × 10 locales plus the edge
      fixtures without error; the controller screenshots ca/en light and dark at 375 and 600px and they match the
      approved mockups.
- [ ] AC3a: `git grep -l 'mypinata\|fonts.googleapis'` over the whole repo returns nothing.
- [ ] AC3b: a unit test on `composeBody` asserts both parts declare `quoted-printable`, the message carries `Date` and
      `Message-ID` headers, and decoding the parts round-trips the original UTF-8.
- [ ] AC4: a 3,000-issue import fixture renders ≤ 60 KB with 10 issues and "and 2,990 more".
- [ ] AC5: `GET /mail/assets/vocdoni-wordmark-black-halo@2x.png` returns 200 with an image content type; an unknown
      file or `../` path returns 404.
- [ ] AC6: the full test suite (`go test ./...`) and `golangci-lint run` pass.
- [ ] AC7: one real send per email through the dev SMTP to test inboxes is checked by the owner before release in Gmail
      (web and app), Apple Mail and Outlook.com (light and dark); Outlook desktop on Windows for the two code emails and
      invite_admin, and Yahoo once for verification_account, if the owner has access (otherwise the PR says they were
      not checked). Each client either matches the mockups or degrades to the documented fallback (no radius/max-width
      and no org logo in Outlook desktop, no dark swap in Gmail IMAP).

## Constraints

- Go conventions of this repo (`AGENTS.md`); conventional commits `type(scope): lowercase summary`, no AI attribution
  or `Co-authored-by` trailers; commit with explicit paths, never `git add -A`; never push.
- No new Go dependencies unless the plan justifies one.
- Copy rules: sentence case, no em dash, "Vocdoni App", gender-neutral, calm voice (vocdoni-app `BRAND.md`).
- Do not touch the notification queue, rate limits, code generation or who receives what.

## Design references (non-normative)

Approved mockups, gallery and the full copy: `~/.cache/saas-backend/mockups/` (`index.html`, `DESIGN.md`,
`emails/*.html`); generator `~/.cache/saas-backend/mockup-tools/build.py`. Differences decided after the mockups:
import button is "Review the member base" to `/admin/memberbase/`; completion shows the date only, in the fixed
Europe/Madrid zone (the mockups' "deployment zone" note is superseded); no org logo in Outlook desktop.

## Open questions

None.
