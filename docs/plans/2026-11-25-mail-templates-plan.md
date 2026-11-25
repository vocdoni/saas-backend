# SaaS mailing templates redesign: implementation plan

Spec: docs/specs/2026-11-25-mail-templates-design.md (approved 2026-11-25; Fable spec audit folded in, see
.delivery/2026-11-25-mail-templates/audit-spec.md)
Class: architectural
Repos and branches: /Users/ferran/Repos/saas-backend → 817-email-redesign (base 896c3b0)

Approved visual design (non-normative, for the controller's AC2 comparison): `~/.cache/saas-backend/mockups/`
(`DESIGN.md`, `emails/*.html`, `shots/`), generator `~/.cache/saas-backend/mockup-tools/build.py`. The HTML in
this plan is a port of `build.py`'s `layout()`, `style()`, `heading()`, `para()`, `muted()`, `code_box()`,
`button()`, `link()`, `fallback_link()`, `kv_table()`, `notice()`, `header_*()` and `footer_*()`; do not
re-port it, paste the files below.

Every code block in this plan was applied, built and tested task by task in a scratch worktree
(`~/.cache/saas-backend/scratch/plan-check`, one commit per task on top of 896c3b0): `go build ./...`,
`go vet ./...`, the Docker-free tests named in each task, `golangci-lint run` (v2.15.0) on the touched
packages (0 issues), `./scripts/check-qt-patterns.sh`, `go mod tidy` (no diff), `make swagger` and
`go run ./cmd/mailpreview`. The Docker suites (`api/`, `csp/`, `csp/notifications/`, `objectstorage/`) were
only compiled and vetted there: the drafting machine has no Docker. The implementer runs them where the
task says so.

Decisions this plan takes where the spec leaves room (binding for every task):
- D1. Strings live in `assets/mail/i18n/<lang>.yaml`; the plain-text *structure* (which strings, in which
  order, conditionals) lives in `assets/mail/<name>.txt` (text/template blocks `subject`, `preheader`,
  `plain`) next to `<name>.html` (html/template blocks `header`, `content`, `footer`). Translators never
  write template logic or HTML.
- D2. A string holds literal text, `{{.Field}}` (an exported data field, nothing else), `*emphasis*` and `\*`.
  Count-dependent strings are plurals `{one: "...", other: "..."}`. A locale may only use fields its English
  string uses. `Load` fails on malformed strings, on a template key missing in English and on any fixture
  that does not render in any language; missing keys in a locale fall back to English per key.
- D3. Templates see the call-site data as a map plus two reserved keys: `.Brand` (spec) and `.L`, the
  localizer (`.L.HTML "key"`, `.L.Text`, `.L.Subject`, their plural `...n` forms, `.L.V` to format a value).
  Call sites pass named types from `mailtemplates` (`CodeData`, `InviteData`, `MembersImportData`,
  `SupportData`, `OTPData`).
- D4. Values are formatted by type: `time.Duration` → whole minutes/hours/days rounded up, in words;
  `time.Time` → date only in Europe/Madrid; integers → grouped digits. The words live in the `duration`,
  `date` and `number` sections of each locale file.
- D5. Subjects are fitted to 78 characters by truncating interpolated values (longest first, with "…"),
  never the translated text.
- D6. The organization logo filter runs inside the renderer (`.L.OrgLogo`), so no call site can bypass it;
  call sites pass `org.LogoURL()` as is.
- D7. Missing organization name: invite and import use `org.DisplayName()` (no hex-address fallback) and the
  `*_no_org` strings; support keeps today's address fallback; the OTP keeps `DefaultOrgName` "Vocdoni".
- D8. The English failed/empty import copy never says "completed" ("No members could be imported", row label
  "Finished"); the approved Catalan keeps "no s’ha pogut completar" and "Finalitzada" (R16). The import greeting uses
  the first name only.
- D9. OTP SMS text (English): ‘Code to vote: {Code}. Expires in {Exp}.’ The SMS names no organization. Every locale’s `sms` string must render in ≤ 64 characters with a 6-digit code and ‘15 minutes’ (70 minus the `[STG] ` label: one UCS-2 segment), checked per locale.
- D10. The halo wordmark is drawn 120×28 (its real aspect ratio; the mockup squashed it to 26). The OTP
  header without a usable logo shows a monogram tile (first letter of the name).
- D11. AC3a is checked as `git grep -l 'mypinata\|fonts.googleapis' -- ':!docs'`: the spec and this plan
  quote both strings.
- D12. `mailAccentColor`: the dark-mode link colour is the accent mixed towards white in 1% steps until it
  reaches 4.5:1 on #171717; the default accent keeps the Fil value #6a99f1. The button label is white when it
  reaches 4.5:1 on the accent, else black.

## Global Constraints

- Commits: conventional commits `type(scope): lowercase summary`, with no AI attribution and no
  Co-Authored-By trailers.
- Never push. Commit with explicit paths (`git commit -m "…" -- <paths>`, after `git add <paths>` for new
  files and `git rm <paths>` for deleted ones); never `git add -A` or `git add .`.
- No new Go dependencies (`go.mod`/`go.sum` must not change; `go mod tidy` leaves no diff). Everything here
  uses the standard library plus modules already required (`gopkg.in/yaml.v3`, `github.com/go-chi/chi/v5`,
  quicktest).
- Build, preview and scratch output go under `~/.cache/saas-backend/`, never `/tmp`.
- Do not change who receives what, rate limits, cooldowns, code generation or the notification queue. Only
  the content of the messages, their MIME encoding and the accepted language codes change.
- Copy rules for every string: sentence case, no em dash, "Vocdoni App" for the product in running text (in
  every language), gender-neutral wording, calm voice.
- Subagents never dispatch subagents.
- Repo rules (AGENTS.md): errors with `fmt.Errorf` and `%w`, messages start lowercase, gofumpt, line length
  130, godoc on every exported name, quicktest with the right matchers (`./scripts/check-qt-patterns.sh`),
  `make swagger` after changing swag annotations or routes. Tools: `golangci-lint` v2.15.0 and `gofumpt`
  v0.12.0 (if missing: `GOBIN=~/.cache/saas-backend/bin go install
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 mvdan.cc/gofumpt@v0.12.0`). `make swagger`
  needs `swag` v1.16.5 on `PATH` (`PATH=$(go env GOPATH)/bin:$PATH make swagger`).
- Tests in `api/`, `csp/`, `csp/notifications/` and `objectstorage/` start Docker containers; always narrow
  them with `-run`. The `api` suite takes about 7 minutes in full.
- Paste code from this plan exactly. If a step's expected output differs, stop and report; do not improvise
  a different design.

## Review Focus

Line numbers are those of the finished branch (scratch replay); they shift slightly while tasks land.
- HTML escaping of untrusted input (support title/type/description, organization names, import errors, user
  names, emails): strings are escaped by `message.render` (`notifications/mailtemplates/message.go:119`) and
  everything else by html/template; the only `template.HTML` values are rendered messages (`templates.go:375`
  ff.) and the fixed MSO comments (`templates.go:181`). Untrusted line breaks go through `notice_lines`
  (`assets/mail/components.html:97`). Test: `TestEscaping`.
- The emphasis/field parser `parseMessage` (`notifications/mailtemplates/message.go:40`): only `{{.Field}}`,
  `*…*`, `\*`; no `<`/`>`; unmatched or empty emphasis fails `Load`. Test: `TestParseMessage`.
- Subject fitting `renderFitted`/`fitValues` (`message.go:144`) and `Truncate` (rune-safe).
- Organization logo filter `orgLogoURL` (`notifications/mailtemplates/data.go:56`): only
  `objectstorage.Client.LocalName` references, canonical URL, raster extension, no `/?#\` in the name; used
  only via `.L.OrgLogo` (`templates.go:425`); hidden from Outlook desktop (`components.html:13`, `:23`).
- Static asset route `ServeAsset` (`notifications/mailtemplates/assets.go:48`) behind
  `GET /mail/assets/{file}` (`api/api.go:591`): exact-name lookup in an index built from the embedded
  directory, no path handling, 404 otherwise.
- MIME composition `composeBody` (`notifications/smtp/smtp.go:196`), `writeQuotedPrintablePart` (`:259`),
  `newMessageID` (`:278`); `encodeHeader` unchanged.
- Language normalization: `apicommon.NormalizeLang` (`api/apicommon/lang.go:18`), `setLang`
  (`api/middlewares.go:262`), `validateDefaultLang` (`api/organizations.go:38`, now normalizes in place before
  storing), `NotificationLang` read side; `MailTemplate.Localized` exact match with English fallback.
- SMS length: the SMS names no organization; `NewNotificationChallenge` uses `ExecPlain` for SMS; the `[Label] ` prefix is added in `notifications/twilio/sms.go:128`. Test: `TestOTPSMSLength` (one subtest per locale, ≤ 70 with the prefix).
- The `Load` move in `cmd/service/main.go:252-262`: now unconditional and fatal, before the SMTP/Twilio blocks.
  `Load` (`notifications/mailtemplates/templates.go:77`) swaps state atomically.
- Members import bounds `NewMembersImportData` (`notifications/mailtemplates/data_import.go:69`) and its call
  site (`api/org_members.go:22`): never more than 10 issues of 140 characters reach the template.

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1. MIME quoted-printable, Date, Message-ID; decoded test inbox | — | `notifications/smtp`, `csp/testutil` | 2 |
| 2. Ten languages, lowercase at the boundaries | — | `api/apicommon/lang*.go`, `api/middlewares*.go`, `api/organizations*.go`, `api/lang_test.go` | 1 |
| 3. Rendering engine, layout, components, emails, en/ca/es strings; `Load(Brand)` at start-up | 1, 2 | `notifications/mailtemplates/`, `assets/`, `cmd/service/main.go`, 4 test files (one-line edits) | — |
| 4. Call sites and their tests | 3 | `api/users.go`, `api/organization_users.go`, `api/org_members.go`, `api/organizations.go`, `csp/…`, tests | 6 |
| 5. Logos, `GET /mail/assets/{file}`, generator, swagger | 4 | `assets/mail/images/`, `notifications/mailtemplates/assets*.go`, `api/routes.go`, `api/api.go`, `api/mail_assets*.go`, `scripts/mail-logos/`, `docs/swagger.yaml` | 6 |
| 6. Seven translations (de, el, eu, fr, it, pt, pt-br) | 3 | `assets/mail/i18n/{de,el,eu,fr,it,pt,pt-br}.yaml`, one line of `notifications/mailtemplates/mailtemplates_test.go` | 4, 5, 7 (may be split into one dispatch per language; the test line goes last) |
| 7. `cmd/mailpreview`, README, AGENTS.md, example.env | 5 | `cmd/mailpreview/main.go`, `README.md`, `AGENTS.md`, `example.env` | 6 |

Task 6 touches only YAML (plus one test line), so it can run while 4, 5 and 7 land. Tasks 4 and 5 both
touch `api/`, run them one after the other.

## Tasks

### Task 1: MIME quoted-printable parts, Date and Message-ID; decoded test inbox
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `notifications/smtp/smtp.go`,
`notifications/smtp/smtp_test.go`, `csp/testutil/smtp.go`, `csp/testutil/smtp_test.go` (new)
Goal: both MIME parts are quoted-printable and the message carries `Date` and `Message-ID` (AC3b); the test
inbox helper decodes the parts so every existing body regex keeps matching UTF-8 (spec §Preview and tests).

Step 1: write the failing tests

Change `notifications/smtp/smtp_test.go` (exact diff against the state after the previous task):

```diff
--- a/notifications/smtp/smtp_test.go
+++ b/notifications/smtp/smtp_test.go
@@ -3,10 +3,15 @@ package smtp
 import (
 	"bytes"
 	"fmt"
+	"io"
 	"mime"
+	"mime/multipart"
+	"mime/quotedprintable"
 	"net/mail"
 	"net/textproto"
+	"strings"
 	"testing"
+	"time"
 
 	qt "github.com/frankban/quicktest"
 	"github.com/vocdoni/saas-backend/notifications"
@@ -58,3 +63,49 @@ func TestComposeBodySubjectCannotInjectHeaders(t *testing.T) {
 	c.Assert(err, qt.IsNil)
 	c.Assert(decoded, qt.Equals, "Codi de Verificació - Org")
 }
+
+func TestComposeBodyQuotedPrintable(t *testing.T) {
+	c := qt.New(t)
+	se := &Email{config: &Config{FromAddress: "noreply@vocdoni.app", FromName: "Vocdoni"}}
+
+	// accented text, a line far beyond 76 characters and an "=" that QP must escape
+	plain := "Codi de verificació: 4e92af\nEnllaç: https://app.vocdoni.io/account/verify?code=4e92af&email=" +
+		strings.Repeat("mariona.soler.", 10) + "%40exemple.cat\nGràcies."
+	html := `<p style="color:#171717;">Confirma l’adreça d’<strong>Àngels</strong></p>` + strings.Repeat("<td>ç</td>", 40)
+	body, err := se.composeBody(&notifications.Notification{
+		ToAddress: "mariona@exemple.cat",
+		Subject:   "Verifica el teu compte",
+		PlainBody: plain,
+		Body:      html,
+	})
+	c.Assert(err, qt.IsNil)
+
+	msg, err := mail.ReadMessage(bytes.NewReader(body))
+	c.Assert(err, qt.IsNil)
+	date, err := msg.Header.Date()
+	c.Assert(err, qt.IsNil)
+	c.Assert(time.Since(date) < time.Minute, qt.IsTrue)
+	c.Assert(msg.Header.Get("Message-ID"), qt.Matches, `<[0-9a-f]{32}@vocdoni\.app>`)
+
+	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
+	c.Assert(err, qt.IsNil)
+	c.Assert(mediaType, qt.Equals, "multipart/alternative")
+	reader := multipart.NewReader(msg.Body, params["boundary"])
+	want := []struct{ mediaType, content string }{{"text/plain", plain}, {"text/html", html}}
+	for _, w := range want {
+		part, err := reader.NextRawPart()
+		c.Assert(err, qt.IsNil)
+		c.Assert(part.Header.Get("Content-Transfer-Encoding"), qt.Equals, "quoted-printable")
+		c.Assert(part.Header.Get("Content-Type"), qt.Equals, w.mediaType+`; charset="UTF-8"`)
+		raw, err := io.ReadAll(part)
+		c.Assert(err, qt.IsNil)
+		for line := range strings.SplitSeq(string(raw), "\r\n") {
+			c.Assert(len(line) <= 76, qt.IsTrue, qt.Commentf("encoded line too long: %q", line))
+		}
+		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
+		c.Assert(err, qt.IsNil)
+		c.Assert(strings.ReplaceAll(string(decoded), "\r\n", "\n"), qt.Equals, w.content)
+	}
+	_, err = reader.NextRawPart()
+	c.Assert(err, qt.Equals, io.EOF)
+}
```

Create `csp/testutil/smtp_test.go`:

```go
package testutil

import (
	"bytes"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestDecodeMessageBody(t *testing.T) {
	c := qt.New(t)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range []struct{ mediaType, content string }{
		{"text/plain", "El teu codi és: 4e92af\nEnllaç: https://app.vocdoni.io/account/verify?code=4e92af"},
		{"text/html", `<a href="https://app.vocdoni.io/account/verify?code=4e92af">Verifica-ho</a>`},
	} {
		part, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.mediaType + `; charset="UTF-8"`},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		c.Assert(err, qt.IsNil)
		qp := quotedprintable.NewWriter(part)
		_, err = qp.Write([]byte(p.content))
		c.Assert(err, qt.IsNil)
		c.Assert(qp.Close(), qt.IsNil)
	}
	c.Assert(w.Close(), qt.IsNil)
	// the raw body is encoded: "=" became "=3D" and "é" became "=C3=A9"
	c.Assert(body.String(), qt.Contains, "code=3D4e92af")

	decoded, err := DecodeMessageBody([]string{"multipart/alternative; boundary=" + w.Boundary()}, body.String())
	c.Assert(err, qt.IsNil)
	c.Assert(decoded, qt.Equals, "El teu codi és: 4e92af\nEnllaç: https://app.vocdoni.io/account/verify?code=4e92af"+
		"\n\n"+`<a href="https://app.vocdoni.io/account/verify?code=4e92af">Verifica-ho</a>`)

	// a single-part body is returned as is, with normalized line breaks
	decoded, err = DecodeMessageBody([]string{"text/plain; charset=UTF-8"}, "a\r\nb")
	c.Assert(err, qt.IsNil)
	c.Assert(decoded, qt.Equals, "a\nb")
}
```

Run: `go test -count=1 ./notifications/smtp/ ./csp/testutil/`
Expected: `TestComposeBodyQuotedPrintable` fails at `date, err := msg.Header.Date()` with
`mail: header not in message`, and `csp/testutil` does not compile: `undefined: DecodeMessageBody`.

Step 2: implement

Change `notifications/smtp/smtp.go` (exact diff against the state after the previous task):

```diff
--- a/notifications/smtp/smtp.go
+++ b/notifications/smtp/smtp.go
@@ -5,11 +5,14 @@ package smtp
 import (
 	"bytes"
 	"context"
+	"crypto/rand"
 	"crypto/tls"
+	"encoding/hex"
 	"errors"
 	"fmt"
 	"mime"
 	"mime/multipart"
+	"mime/quotedprintable"
 	"net"
 	"net/mail"
 	"net/smtp"
@@ -217,6 +220,12 @@ func (se *Email) composeBody(notification *notifications.Notification) ([]byte,
 		fmt.Fprintf(&headers, "Cc: %s\r\n", cc.String())
 	}
 	fmt.Fprintf(&headers, "Subject: %s\r\n", encodeHeader(notification.Subject))
+	fmt.Fprintf(&headers, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
+	messageID, err := newMessageID(se.config.FromAddress)
+	if err != nil {
+		return nil, err
+	}
+	fmt.Fprintf(&headers, "Message-ID: %s\r\n", messageID)
 	if !notification.EnableTracking {
 		fmt.Fprintf(&headers, "X-SMTPAPI: %s\r\n", disableTrackingFilter)
 	}
@@ -229,21 +238,11 @@ func (se *Email) composeBody(notification *notifications.Notification) ([]byte,
 	if err := writer.SetBoundary(boundary); err != nil {
 		return nil, fmt.Errorf("could not set boundary: %v", err)
 	}
-	// plain text part
-	textPart, _ := writer.CreatePart(textproto.MIMEHeader{
-		"Content-Type":              {"text/plain; charset=\"UTF-8\""},
-		"Content-Transfer-Encoding": {"7bit"},
-	})
-	if _, err := textPart.Write([]byte(notification.PlainBody)); err != nil {
-		return nil, fmt.Errorf("could not write plain text part: %v", err)
+	if err := writeQuotedPrintablePart(writer, "text/plain", notification.PlainBody); err != nil {
+		return nil, fmt.Errorf("could not write plain text part: %w", err)
 	}
-	// HTML part
-	htmlPart, _ := writer.CreatePart(textproto.MIMEHeader{
-		"Content-Type":              {"text/html; charset=\"UTF-8\""},
-		"Content-Transfer-Encoding": {"7bit"},
-	})
-	if _, err := htmlPart.Write([]byte(notification.Body)); err != nil {
-		return nil, fmt.Errorf("could not write HTML part: %v", err)
+	if err := writeQuotedPrintablePart(writer, "text/html", notification.Body); err != nil {
+		return nil, fmt.Errorf("could not write HTML part: %w", err)
 	}
 	if err := writer.Close(); err != nil {
 		return nil, fmt.Errorf("could not close writer: %v", err)
@@ -254,3 +253,36 @@ func (se *Email) composeBody(notification *notifications.Notification) ([]byte,
 	email.Write(body.Bytes())
 	return email.Bytes(), nil
 }
+
+// writeQuotedPrintablePart adds a UTF-8 part of the given media type to w, encoded as
+// quoted-printable so accented text and long lines survive 7-bit relays (RFC 2045).
+func writeQuotedPrintablePart(w *multipart.Writer, mediaType, content string) error {
+	part, err := w.CreatePart(textproto.MIMEHeader{
+		"Content-Type":              {mediaType + "; charset=\"UTF-8\""},
+		"Content-Transfer-Encoding": {"quoted-printable"},
+	})
+	if err != nil {
+		return fmt.Errorf("could not create part: %w", err)
+	}
+	qp := quotedprintable.NewWriter(part)
+	if _, err := qp.Write([]byte(content)); err != nil {
+		return fmt.Errorf("could not encode part: %w", err)
+	}
+	if err := qp.Close(); err != nil {
+		return fmt.Errorf("could not flush part: %w", err)
+	}
+	return nil
+}
+
+// newMessageID returns a unique RFC 5322 Message-ID on the sender's domain.
+func newMessageID(fromAddress string) (string, error) {
+	domain := "localhost"
+	if at := strings.LastIndex(fromAddress, "@"); at >= 0 && at < len(fromAddress)-1 {
+		domain = fromAddress[at+1:]
+	}
+	id := make([]byte, 16)
+	if _, err := rand.Read(id); err != nil {
+		return "", fmt.Errorf("could not generate message id: %w", err)
+	}
+	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(id), domain), nil
+}
```

Change `csp/testutil/smtp.go` (exact diff against the state after the previous task):

```diff
--- a/csp/testutil/smtp.go
+++ b/csp/testutil/smtp.go
@@ -3,9 +3,13 @@ package testutil
 import (
 	"context"
 	"encoding/json"
+	"errors"
 	"fmt"
 	"io"
+	"mime"
+	"mime/multipart"
 	"net/http"
+	"strings"
 
 	"github.com/vocdoni/saas-backend/notifications"
 	"github.com/vocdoni/saas-backend/notifications/smtp"
@@ -80,7 +84,8 @@ func (sm *SMTP) FindEmail(ctx context.Context, to string) (string, error) {
 		Items []struct {
 			ID      string `json:"ID"`
 			Content struct {
-				Body string `json:"Body"`
+				Headers map[string][]string `json:"Headers"`
+				Body    string              `json:"Body"`
 			} `json:"Content"`
 		} `json:"items"`
 	}
@@ -92,7 +97,45 @@ func (sm *SMTP) FindEmail(ctx context.Context, to string) (string, error) {
 		return "", io.EOF
 	}
 	match := mailResults.Items[0]
-	return match.Content.Body, sm.deleteMessage(ctx, match.ID)
+	decoded, err := DecodeMessageBody(match.Content.Headers["Content-Type"], match.Content.Body)
+	if err != nil {
+		return "", fmt.Errorf("could not decode message %s: %w", match.ID, err)
+	}
+	return decoded, sm.deleteMessage(ctx, match.ID)
+}
+
+// DecodeMessageBody returns the decoded text of a message body: for a multipart message, every
+// text part (quoted-printable decoded by mime/multipart) in order, joined by a blank line; for a
+// single-part message, the body as is. Line breaks are normalized to "\n" so tests can match the
+// rendered UTF-8 text.
+func DecodeMessageBody(contentType []string, body string) (string, error) {
+	if len(contentType) == 0 {
+		return strings.ReplaceAll(body, "\r\n", "\n"), nil
+	}
+	mediaType, params, err := mime.ParseMediaType(contentType[0])
+	if err != nil {
+		return "", fmt.Errorf("could not parse content type: %w", err)
+	}
+	if !strings.HasPrefix(mediaType, "multipart/") {
+		return strings.ReplaceAll(body, "\r\n", "\n"), nil
+	}
+	reader := multipart.NewReader(strings.NewReader(body), params["boundary"])
+	var parts []string
+	for {
+		part, err := reader.NextPart() // NextPart decodes quoted-printable transparently
+		if errors.Is(err, io.EOF) {
+			break
+		}
+		if err != nil {
+			return "", fmt.Errorf("could not read part: %w", err)
+		}
+		content, err := io.ReadAll(part)
+		if err != nil {
+			return "", fmt.Errorf("could not read part content: %w", err)
+		}
+		parts = append(parts, strings.ReplaceAll(string(content), "\r\n", "\n"))
+	}
+	return strings.Join(parts, "\n\n"), nil
 }
 
 // deleteMessage removes a single message from the test inbox by its ID. A blank
```

`multipart.Reader.NextPart` decodes quoted-printable by itself (it drops the header and wraps the body), which
is why `DecodeMessageBody` needs no explicit `quotedprintable.NewReader`. `FindEmail` keeps its signature and
now returns the decoded text part, a blank line, then the decoded HTML part, with `\n` line breaks. The
callers (`api/api_test.go` `waitForEmail`, `csp/auth_test.go`, `csp/notifications/*_test.go`,
`api/org_members_test.go`, `api/users_password_setup_test.go`) are not touched.

Run: `go test -count=1 ./notifications/smtp/ ./csp/testutil/ && go vet ./notifications/smtp/ ./csp/testutil/`
Expected: `ok  github.com/vocdoni/saas-backend/notifications/smtp` and `ok  …/csp/testutil` (3 and 1 top-level
tests pass), vet silent.
Run: `~/.cache/saas-backend/bin/golangci-lint run ./notifications/smtp/ ./csp/testutil/`
Expected: `0 issues.`

Step 3: commit
`git add csp/testutil/smtp_test.go && git commit -m "fix(mail): encode parts as quoted-printable and add date and message-id" -- notifications/smtp/smtp.go notifications/smtp/smtp_test.go csp/testutil/smtp.go csp/testutil/smtp_test.go`

Interfaces this task provides to later tasks: `func testutil.DecodeMessageBody(contentType []string, body
string) (string, error)`; `(*testutil.SMTP).FindEmail` returns decoded text + HTML.

### Task 2: ten languages, lowercase at the boundaries
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `api/apicommon/lang.go`,
`api/apicommon/lang_test.go`, `api/middlewares.go`, `api/middlewares_lang_test.go` (new), `api/organizations.go`,
`api/organizations_managed.go`, `api/lang_test.go`
Goal: `SupportedLangs` is the app's ten locale codes and codes are lowercased once where they enter (spec
§Languages; audit F4).

Step 1: write the failing tests

Change `api/apicommon/lang_test.go` (exact diff against the state after the previous task):

```diff
--- a/api/apicommon/lang_test.go
+++ b/api/apicommon/lang_test.go
@@ -65,6 +65,11 @@ func TestNotificationLang(t *testing.T) {
 		ctx:  context.Background(),
 		org:  nil,
 		want: DefaultLang,
+	}, {
+		name: "a stored org default is read lowercase",
+		ctx:  public(""),
+		org:  &db.Organization{DefaultLang: "PT-BR"},
+		want: "pt-br",
 	}, {
 		name: "an empty param is not a language",
 		ctx:  public(""),
@@ -84,6 +89,23 @@ func TestIsValidLang(t *testing.T) {
 	}
 	c.Assert(IsValidLang(""), qt.IsFalse)
 	c.Assert(IsValidLang("xx"), qt.IsFalse)
-	// exact match only: the API stores what it validates
+	// exact match only: the API normalizes before validating and stores what it validates
 	c.Assert(IsValidLang("EN"), qt.IsFalse)
+	c.Assert(IsValidLang("pt-BR"), qt.IsFalse)
+	c.Assert(IsValidLang("pt_br"), qt.IsFalse)
+}
+
+func TestSupportedLangs(t *testing.T) {
+	c := qt.New(t)
+	// the web app's locale folders, in this exact lowercase form
+	c.Assert(SupportedLangs, qt.DeepEquals, []string{"ca", "de", "el", "en", "es", "eu", "fr", "it", "pt", "pt-br"})
+	c.Assert(IsValidLang(DefaultLang), qt.IsTrue)
+}
+
+func TestNormalizeLang(t *testing.T) {
+	c := qt.New(t)
+	c.Assert(NormalizeLang("pt-BR"), qt.Equals, "pt-br")
+	c.Assert(NormalizeLang(" EU "), qt.Equals, "eu")
+	c.Assert(NormalizeLang(""), qt.Equals, "")
+	c.Assert(IsValidLang(NormalizeLang("PT-BR")), qt.IsTrue)
 }
```

Create `api/middlewares_lang_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestSetLangNormalizes(t *testing.T) {
	c := qt.New(t)
	var got any
	handler := new(API).setLang(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Context().Value(apicommon.LangMetadataKey)
	}))
	for query, want := range map[string]any{
		"?lang=pt-BR": "pt-br",
		"?lang=EU":    "eu",
		"?lang=ca":    "ca",
		"?lang=xx":    nil, // unsupported values are dropped
		"":            nil,
	} {
		got = nil
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users"+query, http.NoBody))
		c.Assert(got, qt.Equals, want, qt.Commentf("query %q", query))
	}
}
```

Change `api/lang_test.go` (append the Docker-backed API test) (exact diff against the state after the previous task):

```diff
--- a/api/lang_test.go
+++ b/api/lang_test.go
@@ -213,3 +213,18 @@ func assertContentMatches(t *testing.T, content, lang string, regexps map[string
 		}
 	}
 }
+
+// TestDefaultLangIsNormalized tests that an organization's defaultLang is stored lowercase, so the
+// mail templates find its strings whatever case the client sent.
+func TestDefaultLangIsNormalized(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "superpassword123")
+	orgAddress := testCreateOrganization(t, token)
+	requestAndAssertCode(200, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: "PT-BR"},
+		"organizations", orgAddress.String())
+	org := requestAndParse[apicommon.OrganizationInfo](t, "GET", token, nil, "organizations", orgAddress.String())
+	c.Assert(org.DefaultLang, qt.Equals, "pt-br")
+	// an unsupported code is still refused
+	requestAndAssertCode(400, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: "xx"},
+		"organizations", orgAddress.String())
+}
```

Run: `go test -count=1 ./api/apicommon/`
Expected: compile error `undefined: NormalizeLang`.

Step 2: implement

Replace `api/apicommon/lang.go` with:

```go
package apicommon

import (
	"context"
	"slices"
	"strings"

	"github.com/vocdoni/saas-backend/db"
)

// SupportedLangs lists the notification languages: exactly the lowercase locale codes of the
// web app (vocdoni-app src/i18n/locales), each with a string file in assets/mail/i18n.
var SupportedLangs = []string{"ca", "de", "el", "en", "es", "eu", "fr", "it", "pt", "pt-br"}

// NormalizeLang returns the canonical form of a language code: trimmed and lowercased. Codes are
// normalized once where they enter the service (the ?lang= parameter, an organization's
// defaultLang); past that point IsValidLang and the template lookup match exactly.
func NormalizeLang(lang string) string {
	return strings.ToLower(strings.TrimSpace(lang))
}

// IsValidLang reports whether lang is one of the supported notification
// languages. It matches exactly, so callers normalize with NormalizeLang first.
func IsValidLang(lang string) bool {
	return slices.Contains(SupportedLangs, lang)
}

// NotificationLang resolves the language of a notification:
//
//	public endpoints:    ?lang= -> org.DefaultLang -> DefaultLang
//	protected endpoints: org.DefaultLang -> DefaultLang
//
// ?lang= is the caller's own language, so it only applies where the caller is
// the recipient. Pass a nil org for mail sent to the user themselves.
func NotificationLang(ctx context.Context, org *db.Organization) string {
	// authenticated means protected: only that group runs the authenticator.
	if _, authenticated := UserFromContext(ctx); !authenticated {
		if lang, ok := ctx.Value(LangMetadataKey).(string); ok && lang != "" {
			return lang
		}
	}
	if org != nil && org.DefaultLang != "" {
		// rows stored before defaultLang was normalized on write
		return NormalizeLang(org.DefaultLang)
	}
	return DefaultLang
}
```

Change `api/middlewares.go` (exact diff against the state after the previous task):

```diff
--- a/api/middlewares.go
+++ b/api/middlewares.go
@@ -264,7 +264,8 @@ func (*API) setLang(next http.Handler) http.Handler {
 		ctx := r.Context()
 		// unsupported values are dropped rather than rejected, so a stale client
 		// falls back to the default language instead of failing
-		if lang := r.URL.Query().Get(string(apicommon.LangMetadataKey)); apicommon.IsValidLang(lang) {
+		lang := apicommon.NormalizeLang(r.URL.Query().Get(string(apicommon.LangMetadataKey)))
+		if apicommon.IsValidLang(lang) {
 			ctx = context.WithValue(ctx, apicommon.LangMetadataKey, lang)
 		}
 		next.ServeHTTP(w, r.WithContext(ctx))
```

Change `api/organizations.go` (exact diff against the state after the previous task):

```diff
--- a/api/organizations.go
+++ b/api/organizations.go
@@ -32,10 +32,12 @@ func writePermissionError(w http.ResponseWriter, err error) {
 	errors.ErrGenericInternalServerError.WithErr(err).Write(w)
 }
 
-// validateDefaultLang reports whether lang is a supported notification language, writing the
+// validateDefaultLang normalizes *lang in place (see apicommon.NormalizeLang), so the stored value
+// is the canonical one, and reports whether it is a supported notification language, writing the
 // error response when it is not. An empty value is accepted, meaning "unchanged".
-func validateDefaultLang(w http.ResponseWriter, lang string) bool {
-	if lang != "" && !apicommon.IsValidLang(lang) {
+func validateDefaultLang(w http.ResponseWriter, lang *string) bool {
+	*lang = apicommon.NormalizeLang(*lang)
+	if *lang != "" && !apicommon.IsValidLang(*lang) {
 		errors.ErrMalformedBody.Withf("invalid defaultLang").Write(w)
 		return false
 	}
@@ -89,7 +91,7 @@ func (a *API) createOrganizationHandler(w http.ResponseWriter, r *http.Request)
 		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
 		return
 	}
-	if !validateDefaultLang(w, orgInfo.DefaultLang) {
+	if !validateDefaultLang(w, &orgInfo.DefaultLang) {
 		return
 	}
 	if err := apicommon.ValidateMetaKeys(orgInfo.Meta); err != nil {
@@ -263,7 +265,7 @@ func (a *API) updateOrganizationHandler(w http.ResponseWriter, r *http.Request)
 		errors.ErrMalformedBody.Write(w)
 		return
 	}
-	if !validateDefaultLang(w, newOrgInfo.DefaultLang) {
+	if !validateDefaultLang(w, &newOrgInfo.DefaultLang) {
 		return
 	}
 	if err := apicommon.ValidateMetaKeys(newOrgInfo.Meta); err != nil {
```

Change `api/organizations_managed.go` (exact diff against the state after the previous task):

```diff
--- a/api/organizations_managed.go
+++ b/api/organizations_managed.go
@@ -122,7 +122,7 @@ func (a *API) createManagedOrganizationHandler(w http.ResponseWriter, r *http.Re
 		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
 		return
 	}
-	if !validateDefaultLang(w, req.DefaultLang) {
+	if !validateDefaultLang(w, &req.DefaultLang) {
 		return
 	}
 	if err := apicommon.ValidateMetaKeys(req.Meta); err != nil {
```

`GET /organizations/languages` keeps cloning `SupportedLangs`, so it now returns the ten codes;
`TestOrganizationsLanguagesHandler` compares against the variable and needs no change.

Run: `go build ./... && go vet ./api/... && go test -count=1 ./api/apicommon/`
Expected: vet silent; `ok  github.com/vocdoni/saas-backend/api/apicommon` (18 top-level tests).
Run (Docker): `go test -count=1 -run 'TestSetLangNormalizes|TestDefaultLangIsNormalized|TestOrganizationsLanguagesHandler|TestUpdateOrganizationHandler' ./api/`
Expected: `ok  github.com/vocdoni/saas-backend/api`.
Run: `~/.cache/saas-backend/bin/golangci-lint run ./api/...`
Expected: `0 issues.`

Step 3: commit
`git add api/middlewares_lang_test.go && git commit -m "feat(api): accept the ten app languages and normalize their case" -- api/apicommon/lang.go api/apicommon/lang_test.go api/middlewares.go api/middlewares_lang_test.go api/organizations.go api/organizations_managed.go api/lang_test.go`

Interfaces this task provides to later tasks: `apicommon.SupportedLangs` = `ca de el en es eu fr it pt pt-br`;
`func apicommon.NormalizeLang(string) string`; every language code reaching `mailtemplates` is lowercase.

### Task 3: rendering engine, layout, components, the eight emails and the en/ca/es strings
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `notifications/mailtemplates/{brand,message,locale,
templates,data,data_import,definitions,fixtures}.go`, `notifications/mailtemplates/{engine,mailtemplates}_test.go`,
`assets/mail/{layout,components}.html`, `assets/mail/<email>.html` and `.txt` for the 8 emails,
`assets/mail/i18n/*.yaml` (10 files), `cmd/service/main.go`; deletes the 48 `assets/*_{ca,en,es}.{html,yaml}`,
`assets/images/vocdoni_logo_mailing.png` and the old `notifications/mailtemplates/mailtemplates_test.go`;
one-line edits in `csp/csp_test.go`, `csp/notifications/challenge_test.go`, `csp/notifications/queue_test.go`,
`api/users_test.go`
Goal: one layout, one component set, a content block per email and one string file per language, with every
edge-case rule of the spec enforced in Go and checked at start-up (Goals 2–4, 7; AC1 for this package).

How the pieces fit (read once, then paste):
- `Load(brand)` reads `assets/mail/i18n/<lang>.yaml` for every `apicommon.SupportedLangs`, checks each locale
  against English (`locale.checkAgainst`), parses each email (`compile`: `layout.html` + `components.html` +
  `<email>.html` with html/template, `<email>.txt` with text/template), checks that every `.L.<Method> "key"`
  in those files exists in English with the right shape, renders every `Fixtures()` entry in every language,
  and only then publishes the state (`atomic.Pointer`, so `Load` never races a render).
- `ExecTemplate`/`ExecPlain` keep their signatures. Data (a struct, pointer or map) becomes a map; `.Brand`
  and `.L` are added and are reserved names.
- `.L.HTML "key"` renders a string as escaped HTML with `<strong>`; `.L.Text` as text; `.L.Subject` as text
  fitted to 78 characters; `HTMLn`/`Textn`/`Subjectn` take a count and pick `one`/`other`. A key without a
  dot is `<email>.<key>`; `shared.*`, `duration.*`, `date.*`, `number.*` are absolute. Components only use
  `shared.*` keys or take strings as arguments, so the key check stays valid for every email.
- html/template strips HTML comments from template text, so the Outlook conditional comments come from
  functions (`msoIf`, `msoEnd`, `notMsoIf`, `notMsoEnd`, `msoHead`). Anything between them is still parsed
  and escaped by html/template (the VML `href` included).
- The `Brand` lives in this package; `cmd/service/main.go` builds it from two new flags (`mailAccentColor`,
  `mailAssetsURL`) and the `serverURL` flag, and calls `Load` unconditionally before configuring SMTP or
  Twilio. A bad flag or a broken template stops the service.

String schema (all ten locale files follow `en.yaml` exactly; it is the schema):
- Top-level sections: `shared` (strings several emails use), `duration.{minute,hour,day}` (plurals with
  `{{.N}}`), `date.format` (fields `{{.Day}}`, `{{.Month}}`, `{{.Year}}`) and `date.months` (a list of 12),
  `number.group` (thousands separator), then one section per email named like its file.
- Fields each email's strings may use (exactly the data type's fields): `verification_account`,
  `forgot_password`, `password_setup`, `verify_email_change` → `CodeData` (`Code`, `Link`, `Email`, `Expiry`);
  `invite_admin` → `InviteData` (`Organization`, `OrganizationLogo`, `Link`, `Expiry`); `members_import_done`
  → `MembersImportData` (`Organization`, `UserName`, `Total`, `Added`, `ErrorCount`, `MoreIssues`,
  `CompletedAt`, …); `support` → `SupportData` (`Type`, `Title`, `Description`, `Email`, `Organization`);
  `verification_code_otp` → `OTPData` (`Code`, `Organization`, `ExpiryTime`). A locale string may only use
  the fields its English string uses (Load enforces it).
- Values are double-quoted YAML strings; `\\*` is a literal asterisk; `*text*` is emphasis; no HTML, no
  `{{if}}`. Count-dependent strings are `{one: …, other: …}` maps and must stay maps in every locale.
- `verification_code_otp.sms` is also the SMS: one line, ≤ 64 characters rendered with a 6-digit code and '15 minutes'; it names no organization; no `? ( ) [ ] + * | ^ $` in the English one (tests use it as a regular expression); prefer straight apostrophes.
- Catalan: avoid elision before an interpolated name ("de l’organització {{.Organization}}", not
  "d’{{.Organization}}"); months carry their preposition (`"d’abril"`).

Step 1: write the failing tests

`git rm notifications/mailtemplates/mailtemplates_test.go` (it tests the 48 files being removed), then:

Create `notifications/mailtemplates/engine_test.go`:

```go
package mailtemplates

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
)

func TestMain(m *testing.M) {
	if err := Load(DefaultBrand()); err != nil {
		panic(err)
	}
	m.Run()
}

func TestParseMessage(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		in, html, text, err string
	}{
		{in: "Hello, {{.UserName}}.", html: "Hello, Ana &lt;3.", text: "Hello, Ana <3."},
		{
			in: "Import at *{{ .Organization }}* done", html: "Import at <strong>Org &amp; Co</strong> done",
			text: "Import at Org & Co done",
		},
		{in: `5 \* 3`, html: "5 * 3", text: "5 * 3"},
		{in: "l’adreça d’*{{.Organization}}*", html: "l’adreça d’<strong>Org &amp; Co</strong>", text: "l’adreça d’Org & Co"},
		{in: "*open", err: "unmatched *"},
		{in: "a ** b", err: "empty emphasis"},
		{in: "<b>bold</b>", err: "markup is not allowed"},
		{in: "{{if .X}}x{{end}}", err: "only {{.Field}} is allowed"},
		{in: "{{.a}}", err: "only {{.Field}} is allowed"},
		{in: "stray }} here", err: "stray }}"},
		{in: `\n`, err: "unknown escape"},
	} {
		m, err := parseMessage(tc.in)
		if tc.err != "" {
			c.Assert(err, qt.ErrorMatches, ".*"+regexpQuote(tc.err)+".*", qt.Commentf("%q", tc.in))
			continue
		}
		c.Assert(err, qt.IsNil, qt.Commentf("%q", tc.in))
		values := fieldValues(map[string]string{"UserName": "Ana <3", "Organization": "Org & Co"})
		html, err := m.render(asHTML, values)
		c.Assert(err, qt.IsNil)
		c.Assert(html, qt.Equals, tc.html)
		text, err := m.render(asText, values)
		c.Assert(err, qt.IsNil)
		c.Assert(text, qt.Equals, tc.text)
	}
}

func regexpQuote(s string) string {
	return strings.NewReplacer(`*`, `\*`, `{`, `\{`, `}`, `\}`, `.`, `\.`).Replace(s)
}

func TestTruncateAndFit(t *testing.T) {
	c := qt.New(t)
	c.Assert(Truncate("Gràcia", 6), qt.Equals, "Gràcia")
	c.Assert(Truncate("Col·legi d’Enginyeria", 9), qt.Equals, "Col·legi…")
	c.Assert(utf8.RuneCountInString(Truncate(strings.Repeat("ç", 300), 140)), qt.Equals, 140)
	c.Assert(utf8.ValidString(Truncate(strings.Repeat("€", 10), 5)), qt.IsTrue)

	// short values keep their length, the long one takes what is left
	fitted := fitValues([]string{"Bug", strings.Repeat("x", 100)}, 40)
	c.Assert(fitted[0], qt.Equals, "Bug")
	c.Assert(utf8.RuneCountInString(fitted[1]), qt.Equals, 37)
	c.Assert(fitted[1], qt.Matches, `x+…`)
	c.Assert(fitValues([]string{"a", "b"}, 10), qt.DeepEquals, []string{"a", "b"})
}

func TestFormatter(t *testing.T) {
	c := qt.New(t)
	st := loaded.Load()
	f := func(lang string) formatter {
		return formatter{loc: st.locales[lang], en: st.locales["en"], zone: st.zone}
	}
	for d, want := range map[time.Duration]string{
		0:                              "1 minute",
		30 * time.Second:               "1 minute",
		15 * time.Minute:               "15 minutes",
		5*time.Minute + 30*time.Second: "6 minutes",
		59*time.Minute + time.Second:   "1 hour",
		90 * time.Minute:               "2 hours",
		24 * time.Hour:                 "1 day",
		5 * 24 * time.Hour:             "5 days",
	} {
		got, err := f("en").duration(d)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.Equals, want, qt.Commentf("%s", d))
	}
	got, err := f("ca").duration(15 * time.Minute)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "15 minuts")

	// dates are shown in Europe/Madrid: 23:30 UTC on 31 March is already 1 April there
	when := time.Date(2026, time.March, 31, 23, 30, 0, 0, time.UTC)
	got, err = f("ca").date(when)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "1 d’abril de 2026")
	got, err = f("en").date(when)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "April 1, 2026")

	for n, want := range map[int64]string{0: "0", 999: "999", 1248: "1,248", 1234567: "1,234,567", -3000: "-3,000"} {
		got, err := f("en").number(n)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.Equals, want)
	}
	got, err = f("ca").value(2990)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "2.990")
}

func TestBrand(t *testing.T) {
	c := qt.New(t)
	def := DefaultBrand()
	c.Assert(def.validate(), qt.IsNil)
	c.Assert(def.AccentDark, qt.Equals, "#6a99f1")

	b, err := NewBrand("#1D62E9", "https://api.example.org/", "")
	c.Assert(err, qt.IsNil)
	c.Assert(b.Accent, qt.Equals, DefaultAccent)
	c.Assert(b.AccentDark, qt.Equals, DefaultAccentDark)
	c.Assert(b.AccentLabel, qt.Equals, "#ffffff")
	c.Assert(b.ServerURL, qt.Equals, "https://api.example.org")
	c.Assert(b.AssetsURL, qt.Equals, "https://api.example.org/mail/assets")

	for _, accent := range []string{"#166534", "#000000", "#ffcc00", "#7f7f7f", "#e11d48"} {
		b, err := NewBrand(accent, "https://api.example.org", "https://cdn.example.org/mail/")
		c.Assert(err, qt.IsNil)
		c.Assert(b.AssetsURL, qt.Equals, "https://cdn.example.org/mail")
		c.Assert(contrast(b.AccentDark, darkCard) >= minContrast, qt.IsTrue, qt.Commentf("%s -> %s", accent, b.AccentDark))
		c.Assert(contrast(b.AccentLabel, accent) >= minContrast, qt.IsTrue, qt.Commentf("%s label %s", accent, b.AccentLabel))
	}
	b, err = NewBrand("#ffcc00", "https://api.example.org", "")
	c.Assert(err, qt.IsNil)
	c.Assert(b.AccentLabel, qt.Equals, "#000000")

	for _, bad := range []string{"", "1d62e9", "#1d62e", "#1d62e9ff", "red", "#gggggg"} {
		_, err := NewBrand(bad, "https://api.example.org", "")
		c.Assert(err, qt.ErrorMatches, "invalid mail accent colour.*")
	}
	_, err = NewBrand(DefaultAccent, "", "")
	c.Assert(err, qt.ErrorMatches, "the server URL is required")
	c.Assert(Load(Brand{}), qt.ErrorMatches, "invalid brand colour.*")
}

func TestToMap(t *testing.T) {
	c := qt.New(t)
	m, err := toMap(CodeData{Code: "x"})
	c.Assert(err, qt.IsNil)
	c.Assert(m["Code"], qt.Equals, "x")
	m, err = toMap(&struct{ Code string }{"y"})
	c.Assert(err, qt.IsNil)
	c.Assert(m["Code"], qt.Equals, "y")
	_, err = toMap(struct{ Brand string }{"z"})
	c.Assert(err, qt.ErrorMatches, `.*"Brand" field, it is reserved`)
	_, err = toMap("not a struct")
	c.Assert(err, qt.ErrorMatches, "mail data must be a struct or a map.*")
}

func TestMissingFieldFails(t *testing.T) {
	c := qt.New(t)
	_, err := VerifyAccountNotification.Localized("en").ExecTemplate(struct{ Code, Link string }{"x", "y"})
	c.Assert(err, qt.ErrorMatches, `.*the data has no field "Expiry".*`)
}

func TestOrgLogoURL(t *testing.T) {
	c := qt.New(t)
	const server = "https://api.example.org"
	for raw, want := range map[string]string{
		server + "/storage/0f3a.png":                       server + "/storage/0f3a.png",
		server + "/storage/0f3a.jpeg":                      server + "/storage/0f3a.jpeg",
		server + "/storage/0f3a.JPG":                       server + "/storage/0f3a.JPG",
		server + "//storage/0f3a.webp":                     server + "/storage/0f3a.webp",
		"/storage/0f3a.gif":                                server + "/storage/0f3a.gif",
		" " + server + "/storage/0f3a.png ":                server + "/storage/0f3a.png",
		server + "/storage/0f3a.svg":                       "",
		server + "/storage/0f3a.json":                      "",
		server + "/storage/":                               "",
		server + "/storage/a/../0f3a.png":                  "",
		server + "/storage/0f3a.png?x=.png":                "",
		"https://tracker.example.com/pixel.png":            "",
		"http://api.example.org.evil.com/storage/0f3a.png": "",
		"data:image/png;base64,AAAA":                       "",
		"https://gateway.example.com/ipfs/bafkrei":         "",
		"": "",
	} {
		c.Assert(orgLogoURL(raw, server), qt.Equals, want, qt.Commentf("%q", raw))
	}
}

func TestNewMembersImportData(t *testing.T) {
	c := qt.New(t)
	issues := make([]string, 3000)
	for i := range issues {
		issues[i] = strings.Repeat("é", 200)
	}
	for _, tc := range []struct {
		r     MembersImportResult
		state string
		shown int
		more  int
	}{
		{MembersImportResult{Total: 10, Added: 10}, ImportDone, 0, 0},
		{MembersImportResult{Total: 10, Added: 7, Errors: issues[:3]}, ImportIssues, 3, 0},
		{MembersImportResult{Total: 3250, Added: 250, Errors: issues}, ImportIssues, 10, 2990},
		{MembersImportResult{Total: 3000, Added: 0, Errors: issues}, ImportFailed, 10, 2990},
		{MembersImportResult{}, ImportEmpty, 0, 0},
	} {
		d := NewMembersImportData(tc.r)
		c.Assert(d.State, qt.Equals, tc.state)
		c.Assert(d.Issues, qt.HasLen, tc.shown)
		c.Assert(d.MoreIssues, qt.Equals, tc.more)
		c.Assert(d.ErrorCount, qt.Equals, len(tc.r.Errors))
		for _, issue := range d.Issues {
			c.Assert(utf8.RuneCountInString(issue), qt.Equals, ImportIssueMaxRunes)
		}
	}
	c.Assert(NewMembersImportData(MembersImportResult{UserName: "  "}).UserName, qt.Equals, "")
}

func TestNewSupportData(t *testing.T) {
	c := qt.New(t)
	short := NewSupportData("Bug", "Title", "line 1\nline 2", "a@b.c", "Org")
	c.Assert(short.Truncated, qt.IsFalse)
	c.Assert(short.Description, qt.Equals, "line 1\nline 2")
	long := NewSupportData("Bug", "Title", strings.Repeat("à", 5000), "a@b.c", "Org")
	c.Assert(long.Truncated, qt.IsTrue)
	c.Assert(utf8.RuneCountInString(long.Description), qt.Equals, SupportDescriptionMaxRunes)
}
```

Create `notifications/mailtemplates/mailtemplates_test.go`:

```go
package mailtemplates

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// completeLocales are the languages whose files must hold every key. Task 6 of the plan widens it
// to apicommon.SupportedLangs once the seven translations land.
var completeLocales = []string{"ca", "en", "es"}

func TestLocalesComplete(t *testing.T) {
	c := qt.New(t)
	st := loaded.Load()
	en := st.locales["en"]
	for _, lang := range completeLocales {
		loc := st.locales[lang]
		for key, ref := range en.entries {
			e, ok := loc.entries[key]
			c.Assert(ok, qt.IsTrue, qt.Commentf("%s is missing %s", lang, key))
			if ok {
				c.Assert(e.shape(), qt.Equals, ref.shape(), qt.Commentf("%s %s", lang, key))
			}
		}
		for key := range loc.entries {
			_, ok := en.entries[key]
			c.Assert(ok, qt.IsTrue, qt.Commentf("%s has %s, which English does not", lang, key))
		}
	}
}

func TestRenderAllFixtures(t *testing.T) {
	c := qt.New(t)
	for _, fx := range Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			comment := qt.Commentf("%s in %s", fx.Name, lang)
			n, err := fx.Template.Localized(lang).ExecTemplate(fx.Data)
			c.Assert(err, qt.IsNil, comment)
			c.Assert(len(n.Body) <= MaxHTMLBytes, qt.IsTrue, qt.Commentf("%s in %s: %d bytes", fx.Name, lang, len(n.Body)))
			c.Assert(utf8.RuneCountInString(n.Subject) <= MaxSubjectRunes, qt.IsTrue, qt.Commentf("%s: %q", comment, n.Subject))
			c.Assert(n.Subject, qt.Not(qt.Equals), "", comment)
			c.Assert(n.PlainBody, qt.Not(qt.Equals), "", comment)
			for _, leftover := range []string{"{{", "}}", "ZgotmplZ", "<no value>", "%!"} {
				c.Assert(n.Body, qt.Not(qt.Contains), leftover, comment)
				c.Assert(n.PlainBody, qt.Not(qt.Contains), leftover, comment)
			}
			c.Assert(n.Body, qt.Contains, `<html lang="`+lang+`"`, comment)
			c.Assert(n.Body, qt.Contains, "<!--[if mso]>", comment)
			if fx.Template != VerifyOTPCodeNotification { // the voter email shows Vocdoni only in the footer
				c.Assert(n.Body, qt.Contains, DefaultBrand().AssetsURL+"/vocdoni-wordmark-black-halo@2x.png", comment)
			}
			c.Assert(n.Body, qt.Not(qt.Contains), "/ipfs/", comment) // no third-party image gateway
			c.Assert(n.Body, qt.Not(qt.Contains), "<link", comment)  // no web fonts
		}
	}
}

func TestOTPSMSLength(t *testing.T) {
	c := qt.New(t)
	data := OTPData{Code: "472913", Organization: fixtureOrg, ExpiryTime: 15 * time.Minute}
	for _, lang := range apicommon.SupportedLangs {
		c.Run(lang, func(c *qt.C) {
			n, err := VerifyOTPCodeNotification.Localized(lang).ExecPlain(data)
			c.Assert(err, qt.IsNil)
			sms := "[STG] " + n.PlainBody // the longest label prefix the SMS sender adds
			c.Assert(utf8.RuneCountInString(sms) <= 70, qt.IsTrue,
				qt.Commentf("%d runes: %q", utf8.RuneCountInString(sms), sms))
			c.Assert(n.PlainBody, qt.Not(qt.Contains), "\n")
			c.Assert(n.PlainBody, qt.Contains, "472913")
			c.Assert(n.PlainBody, qt.Not(qt.Contains), fixtureOrg) // the SMS names no organization
			c.Assert(n.Body, qt.Equals, "") // ExecPlain renders no HTML
		})
	}
}

func TestEscaping(t *testing.T) {
	c := qt.New(t)
	data := NewSupportData(`<img src=x onerror=alert(1)>`, `"><script>alert(1)</script>`,
		"line one <script>alert(2)</script>\nline two", "a@b.c", `Org <b>&</b>`)
	n, err := SupportNotification.Localized("en").ExecTemplate(data)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), "<script>")
	c.Assert(n.Body, qt.Not(qt.Contains), "<img src=x")
	c.Assert(n.Body, qt.Not(qt.Contains), "<b>&")
	c.Assert(n.Body, qt.Contains, "line one &lt;script&gt;alert(2)&lt;/script&gt;<br>line two")
	c.Assert(n.PlainBody, qt.Contains, "line one <script>alert(2)</script>\nline two")
	c.Assert(n.Subject, qt.Equals, `New ticket · <img src=x onerror=alert(1)> · "><script>alert(1)</script>`)

	// a hostile link never becomes a javascript: URL
	n, err = VerifyAccountNotification.Localized("en").ExecTemplate(CodeData{
		Code: "<b>1</b>", Link: "javascript:alert(1)", Expiry: time.Minute,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), `href="javascript:`)
	c.Assert(n.Body, qt.Contains, "&lt;b&gt;1&lt;/b&gt;")
}

func TestImportIssueCap(t *testing.T) {
	c := qt.New(t)
	var fx Fixture
	for _, f := range Fixtures() {
		if f.Name == "members_import_done_manyerrors" {
			fx = f
		}
	}
	n, err := fx.Template.Localized("en").ExecTemplate(fx.Data)
	c.Assert(err, qt.IsNil)
	c.Assert(len(n.Body) <= MaxHTMLBytes, qt.IsTrue)
	c.Assert(regexp.MustCompile(`invalid email address`).FindAllString(n.Body, -1), qt.HasLen, ImportIssuesShown)
	c.Assert(n.Body, qt.Contains, "and 2,990 more errors")
	c.Assert(n.PlainBody, qt.Contains, "and 2,990 more errors")
	c.Assert(n.Subject, qt.Equals, "Members import completed with 3,000 errors at Ateneu Popular de Gràcia")
	c.Assert(n.Body, qt.Contains, "Review the member base")
	c.Assert(n.Body, qt.Contains, "November 25, 2026")
	c.Assert(n.Body, qt.Not(qt.Contains), "09:42")
}

func TestImportStates(t *testing.T) {
	c := qt.New(t)
	base := MembersImportResult{Organization: "Org", UserName: "Ana", Link: "https://app.example.org/admin/memberbase/"}
	for _, tc := range []struct {
		total, added int
		errors       []string
		subject      string
		heading      string
	}{
		{10, 10, nil, "Members import completed at Org", "Import completed"},
		{10, 9, []string{"row 2: bad"}, "Members import completed with 1 error at Org", "Import completed with errors"},
		{10, 0, make([]string, 10), "No members could be imported at Org", "No members could be imported"},
		{0, 0, nil, "No members could be imported at Org", "No members could be imported"},
	} {
		r := base
		r.Total, r.Added, r.Errors = tc.total, tc.added, tc.errors
		n, err := MembersImportCompletionNotification.Localized("en").ExecTemplate(NewMembersImportData(r))
		c.Assert(err, qt.IsNil)
		c.Assert(n.Subject, qt.Equals, tc.subject)
		c.Assert(n.Body, qt.Contains, ">"+tc.heading+"</h1>")
		if tc.added == 0 {
			c.Assert(strings.ToLower(n.Subject+n.PlainBody), qt.Not(qt.Contains), "completed")
			c.Assert(strings.ToLower(n.PlainBody), qt.Not(qt.Contains), "success")
		}
	}
	// no name, no organization, no link: no dangling clause, no button
	n, err := MembersImportCompletionNotification.Localized("en").ExecTemplate(
		NewMembersImportData(MembersImportResult{Total: 2, Added: 2}))
	c.Assert(err, qt.IsNil)
	c.Assert(n.Subject, qt.Equals, "Members import completed")
	c.Assert(n.PlainBody, qt.Contains, "Hello. All done!")
	c.Assert(n.Body, qt.Not(qt.Contains), "v:roundrect")
	c.Assert(n.Body, qt.Not(qt.Contains), "Review the member base")
	c.Assert(n.Body, qt.Not(qt.Contains), " at <strong>")
}

func TestSubjectFitting(t *testing.T) {
	c := qt.New(t)
	n, err := SupportNotification.Localized("ca").ExecTemplate(NewSupportData("Incidència",
		strings.Repeat("No puc publicar una votació ", 10), "x", "a@b.c", "Org"))
	c.Assert(err, qt.IsNil)
	c.Assert(utf8.RuneCountInString(n.Subject), qt.Equals, MaxSubjectRunes)
	c.Assert(n.Subject, qt.Matches, `Nou tiquet · Incidència · No puc publicar.*…`)

	n, err = VerifyOTPCodeNotification.Localized("en").ExecTemplate(OTPData{
		Code: "1", Organization: strings.Repeat("Ö", 200), ExpiryTime: time.Minute,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(utf8.RuneCountInString(n.Subject), qt.Equals, MaxSubjectRunes)
	// the body keeps the full name: it wraps, it is never cut
	c.Assert(n.Body, qt.Contains, strings.Repeat("Ö", 200))
}

func TestOrgLogoInMail(t *testing.T) {
	c := qt.New(t)
	local := DefaultBrand().ServerURL + "/storage/0f3a.png"
	data := InviteData{Organization: "Org", OrganizationLogo: local, Link: "https://app.example.org/x", Expiry: time.Hour}
	n, err := InviteNotification.Localized("en").ExecTemplate(data)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Contains, `<!--[if !mso]><!--><table role="presentation" cellpadding="0" cellspacing="0" align="right">`)
	c.Assert(n.Body, qt.Contains, `<img src="`+local+`" height="32"`)

	for _, rejected := range []string{"https://tracker.example.com/p.png", DefaultBrand().ServerURL + "/storage/x.svg", ""} {
		data.OrganizationLogo = rejected
		n, err := InviteNotification.Localized("en").ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(n.Body, qt.Not(qt.Contains), `height="32"`, qt.Commentf("%q", rejected))
		if rejected != "" {
			c.Assert(n.Body, qt.Not(qt.Contains), rejected)
		}
	}

	otp := OTPData{Code: "1", Organization: "ateneu", OrganizationLogo: "https://example.com/logo.png", ExpiryTime: time.Minute}
	n, err = VerifyOTPCodeNotification.Localized("en").ExecTemplate(otp)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), "example.com/logo.png")
	c.Assert(n.Body, qt.Contains, `font-weight:600;color:#171717;">A</td>`) // the monogram
}

func TestCodeEmailsPlainText(t *testing.T) {
	c := qt.New(t)
	// api/users_test.go builds its code regexps from the first plain-text line in English
	for mt, first := range map[MailTemplate]string{
		VerifyAccountNotification:     "Your Vocdoni verification code is: 4e92af",
		PasswordResetNotification:     "Your Vocdoni password reset code is: 4e92af",
		PasswordSetupNotification:     "Your Vocdoni password setup code is: 4e92af",
		VerifyEmailChangeNotification: "Your code to confirm your new email address is: 4e92af",
	} {
		n, err := mt.Localized("en").ExecTemplate(CodeData{
			Code: "4e92af", Link: "https://app.example.org" + mt.WebAppURI + "?code=4e92af", Expiry: 15 * time.Minute,
		})
		c.Assert(err, qt.IsNil)
		lines := strings.Split(n.PlainBody, "\n")
		c.Assert(lines[0], qt.Equals, first)
		c.Assert(lines[1], qt.Equals, "Expires in 15 minutes.")
		c.Assert(n.PlainBody, qt.Contains, ": https://app.example.org"+mt.WebAppURI+"?code=4e92af")
		c.Assert(n.Body, qt.Contains, "Expires in 15 minutes")
	}
}

func TestLocalizedFallsBack(t *testing.T) {
	c := qt.New(t)
	for _, lang := range []string{"", "xx", "EN", "pt_BR"} {
		n, err := VerifyAccountNotification.Localized(lang).ExecTemplate(CodeData{Code: "1", Expiry: time.Minute})
		c.Assert(err, qt.IsNil)
		c.Assert(n.Subject, qt.Equals, "Verify your Vocdoni account")
	}
	c.Assert(slices.Contains(apicommon.SupportedLangs, apicommon.DefaultLang), qt.IsTrue)
}
```

Run: `go test -count=1 ./notifications/mailtemplates/`
Expected: build failure, e.g. `undefined: DefaultBrand`, `undefined: Fixtures`, `undefined: parseMessage`.

Step 2: implement

2a. Remove the old assets:
`git rm assets/*_ca.html assets/*_en.html assets/*_es.html assets/*_ca.yaml assets/*_en.yaml assets/*_es.yaml assets/images/vocdoni_logo_mailing.png`
(48 files plus the unused PNG; `assets/` then holds only `mail/` after this task and Task 5.)

2b. Replace the package code. `templates.go` and `definitions.go` replace the existing files entirely;
`embed.go` at the repo root is unchanged (`//go:embed all:assets` already embeds `assets/mail/`).

Create `notifications/mailtemplates/brand.go`:

```go
package mailtemplates

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// DefaultAccent is the Fil accent (accent.solid): the button fill and the light-mode link colour.
	DefaultAccent = "#1d62e9"
	// DefaultAccentDark is the Fil dark-mode link colour (accent.fg) that goes with DefaultAccent.
	DefaultAccentDark = "#6a99f1"
	// darkCard is the card colour in dark mode, the background the dark accent text must read on.
	darkCard = "#171717"
	// minContrast is the WCAG AA ratio for body text.
	minContrast = 4.5
	white       = "#ffffff"
	black       = "#000000"
)

var hexColorRgx = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Brand holds the per-deployment values every email shares. ExecTemplate and ExecPlain expose
// it to the templates as .Brand.
type Brand struct {
	// Accent is the button fill and the light-mode link colour, as #rrggbb.
	Accent string
	// AccentDark is the dark-mode link colour, derived from Accent to keep 4.5:1 on the dark card.
	AccentDark string
	// AccentLabel is the button label colour: #ffffff or #000000, whichever reads on Accent.
	AccentLabel string
	// AssetsURL is the base URL of the Vocdoni logo PNGs, without a trailing slash.
	AssetsURL string
	// ServerURL is the public URL of this API, without a trailing slash; organization logos are
	// shown only when they are stored under it (see orgLogoURL).
	ServerURL string
	// SiteURL is the Vocdoni website linked from the footer.
	SiteURL string
	// SupportEmail is the support address shown in the footer.
	SupportEmail string
}

// DefaultBrand returns the brand of a deployment with no mail configuration, serving its logos
// from http://localhost:8080 (the serverURL flag default).
func DefaultBrand() Brand {
	return Brand{
		Accent:       DefaultAccent,
		AccentDark:   DefaultAccentDark,
		AccentLabel:  white,
		AssetsURL:    "http://localhost:8080/mail/assets",
		ServerURL:    "http://localhost:8080",
		SiteURL:      "https://vocdoni.io",
		SupportEmail: "support@vocdoni.org",
	}
}

// NewBrand returns the brand for the given accent colour (#rrggbb), public server URL and logo
// base URL. An empty assetsURL means {serverURL}/mail/assets. The dark link colour and the button
// label colour are derived from the accent.
func NewBrand(accent, serverURL, assetsURL string) (Brand, error) {
	accent = strings.ToLower(strings.TrimSpace(accent))
	if !hexColorRgx.MatchString(accent) {
		return Brand{}, fmt.Errorf("invalid mail accent colour %q, want #rrggbb", accent)
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return Brand{}, fmt.Errorf("the server URL is required")
	}
	assetsURL = strings.TrimRight(strings.TrimSpace(assetsURL), "/")
	if assetsURL == "" {
		assetsURL = serverURL + "/mail/assets"
	}
	b := DefaultBrand()
	b.Accent = accent
	b.AccentDark = darkAccent(accent)
	b.AccentLabel = labelColor(accent)
	b.ServerURL = serverURL
	b.AssetsURL = assetsURL
	return b, nil
}

// validate reports whether b holds usable colours and URLs.
func (b Brand) validate() error {
	for _, c := range []string{b.Accent, b.AccentDark, b.AccentLabel} {
		if !hexColorRgx.MatchString(c) {
			return fmt.Errorf("invalid brand colour %q", c)
		}
	}
	if b.AssetsURL == "" || b.ServerURL == "" || b.SiteURL == "" || b.SupportEmail == "" {
		return fmt.Errorf("incomplete brand: %+v", b)
	}
	return nil
}

// darkAccent lightens accent towards white in 1% steps until it reaches 4.5:1 on the dark card.
// The default accent keeps the Fil token value.
func darkAccent(accent string) string {
	if accent == DefaultAccent {
		return DefaultAccentDark
	}
	r, g, b := rgb(accent)
	for step := 0; step <= 100; step++ {
		t := float64(step) / 100
		c := hexColor(mix(r, t), mix(g, t), mix(b, t))
		if contrast(c, darkCard) >= minContrast {
			return c
		}
	}
	return white
}

// labelColor returns white when it reaches 4.5:1 on the accent, black otherwise (one of the two
// always does).
func labelColor(accent string) string {
	if contrast(white, accent) >= minContrast {
		return white
	}
	return black
}

func mix(c uint8, t float64) uint8 {
	return uint8(math.Round(float64(c) + (255-float64(c))*t))
}

func rgb(hex string) (r, g, b uint8) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return uint8(v >> 16), uint8(v >> 8), uint8(v)
}

func hexColor(r, g, b uint8) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// contrast is the WCAG 2 contrast ratio between two #rrggbb colours.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func luminance(hex string) float64 {
	r, g, b := rgb(hex)
	channel := func(c uint8) float64 {
		v := float64(c) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}
```

Create `notifications/mailtemplates/message.go`:

```go
package mailtemplates

import (
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// segmentKind tells what a piece of a parsed message is.
type segmentKind int

const (
	segmentText segmentKind = iota
	segmentField
	segmentStrongOpen
	segmentStrongClose
)

// segment is one piece of a parsed message: literal text, a {{.Field}} or an emphasis marker.
type segment struct {
	kind segmentKind
	text string // the literal text, or the field name
}

// message is a translator string parsed at Load: literal text, {{.Field}} interpolations and
// *emphasis*. Translators never write HTML, so nothing else is accepted.
type message struct {
	segments []segment
	fields   []string
}

// fieldActionRgx matches the only template action a string may hold, {{.Field}} (an exported name).
var fieldActionRgx = regexp.MustCompile(`^\{\{\s*\.([A-Z][A-Za-z0-9]*)\s*\}\}`)

// parseMessage parses a translator string. A literal asterisk is written \*; an unmatched or
// empty *emphasis*, any other escape, any other {{action}} and any < or > fail.
func parseMessage(s string) (*message, error) {
	m := &message{}
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			m.segments = append(m.segments, segment{kind: segmentText, text: text.String()})
			text.Reset()
		}
	}
	strong := false
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			match := fieldActionRgx.FindStringSubmatch(s[i:])
			if match == nil {
				return nil, fmt.Errorf("only {{.Field}} is allowed, near %q", around(s, i))
			}
			flush()
			m.segments = append(m.segments, segment{kind: segmentField, text: match[1]})
			if !slices.Contains(m.fields, match[1]) {
				m.fields = append(m.fields, match[1])
			}
			i += len(match[0])
		case strings.HasPrefix(s[i:], "}}"):
			return nil, fmt.Errorf("stray }}, near %q", around(s, i))
		case s[i] == '\\':
			if i+1 >= len(s) || s[i+1] != '*' {
				return nil, fmt.Errorf("unknown escape (only \\* is allowed), near %q", around(s, i))
			}
			text.WriteString("*")
			i += 2
		case s[i] == '*':
			flush()
			if strong {
				if last := m.segments[len(m.segments)-1]; last.kind == segmentStrongOpen {
					return nil, fmt.Errorf("empty emphasis, near %q", around(s, i))
				}
				m.segments = append(m.segments, segment{kind: segmentStrongClose})
			} else {
				m.segments = append(m.segments, segment{kind: segmentStrongOpen})
			}
			strong = !strong
			i++
		case s[i] == '<' || s[i] == '>':
			return nil, fmt.Errorf("markup is not allowed, near %q", around(s, i))
		default:
			text.WriteString(s[i : i+1])
			i++
		}
	}
	if strong {
		return nil, fmt.Errorf("unmatched * in %q", s)
	}
	flush()
	return m, nil
}

// around returns the part of s near byte offset i, for error messages.
func around(s string, i int) string {
	end := min(len(s), i+20)
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return s[i:end]
}

// renderMode is how a message is written: as HTML (escaped, emphasis as <strong>) or as text.
type renderMode struct {
	escape      func(string) string
	strongOpen  string
	strongClose string
}

var (
	asHTML = renderMode{escape: html.EscapeString, strongOpen: "<strong>", strongClose: "</strong>"}
	asText = renderMode{escape: func(s string) string { return s }}
)

// render writes the message with each field replaced by value(field), escaped as the mode says.
func (m *message) render(mode renderMode, value func(field string) (string, error)) (string, error) {
	var out strings.Builder
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			out.WriteString(mode.escape(seg.text))
		case segmentField:
			v, err := value(seg.text)
			if err != nil {
				return "", err
			}
			out.WriteString(mode.escape(v))
		case segmentStrongOpen:
			out.WriteString(mode.strongOpen)
		case segmentStrongClose:
			out.WriteString(mode.strongClose)
		default:
			return "", fmt.Errorf("unknown segment kind %d", seg.kind)
		}
	}
	return out.String(), nil
}

// renderFitted renders the message as text in at most limit characters, truncating the
// interpolated values (never the translated text) with "…", the longest first.
func (m *message) renderFitted(limit int, value func(field string) (string, error)) (string, error) {
	fixed := 0
	var values []string
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			fixed += utf8.RuneCountInString(seg.text)
		case segmentField:
			v, err := value(seg.text)
			if err != nil {
				return "", err
			}
			values = append(values, v)
		default:
		}
	}
	values = fitValues(values, limit-fixed)
	var out strings.Builder
	next := 0
	for _, seg := range m.segments {
		switch seg.kind {
		case segmentText:
			out.WriteString(seg.text)
		case segmentField:
			out.WriteString(values[next])
			next++
		default:
		}
	}
	return out.String(), nil
}

// fitValues shortens values so that together they take at most budget characters: values shorter
// than an even share keep their length and leave the rest to the longer ones.
func fitValues(values []string, budget int) []string {
	total := 0
	for _, v := range values {
		total += utf8.RuneCountInString(v)
	}
	if total <= budget {
		return values
	}
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		return utf8.RuneCountInString(values[a]) - utf8.RuneCountInString(values[b])
	})
	fitted := slices.Clone(values)
	remaining := max(budget, 0)
	for n, i := range order {
		share := remaining / (len(order) - n)
		fitted[i] = Truncate(values[i], share)
		remaining -= utf8.RuneCountInString(fitted[i])
	}
	return fitted
}

// Truncate returns s cut to at most n characters (runes), ending in "…" when it was cut.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:n-1]), " ") + "…"
}
```

Create `notifications/mailtemplates/locale.go`:

```go
package mailtemplates

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// entry is one key of a locale file: a string, a plural (one/other) or a list (date.months).
type entry struct {
	raw   string
	msg   *message
	one   *message
	other *message
	list  []string
}

// shape names the kind of an entry, for comparing a locale with English.
func (e *entry) shape() string {
	switch {
	case e.list != nil:
		return "list"
	case e.one != nil:
		return "plural"
	default:
		return "string"
	}
}

// fields lists the {{.Field}} names the entry interpolates.
func (e *entry) fields() []string {
	var out []string
	for _, m := range []*message{e.msg, e.one, e.other} {
		if m != nil {
			out = append(out, m.fields...)
		}
	}
	return out
}

// locale is the parsed i18n/<lang>.yaml, flattened to dotted keys ("shared.button_fallback",
// "verification_account.subject", "duration.minute").
type locale struct {
	lang    string
	entries map[string]*entry
}

// parseLocale parses and flattens one locale file.
func parseLocale(lang string, data []byte) (*locale, error) {
	var tree map[string]any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("could not parse i18n/%s.yaml: %w", lang, err)
	}
	loc := &locale{lang: lang, entries: map[string]*entry{}}
	for key, value := range tree {
		if err := loc.flatten(key, value); err != nil {
			return nil, fmt.Errorf("i18n/%s.yaml: %w", lang, err)
		}
	}
	return loc, nil
}

func (loc *locale) flatten(key string, value any) error {
	switch v := value.(type) {
	case nil:
		return nil // an empty section, e.g. a locale not translated yet
	case string:
		m, err := parseMessage(v)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		loc.entries[key] = &entry{raw: v, msg: m}
	case []any:
		list := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s: lists hold strings only", key)
			}
			list = append(list, s)
		}
		loc.entries[key] = &entry{list: list}
	case map[string]any:
		if isPlural(v) {
			one, err := parseMessage(v["one"].(string))
			if err != nil {
				return fmt.Errorf("%s.one: %w", key, err)
			}
			other, err := parseMessage(v["other"].(string))
			if err != nil {
				return fmt.Errorf("%s.other: %w", key, err)
			}
			loc.entries[key] = &entry{one: one, other: other}
			return nil
		}
		for sub, subValue := range v {
			if err := loc.flatten(key+"."+sub, subValue); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unsupported value %v", key, v)
	}
	return nil
}

// isPlural reports whether a map is a plural entry: exactly the string keys one and other.
func isPlural(m map[string]any) bool {
	one, okOne := m["one"].(string)
	other, okOther := m["other"].(string)
	return len(m) == 2 && okOne && okOther && one != "" && other != ""
}

// checkAgainst validates a locale against English: every key it has must exist in English with
// the same shape, interpolate only fields the English string uses, and lists keep their length.
// Missing keys are allowed here (they fall back to English); TestLocalesComplete reports them.
func (loc *locale) checkAgainst(en *locale) error {
	for key, e := range loc.entries {
		ref, ok := en.entries[key]
		if !ok {
			continue // an extra key is never used; TestLocalesComplete reports it
		}
		if e.shape() != ref.shape() {
			return fmt.Errorf("i18n/%s.yaml: %s is a %s, English has a %s", loc.lang, key, e.shape(), ref.shape())
		}
		for _, f := range e.fields() {
			if !slices.Contains(ref.fields(), f) {
				return fmt.Errorf("i18n/%s.yaml: %s uses {{.%s}}, which the English string does not", loc.lang, key, f)
			}
		}
		if e.list != nil && len(e.list) != len(ref.list) {
			return fmt.Errorf("i18n/%s.yaml: %s has %d items, English has %d", loc.lang, key, len(e.list), len(ref.list))
		}
	}
	return nil
}

// formatter renders values in one language, falling back to English per key.
type formatter struct {
	loc, en *locale
	zone    *time.Location
}

// lookup returns the entry for key in the language, else in English.
func (f formatter) lookup(key string) (*entry, error) {
	if e, ok := f.loc.entries[key]; ok {
		return e, nil
	}
	if e, ok := f.en.entries[key]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("missing string %q", key)
}

// value formats a template value for the reader: numbers grouped, durations and dates in words.
func (f formatter) value(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case time.Duration:
		return f.duration(t)
	case time.Time:
		return f.date(t)
	case fmt.Stringer:
		return t.String(), nil
	}
	if n, ok := asInt(v); ok {
		return f.number(n)
	}
	return fmt.Sprint(v), nil
}

// duration writes d in whole minutes, hours or days, rounding up and never below one minute.
func (f formatter) duration(d time.Duration) (string, error) {
	unit, n := "minute", ceilDiv(d, time.Minute)
	if n >= 60 {
		unit, n = "hour", ceilDiv(d, time.Hour)
	}
	if unit == "hour" && n >= 24 {
		unit, n = "day", ceilDiv(d, 24*time.Hour)
	}
	e, err := f.lookup("duration." + unit)
	if err != nil {
		return "", err
	}
	count, err := f.number(n)
	if err != nil {
		return "", err
	}
	return pick(e, n).render(asText, fieldValues(map[string]string{"N": count}))
}

// date writes the calendar date of t in the fixed Europe/Madrid zone, without the time.
func (f formatter) date(t time.Time) (string, error) {
	t = t.In(f.zone)
	months, err := f.lookup("date.months")
	if err != nil {
		return "", err
	}
	format, err := f.lookup("date.format")
	if err != nil {
		return "", err
	}
	return format.msg.render(asText, fieldValues(map[string]string{
		"Day":   strconv.Itoa(t.Day()),
		"Month": months.list[t.Month()-1],
		"Year":  strconv.Itoa(t.Year()),
	}))
}

// number writes n with the locale's thousands separator (number.group).
func (f formatter) number(n int64) (string, error) {
	group, err := f.lookup("number.group")
	if err != nil {
		return "", err
	}
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var out strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteString(group.raw)
		}
		out.WriteString(string(d))
	}
	return sign + out.String(), nil
}

// pick returns the plural form of e for n.
func pick(e *entry, n int64) *message {
	if n == 1 {
		return e.one
	}
	return e.other
}

func fieldValues(values map[string]string) func(string) (string, error) {
	return func(field string) (string, error) {
		v, ok := values[field]
		if !ok {
			return "", fmt.Errorf("unknown field %q", field)
		}
		return v, nil
	}
}

func ceilDiv(d, unit time.Duration) int64 {
	return max(1, int64(math.Ceil(float64(d)/float64(unit))))
}

// asInt converts any integer kind to int64.
func asInt(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), true //nolint:gosec // counts never overflow int64
	default:
		return 0, false
	}
}
```

Create `notifications/mailtemplates/templates.go`:

```go
package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	texttemplate "text/template"
	"time"

	// rendering must not depend on the host's tzdata for Europe/Madrid
	_ "time/tzdata"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications"
)

const (
	// assetsDir is the embedded directory with the layout, components, emails and strings.
	assetsDir = "assets/mail"
	// dateZone is the fixed zone dates are shown in.
	dateZone = "Europe/Madrid"
	// MaxSubjectRunes caps a subject after interpolation (RFC 5322 recommended line length).
	MaxSubjectRunes = 78
	// MaxHTMLBytes is the size budget of a rendered HTML body; Gmail clips at about 102 KB.
	MaxHTMLBytes = 60 * 1024
)

// MailTemplate is one kind of email: assets/mail/<File>.html (HTML blocks), assets/mail/<File>.txt
// (subject, preheader and plain-text blocks) and the <File> section of every i18n/<lang>.yaml.
type MailTemplate struct {
	File      string // base name, e.g. "verification_account"
	WebAppURI string // web app path the email links to, if any
}

// LocalizedTemplate is a MailTemplate in one language.
type LocalizedTemplate struct {
	file string
	lang string
}

// Localized returns the template in lang, or in apicommon.DefaultLang when lang is not a supported
// language. Callers pass normalized (lowercase) codes, see apicommon.NormalizeLang.
func (mt MailTemplate) Localized(lang string) LocalizedTemplate {
	if !apicommon.IsValidLang(lang) {
		lang = apicommon.DefaultLang
	}
	return LocalizedTemplate{file: mt.File, lang: lang}
}

// compiled holds the parsed templates of one email.
type compiled struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

// state is everything Load prepares; it is replaced atomically, so rendering never races Load.
type state struct {
	brand   Brand
	zone    *time.Location
	locales map[string]*locale
	emails  map[string]*compiled
}

var loaded atomic.Pointer[state]

// stringRefRgx finds the string keys templates pass to the .L methods.
var stringRefRgx = regexp.MustCompile(`\.L\.(HTML|Text|Subject)(n?)\s+"([A-Za-z0-9_.]+)"`)

// Load parses the layout, the components, every email and every locale, checks them, renders
// every fixture in every language, and only then makes them available. Any missing or malformed
// piece is an error, so the service fails at start-up rather than when a user triggers a mail.
func Load(brand Brand) error {
	if err := brand.validate(); err != nil {
		return err
	}
	zone, err := time.LoadLocation(dateZone)
	if err != nil {
		return fmt.Errorf("could not load the %s time zone: %w", dateZone, err)
	}
	st := &state{brand: brand, zone: zone, locales: map[string]*locale{}, emails: map[string]*compiled{}}
	for _, lang := range apicommon.SupportedLangs {
		data, err := root.Assets.ReadFile(assetsDir + "/i18n/" + lang + ".yaml")
		if err != nil {
			return fmt.Errorf("could not read the %s strings: %w", lang, err)
		}
		if st.locales[lang], err = parseLocale(lang, data); err != nil {
			return err
		}
	}
	en := st.locales[apicommon.DefaultLang]
	for _, loc := range st.locales {
		if err := loc.checkAgainst(en); err != nil {
			return err
		}
	}
	for _, mt := range All {
		c, err := compile(mt.File, en)
		if err != nil {
			return err
		}
		st.emails[mt.File] = c
	}
	for _, fx := range Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			if err := st.check(LocalizedTemplate{file: fx.Template.File, lang: lang}, fx.Data); err != nil {
				return fmt.Errorf("fixture %s in %s: %w", fx.Name, lang, err)
			}
		}
	}
	loaded.Store(st)
	return nil
}

// check renders one fixture completely, as ExecTemplate would.
func (st *state) check(lt LocalizedTemplate, data any) error {
	r, err := st.newRender(lt, data)
	if err != nil {
		return err
	}
	n, err := r.plain()
	if err != nil {
		return err
	}
	_, err = r.html(n.Subject)
	return err
}

// compile parses one email and checks that every string key its templates use exists in English
// with the shape the method expects.
func compile(file string, en *locale) (*compiled, error) {
	paths := []string{assetsDir + "/layout.html", assetsDir + "/components.html", assetsDir + "/" + file + ".html"}
	sources := ""
	for _, p := range append(paths, assetsDir+"/"+file+".txt") {
		src, err := root.Assets.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", p, err)
		}
		sources += string(src)
	}
	for _, ref := range stringRefRgx.FindAllStringSubmatch(sources, -1) {
		key := ref[3]
		if !strings.Contains(key, ".") {
			key = file + "." + key
		}
		e, ok := en.entries[key]
		if !ok {
			return nil, fmt.Errorf("%s uses the string %q, missing in i18n/en.yaml", file, key)
		}
		if want := map[string]string{"": "string", "n": "plural"}[ref[2]]; e.shape() != want {
			return nil, fmt.Errorf("%s uses %q as a %s, i18n/en.yaml has a %s", file, key, want, e.shape())
		}
	}
	h, err := htmltemplate.New("layout").Funcs(htmlFuncs).ParseFS(root.Assets, paths...)
	if err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", file, err)
	}
	t, err := texttemplate.New("text").ParseFS(root.Assets, assetsDir+"/"+file+".txt")
	if err != nil {
		return nil, fmt.Errorf("could not parse %s.txt: %w", file, err)
	}
	for _, name := range []string{"subject", "preheader", "plain"} {
		if t.Lookup(name) == nil {
			return nil, fmt.Errorf("%s.txt does not define %q", file, name)
		}
	}
	for _, name := range []string{"header", "content", "footer"} {
		if h.Lookup(name) == nil {
			return nil, fmt.Errorf("%s.html does not define %q", file, name)
		}
	}
	return &compiled{html: h, text: t}, nil
}

// htmlFuncs emit the Outlook conditional comments, which html/template would strip from the
// template text.
var htmlFuncs = htmltemplate.FuncMap{
	"msoIf":     func() htmltemplate.HTML { return "<!--[if mso]>" },
	"msoEnd":    func() htmltemplate.HTML { return "<![endif]-->" },
	"notMsoIf":  func() htmltemplate.HTML { return "<!--[if !mso]><!-->" },
	"notMsoEnd": func() htmltemplate.HTML { return "<!--<![endif]-->" },
	"msoHead": func() htmltemplate.HTML {
		return "<!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch>" +
			"</o:OfficeDocumentSettings></xml></noscript><![endif]-->"
	},
	"preheaderPad": func() htmltemplate.HTML { return htmltemplate.HTML(strings.Repeat("&zwnj;&nbsp;", 90)) },
	"dict":         dict,
}

// dict builds the argument map of a component: dict "Label" x "URL" y.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs key/value pairs")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys are strings")
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// ExecTemplate renders the subject, the HTML body and the plain-text body with data, a struct (or
// a map) whose fields the templates and strings read. The brand is added as .Brand.
func (lt LocalizedTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	r, err := newRender(lt, data)
	if err != nil {
		return nil, err
	}
	n, err := r.plain()
	if err != nil {
		return nil, err
	}
	if n.Body, err = r.html(n.Subject); err != nil {
		return nil, err
	}
	return n, nil
}

// ExecPlain renders only the subject and the plain-text body, as SMS needs.
func (lt LocalizedTemplate) ExecPlain(data any) (*notifications.Notification, error) {
	r, err := newRender(lt, data)
	if err != nil {
		return nil, err
	}
	return r.plain()
}

// rendering is one email being rendered in one language.
type rendering struct {
	file   string
	email  *compiled
	l      *localizer
	fields map[string]any
}

func newRender(lt LocalizedTemplate, data any) (*rendering, error) {
	st := loaded.Load()
	if st == nil {
		return nil, fmt.Errorf("mail templates are not loaded")
	}
	return st.newRender(lt, data)
}

func (st *state) newRender(lt LocalizedTemplate, data any) (*rendering, error) {
	email, ok := st.emails[lt.file]
	if !ok {
		return nil, fmt.Errorf("unknown mail template %q", lt.file)
	}
	loc, ok := st.locales[lt.lang]
	if !ok {
		return nil, fmt.Errorf("unknown mail language %q", lt.lang)
	}
	fields, err := toMap(data)
	if err != nil {
		return nil, err
	}
	l := &localizer{
		Lang:   lt.lang,
		file:   lt.file,
		f:      formatter{loc: loc, en: st.locales[apicommon.DefaultLang], zone: st.zone},
		brand:  st.brand,
		fields: fields,
	}
	fields["Brand"] = st.brand
	fields["L"] = l
	return &rendering{file: lt.file, email: email, l: l, fields: fields}, nil
}

// text renders one block of <file>.txt, tidied.
func (r *rendering) text(name string) (string, error) {
	var buf bytes.Buffer
	if err := r.email.text.ExecuteTemplate(&buf, name, r.fields); err != nil {
		return "", fmt.Errorf("could not render %s %s: %w", r.file, name, err)
	}
	return tidy(buf.String()), nil
}

// plain renders the subject and the plain-text body.
func (r *rendering) plain() (*notifications.Notification, error) {
	subject, err := r.text("subject")
	if err != nil {
		return nil, err
	}
	body, err := r.text("plain")
	if err != nil {
		return nil, err
	}
	return &notifications.Notification{Subject: subject, PlainBody: body}, nil
}

// html renders the HTML body, titled with the subject.
func (r *rendering) html(subject string) (string, error) {
	preheader, err := r.text("preheader")
	if err != nil {
		return "", err
	}
	r.l.Title, r.l.Preheader = subject, preheader
	var buf bytes.Buffer
	if err := r.email.html.ExecuteTemplate(&buf, "layout", r.fields); err != nil {
		return "", fmt.Errorf("could not render %s html: %w", r.file, err)
	}
	return buf.String(), nil
}

// toMap turns the call-site data (a struct, a pointer to one, or a map) into the template context.
func toMap(data any) (map[string]any, error) {
	out := map[string]any{}
	if data == nil {
		return out, nil
	}
	if m, ok := data.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	} else {
		v := reflect.ValueOf(data)
		for v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return nil, fmt.Errorf("mail data must be a struct or a map, got %T", data)
		}
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				out[field.Name] = v.Field(i).Interface()
			}
		}
	}
	for _, reserved := range []string{"Brand", "L"} {
		if _, ok := out[reserved]; ok {
			return nil, fmt.Errorf("mail data must not have a %q field, it is reserved", reserved)
		}
	}
	return out, nil
}

// tidy trims trailing spaces, collapses runs of blank lines and trims the text.
func tidy(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// localizer is .L in the templates: it renders the strings of one email in one language against
// the data, and formats values.
type localizer struct {
	// Lang is the language code, for the lang attributes.
	Lang string
	// Title is the rendered subject, for <title> and aria-label.
	Title string
	// Preheader is the rendered preheader.
	Preheader string
	file      string
	f         formatter
	brand     Brand
	fields    map[string]any
}

// HTML renders a string as HTML: text and values escaped, *emphasis* as <strong>.
func (l *localizer) HTML(key string) (htmltemplate.HTML, error) {
	s, err := l.render(key, nil, asHTML)
	return htmltemplate.HTML(s), err //nolint:gosec // render escapes all text and values
}

// HTMLn renders the plural form of a string for count n as HTML.
func (l *localizer) HTMLn(key string, n any) (htmltemplate.HTML, error) {
	s, err := l.render(key, n, asHTML)
	return htmltemplate.HTML(s), err //nolint:gosec // render escapes all text and values
}

// Text renders a string as plain text.
func (l *localizer) Text(key string) (string, error) {
	return l.render(key, nil, asText)
}

// Textn renders the plural form of a string for count n as plain text.
func (l *localizer) Textn(key string, n any) (string, error) {
	return l.render(key, n, asText)
}

// Subject renders a string as plain text in at most MaxSubjectRunes, truncating the values.
func (l *localizer) Subject(key string) (string, error) {
	return l.fitted(key, nil)
}

// Subjectn renders the plural form of a subject for count n.
func (l *localizer) Subjectn(key string, n any) (string, error) {
	return l.fitted(key, n)
}

// V formats a value for the reader (numbers, durations, dates).
func (l *localizer) V(v any) (string, error) {
	return l.f.value(v)
}

// Lines splits text into lines, so templates can keep its line breaks with <br>.
func (*localizer) Lines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// Initial returns the first letter of name, upper-cased, for the monogram tile.
func (*localizer) Initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return ""
}

// OrgLogo returns the data's OrganizationLogo when it may be shown, else "".
func (l *localizer) OrgLogo() string {
	raw, ok := l.fields["OrganizationLogo"].(string)
	if !ok {
		return ""
	}
	return orgLogoURL(raw, l.brand.ServerURL)
}

func (l *localizer) message(key string, n any) (*message, error) {
	if !strings.Contains(key, ".") {
		key = l.file + "." + key
	}
	e, err := l.f.lookup(key)
	if err != nil {
		return nil, err
	}
	if n == nil {
		if e.msg == nil {
			return nil, fmt.Errorf("string %q is not a plain string", key)
		}
		return e.msg, nil
	}
	count, ok := asInt(n)
	if !ok || e.one == nil {
		return nil, fmt.Errorf("string %q is not a plural or %v is not a count", key, n)
	}
	return pick(e, count), nil
}

func (l *localizer) render(key string, n any, mode renderMode) (string, error) {
	m, err := l.message(key, n)
	if err != nil {
		return "", err
	}
	return m.render(mode, l.field)
}

func (l *localizer) fitted(key string, n any) (string, error) {
	m, err := l.message(key, n)
	if err != nil {
		return "", err
	}
	return m.renderFitted(MaxSubjectRunes, l.field)
}

// field returns the formatted value of a data field a string interpolates.
func (l *localizer) field(name string) (string, error) {
	v, ok := l.fields[name]
	if !ok {
		return "", fmt.Errorf("the data has no field %q", name)
	}
	return l.f.value(v)
}
```

Create `notifications/mailtemplates/data.go`:

```go
package mailtemplates

import (
	"path"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/objectstorage"
)

const (
	// SupportDescriptionMaxRunes caps the support ticket description in the email.
	SupportDescriptionMaxRunes = 1200
)

// SupportData is the data of support.
type SupportData struct {
	Type         string
	Title        string
	Description  string // at most SupportDescriptionMaxRunes, see NewSupportData
	Truncated    bool   // the description was cut
	Email        string
	Organization string
}

// NewSupportData caps the description, keeping its line breaks.
func NewSupportData(ticketType, title, description, email, organization string) SupportData {
	cut := Truncate(description, SupportDescriptionMaxRunes)
	return SupportData{
		Type:         ticketType,
		Title:        title,
		Description:  cut,
		Truncated:    cut != description,
		Email:        email,
		Organization: organization,
	}
}

// OTPData is the data of verification_code_otp, by email and by SMS.
type OTPData struct {
	Code             string
	Organization     string
	OrganizationLogo string
	ExpiryTime       time.Duration
}

// orgLogoExtensions are the raster formats every mail client displays.
var orgLogoExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

// orgLogoURL returns the canonical URL of an organization logo when it may appear in mail: only a
// raster image kept in this service's object storage, so an organization cannot put a tracking
// pixel or an external image in mail sent to other people. Anything else returns "" and the
// email uses its no-logo layout.
func orgLogoURL(raw, serverURL string) string {
	storage := objectstorage.Client{ServerURL: serverURL}
	name, ok := storage.LocalName(strings.TrimSpace(raw))
	if !ok || strings.ContainsAny(name, "/?#\\") {
		return ""
	}
	ext := strings.ToLower(path.Ext(name))
	for _, allowed := range orgLogoExtensions {
		if ext == allowed {
			return storage.LocalURL(name)
		}
	}
	return ""
}
```

Create `notifications/mailtemplates/data_import.go`:

```go
package mailtemplates

import (
	"strings"
	"time"
)

const (
	// ImportIssuesShown is the most import issues an email lists.
	ImportIssuesShown = 10
	// ImportIssueMaxRunes caps each listed import issue.
	ImportIssueMaxRunes = 140
)

// Members import outcomes, chosen in Go so the template never decides what "completed" means.
const (
	ImportDone   = "done"   // every row was added
	ImportIssues = "issues" // some rows were added, some failed
	ImportFailed = "failed" // no row was added
	ImportEmpty  = "empty"  // the file had no rows
)

// CodeData is the data of the emails that carry a code and a link: verification_account,
// forgot_password, password_setup and verify_email_change.
type CodeData struct {
	Code   string
	Link   string
	Email  string        // the address the code is for
	Expiry time.Duration // how long the code stays valid
}

// InviteData is the data of invite_admin.
type InviteData struct {
	Organization     string // display name, empty when the organization has none
	OrganizationLogo string // org.LogoURL(); shown only if it passes orgLogoURL
	Link             string
	Expiry           time.Duration
}

// MembersImportData is the data of members_import_done; build it with NewMembersImportData.
type MembersImportData struct {
	Organization     string
	OrganizationLogo string
	UserName         string
	Link             string // empty when it could not be built: the button is omitted
	State            string // ImportDone, ImportIssues, ImportFailed or ImportEmpty
	Total            int
	Added            int
	ErrorCount       int
	Issues           []string // at most ImportIssuesShown, each at most ImportIssueMaxRunes
	MoreIssues       int      // ErrorCount minus the issues listed
	CompletedAt      time.Time
}

// MembersImportResult is what a finished members import reports.
type MembersImportResult struct {
	Organization     string
	OrganizationLogo string
	UserName         string
	Link             string
	Total            int
	Added            int
	Errors           []string
	CompletedAt      time.Time
}

// NewMembersImportData chooses the outcome and bounds the issue list, so a file with thousands of
// bad rows still yields a small email.
func NewMembersImportData(r MembersImportResult) MembersImportData {
	d := MembersImportData{
		Organization:     r.Organization,
		OrganizationLogo: r.OrganizationLogo,
		UserName:         strings.TrimSpace(r.UserName),
		Link:             r.Link,
		Total:            r.Total,
		Added:            r.Added,
		ErrorCount:       len(r.Errors),
		CompletedAt:      r.CompletedAt,
	}
	switch {
	case r.Total == 0:
		d.State = ImportEmpty
	case r.Added == 0:
		d.State = ImportFailed
	case len(r.Errors) > 0:
		d.State = ImportIssues
	default:
		d.State = ImportDone
	}
	shown := r.Errors[:min(len(r.Errors), ImportIssuesShown)]
	for _, issue := range shown {
		d.Issues = append(d.Issues, Truncate(issue, ImportIssueMaxRunes))
	}
	d.MoreIssues = len(r.Errors) - len(shown)
	return d
}
```

Create `notifications/mailtemplates/definitions.go`:

```go
// Package mailtemplates renders the transactional emails: one shared layout and set of
// components (assets/mail), one content block per email, and the strings of every language in
// assets/mail/i18n/<lang>.yaml.
package mailtemplates

// VerifyAccountNotification is sent when a user creates an account and needs to verify it.
var VerifyAccountNotification = MailTemplate{
	File:      "verification_account",
	WebAppURI: "/account/verify",
}

// VerifyOTPCodeNotification is the voter's CSP challenge, by email or (plain text) by SMS.
var VerifyOTPCodeNotification = MailTemplate{
	File: "verification_code_otp",
}

// PasswordResetNotification is sent when a user requests a password reset.
var PasswordResetNotification = MailTemplate{
	File:      "forgot_password",
	WebAppURI: "/account/password/reset",
}

// PasswordSetupNotification is sent when a signed-in account without a password (OAuth-only)
// asks to add one, with the code that proves it owns the mailbox.
var PasswordSetupNotification = MailTemplate{
	File:      "password_setup",
	WebAppURI: "/account/password/setup",
}

// VerifyEmailChangeNotification is sent to the new address when a user asks to change their
// account email, with the code that confirms it.
var VerifyEmailChangeNotification = MailTemplate{
	File:      "verify_email_change",
	WebAppURI: "/account/email/verify",
}

// InviteNotification is sent when a user is invited to administer an organization.
var InviteNotification = MailTemplate{
	File:      "invite_admin",
	WebAppURI: "/account/invite",
}

// SupportNotification is sent to the support desk, with the user in CC, for a new ticket.
var SupportNotification = MailTemplate{
	File: "support",
}

// MembersImportCompletionNotification is sent to the admin who ran a members import when it ends.
var MembersImportCompletionNotification = MailTemplate{
	File: "members_import_done",
}

// All lists every email; Load compiles each one.
var All = []MailTemplate{
	VerifyAccountNotification,
	PasswordResetNotification,
	PasswordSetupNotification,
	VerifyEmailChangeNotification,
	InviteNotification,
	MembersImportCompletionNotification,
	SupportNotification,
	VerifyOTPCodeNotification,
}
```

Create `notifications/mailtemplates/fixtures.go`:

```go
package mailtemplates

import (
	"fmt"
	"strings"
	"time"
)

// Fixture is a sample email: Load renders every fixture in every language, the tests check them
// and cmd/mailpreview writes them out.
type Fixture struct {
	Name     string // unique, e.g. "members_import_done_manyerrors"
	Template MailTemplate
	Data     any
}

// FixtureOrgLogo is the organization logo of the fixtures: a stored object under DefaultBrand's
// server URL, so it passes the logo filter. cmd/mailpreview swaps it for a local sample image.
const FixtureOrgLogo = "http://localhost:8080/storage/0f3a5c7e9b1d2f4a6c8e0b2d4f6a8c0e.png"

const (
	fixtureCode     = "4e92af"
	fixtureOrg      = "Ateneu Popular de Gràcia"
	fixtureLongOrg  = "Col·legi Oficial d’Enginyeria Informàtica de Catalunya i les Illes Balears"
	fixtureWebApp   = "https://app.vocdoni.io"
	fixtureLongMail = "mariona.soler.i.puigdollers.de.la.vall.del.riu@cooperativa-llum-del-nord-exemple.cat"
)

// Fixtures returns every sample email, the edge cases of the design included.
func Fixtures() []Fixture {
	completed := time.Date(2026, time.November, 25, 9, 42, 0, 0, time.UTC)
	codeEmail := func(name string, mt MailTemplate, email string) Fixture {
		return Fixture{Name: name, Template: mt, Data: CodeData{
			Code:   fixtureCode,
			Link:   fixtureWebApp + mt.WebAppURI + "?code=" + fixtureCode + "&email=" + strings.ReplaceAll(email, "@", "%40"),
			Email:  email,
			Expiry: 15 * time.Minute,
		}}
	}
	importData := func(r MembersImportResult) MembersImportData {
		r.Organization, r.OrganizationLogo, r.UserName = fixtureOrg, FixtureOrgLogo, "Mariona"
		r.Link, r.CompletedAt = fixtureWebApp+"/admin/memberbase/", completed
		return NewMembersImportData(r)
	}
	issues := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("row %d: invalid email address <user%d@example>", i+2, i)
		}
		return out
	}
	longIssue := []string{"row 2: " + strings.Repeat("the value of the group column is not one we know about; ", 6)}
	invite := InviteData{
		Organization:     fixtureOrg,
		OrganizationLogo: FixtureOrgLogo,
		Link:             fixtureWebApp + "/account/invite?code=9f2c4e7a1b8d3f605c2e9a4b7d1f8e30&email=mariona%40exemple.cat",
		Expiry:           5 * 24 * time.Hour,
	}
	inviteNoOrg := invite
	inviteNoOrg.Organization, inviteNoOrg.OrganizationLogo = "", ""
	inviteExternalLogo := invite
	inviteExternalLogo.OrganizationLogo = "https://tracker.example.com/pixel.png"
	importNoName := importData(MembersImportResult{Total: 1248, Added: 0, Errors: issues(1248)})
	importNoName.UserName, importNoName.Organization, importNoName.Link = "", "", ""
	longDescription := strings.Repeat("The publish button keeps loading and then says \"transaction failed\".\n\n", 30)
	otp := OTPData{Code: "472913", Organization: fixtureOrg, OrganizationLogo: FixtureOrgLogo, ExpiryTime: 15 * time.Minute}
	otpLong := otp
	otpLong.Organization, otpLong.OrganizationLogo = fixtureLongOrg, ""
	return []Fixture{
		codeEmail("verification_account", VerifyAccountNotification, "mariona@exemple.cat"),
		codeEmail("verification_account_longemail", VerifyAccountNotification, fixtureLongMail),
		codeEmail("forgot_password", PasswordResetNotification, "mariona@exemple.cat"),
		codeEmail("password_setup", PasswordSetupNotification, "mariona@exemple.cat"),
		codeEmail("verify_email_change", VerifyEmailChangeNotification, "nova.adreca@exemple.cat"),
		{Name: "invite_admin", Template: InviteNotification, Data: invite},
		{Name: "invite_admin_noorg", Template: InviteNotification, Data: inviteNoOrg},
		{Name: "invite_admin_externallogo", Template: InviteNotification, Data: inviteExternalLogo},
		{
			Name: "members_import_done", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1248}),
		},
		{
			Name: "members_import_done_errors", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1245, Errors: issues(3)}),
		},
		{
			Name: "members_import_done_oneerror", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1247, Errors: longIssue}),
		},
		{
			Name: "members_import_done_manyerrors", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 3250, Added: 250, Errors: issues(3000)}),
		},
		{
			Name: "members_import_done_allfailed_noname_noorg_nolink", Template: MembersImportCompletionNotification,
			Data: importNoName,
		},
		{
			Name: "members_import_done_empty", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{}),
		},
		{Name: "support", Template: SupportNotification, Data: NewSupportData("Bug", "I cannot publish a vote",
			"When I publish the process the button keeps loading.\nI tried two browsers.", "mariona@exemple.cat", fixtureOrg)},
		{Name: "support_longdesc", Template: SupportNotification, Data: NewSupportData(
			"Bug <b>", strings.Repeat("Very long title ", 10), "<script>alert(1)</script>\n"+longDescription,
			fixtureLongMail, fixtureLongOrg)},
		{Name: "verification_code_otp", Template: VerifyOTPCodeNotification, Data: otp},
		{Name: "verification_code_otp_longorg", Template: VerifyOTPCodeNotification, Data: otpLong},
	}
}
```

2c. Layout and components (ported from `build.py`; colours are the Fil light values inline, dark mode by
class in `<style>`):

Create `assets/mail/layout.html`:

```html
{{define "layout"}}<!DOCTYPE html>
<html lang="{{.L.Lang}}" xmlns="http://www.w3.org/1999/xhtml" xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="X-UA-Compatible" content="IE=edge">
<meta name="color-scheme" content="light dark">
<meta name="supported-color-schemes" content="light dark">
<meta name="x-apple-disable-message-reformatting">
<meta name="format-detection" content="telephone=no,date=no,address=no,email=no,url=no">
<title>{{.L.Title}}</title>
{{msoHead}}
<style type="text/css">
:root { color-scheme: light dark; supported-color-schemes: light dark; }
html, body { margin:0 !important; padding:0 !important; height:100% !important; width:100% !important; }
#outlook a { padding:0; }
table, td { mso-table-lspace:0pt; mso-table-rspace:0pt; }
img { -ms-interpolation-mode:bicubic; border:0; line-height:100%; outline:none; text-decoration:none; }
a[x-apple-data-detectors] { color:inherit !important; text-decoration:none !important; }
.logo-dark { display:none; }
@media only screen and (max-width:480px) {
  .sm-px { padding-left:24px !important; padding-right:24px !important; }
  .sm-h1 { font-size:22px !important; line-height:28px !important; }
  .sm-code { font-size:24px !important; letter-spacing:5px !important; }
}
@media (prefers-color-scheme: dark) {
  .bg-page { background-color:#0a0a0a !important; }
  .card { background-color:#171717 !important; border-color:#262626 !important; }
  .t1, .code { color:#ededed !important; }
  .t2 { color:#b4b4b4 !important; }
  .t3 { color:#999999 !important; }
  .field { background-color:#0a0a0a !important; border-color:#707070 !important; }
  .inset { background-color:#0a0a0a !important; border-color:#262626 !important; }
  .notice-err { background-color:#111111 !important; border-color:#262626 !important; }
  .kv { border-top-color:#262626 !important; }
  .link { color:{{.Brand.AccentDark}} !important; }
  .err { color:#f97770 !important; }
  .tile { border-color:#262626 !important; }
  .logo-light { display:none !important; }
  .logo-dark { display:block !important; max-height:none !important; overflow:visible !important; }
}
[data-ogsb] .bg-page { background-color:#0a0a0a !important; }
[data-ogsb] .card { background-color:#171717 !important; border-color:#262626 !important; }
[data-ogsb] .field { background-color:#0a0a0a !important; border-color:#707070 !important; }
[data-ogsb] .inset { background-color:#0a0a0a !important; border-color:#262626 !important; }
[data-ogsb] .notice-err { background-color:#111111 !important; border-color:#262626 !important; }
[data-ogsc] .t1, [data-ogsc] .code { color:#ededed !important; }
[data-ogsc] .t2 { color:#b4b4b4 !important; }
[data-ogsc] .t3 { color:#999999 !important; }
[data-ogsc] .link { color:{{.Brand.AccentDark}} !important; }
[data-ogsc] .err { color:#f97770 !important; }
[data-ogsc] .kv { border-top-color:#262626 !important; }
[data-ogsc] .logo-light { display:none !important; }
[data-ogsc] .logo-dark { display:block !important; max-height:none !important; overflow:visible !important; }
</style>
</head>
<body class="bg-page" style="margin:0;padding:0;word-spacing:normal;background-color:#fafafa;">
<div style="display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">{{.L.Preheader}}{{preheaderPad}}</div>
<div role="article" aria-roledescription="email" aria-label="{{.L.Title}}" lang="{{.L.Lang}}">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" class="bg-page" style="background-color:#fafafa;">
<tr><td align="center" style="padding:0 12px;">
{{msoIf}}<table role="presentation" width="600" cellpadding="0" cellspacing="0" align="center"><tr><td>{{msoEnd}}
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;">
<tr>{{template "header" .}}</tr>
<tr><td class="card sm-px" style="background-color:#ffffff;border:1px solid #e5e5e5;border-radius:12px;padding:36px 40px;word-break:break-word;">
{{template "content" .}}
</td></tr>
<tr>{{template "footer" .}}</tr>
</table>
{{msoIf}}</td></tr></table>{{msoEnd}}
</td></tr>
</table>
</div>
</body>
</html>
{{end}}
```

Create `assets/mail/components.html`:

```html
{{/* Components shared by every email. Colours are the Fil light values; layout.html overrides
them for dark mode by class. Strings come in as arguments, or from the shared.* keys only. */}}

{{define "wordmark"}}{{/* arg: Brand */}}{{notMsoIf}}<img src="{{.AssetsURL}}/vocdoni-wordmark-black-halo@2x.png" class="logo-light" width="120" height="28" alt="Vocdoni" style="display:block;border:0;">
<div class="logo-dark" style="display:none;overflow:hidden;mso-hide:all;max-height:0;">
<img src="{{.AssetsURL}}/vocdoni-wordmark-white@2x.png" width="120" height="26" alt="Vocdoni" style="border:0;"></div>
{{notMsoEnd}}{{msoIf}}<img src="{{.AssetsURL}}/vocdoni-wordmark-black@2x.png" width="120" height="26" alt="Vocdoni">{{msoEnd}}{{end}}

{{define "header_vocdoni"}}{{/* arg: Brand */}}<td class="sm-px" align="left" style="padding:36px 40px 20px;">
{{template "wordmark" .}}
</td>{{end}}

{{define "header_cobrand"}}{{/* arg: the email context; org logo right, hidden in Outlook desktop */}}{{with .L.OrgLogo}}<td class="sm-px" style="padding:36px 40px 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
<td align="left" valign="middle">{{template "wordmark" $.Brand}}</td>
<td align="right" valign="middle">{{notMsoIf}}<table role="presentation" cellpadding="0" cellspacing="0" align="right"><tr>
<td class="tile" style="background-color:#ffffff;border:1px solid #e5e5e5;border-radius:6px;padding:5px;">
<img src="{{.}}" height="32" alt="" style="display:block;border:0;border-radius:3px;height:32px;max-width:140px;width:auto;"></td>
</tr></table>{{notMsoEnd}}</td>
</tr></table>
</td>{{else}}{{template "header_vocdoni" $.Brand}}{{end}}{{end}}

{{define "header_org"}}{{/* arg: the OTP context; the organization is the protagonist */}}<td align="center" style="padding:36px 24px 20px;">
<table role="presentation" cellpadding="0" cellspacing="0" align="center"><tr>
{{with .L.OrgLogo}}<td class="tile" style="background-color:#ffffff;border:1px solid #e5e5e5;border-radius:6px;padding:6px;">
{{notMsoIf}}<img src="{{.}}" height="44" alt="" style="display:block;border:0;border-radius:4px;height:44px;max-width:140px;width:auto;">{{notMsoEnd}}</td>{{else}}<td class="tile" width="56" height="56" align="center" valign="middle" style="background-color:#ffffff;border:1px solid #e5e5e5;border-radius:6px;width:56px;height:56px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:22px;line-height:56px;font-weight:600;color:#171717;">{{$.L.Initial $.Organization}}</td>{{end}}
</tr></table>
<p class="t1" style="margin:12px 0 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:16px;line-height:22px;font-weight:600;color:#171717;word-break:break-word;">{{.Organization}}</p>
</td>{{end}}

{{define "heading"}}{{/* arg: HTML */}}<h1 class="t1 sm-h1" style="margin:0 0 14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:24px;line-height:30px;font-weight:600;letter-spacing:-0.4px;color:#171717;">{{.}}</h1>{{end}}

{{define "para"}}{{/* arg: HTML; body text */}}<p class="t1" style="margin:0 0 16px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:16px;line-height:24px;color:#171717;">{{.}}</p>{{end}}

{{define "para_small"}}{{/* arg: HTML; secondary text */}}<p class="t2" style="margin:0 0 16px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;color:#4d4d4d;">{{.}}</p>{{end}}

{{define "muted"}}{{/* args: Text (HTML), Top (px) */}}<p class="t3" style="margin:{{.Top}}px 0 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">{{.Text}}</p>{{end}}

{{define "code_box"}}{{/* args: Code, Caption (HTML); a read-only field */}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:8px 0 24px;">
<tr><td class="field" align="center" style="background-color:#ffffff;border:1px solid #8f8f8f;border-radius:6px;padding:18px 16px;">
<span class="code t1 sm-code" style="font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono','Courier New',monospace;font-size:28px;line-height:36px;letter-spacing:6px;font-weight:600;color:#171717;word-break:break-all;-webkit-user-select:all;user-select:all;">{{.Code}}</span><br>
<span class="t3" style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:22px;color:#6b6b6b;">{{.Caption}}</span>
</td></tr></table>{{end}}

{{define "link"}}{{/* args: URL, Text, Brand, Break (bool: let a long URL wrap anywhere) */}}<a class="link" href="{{.URL}}" target="_blank" rel="noopener" style="color:{{.Brand.Accent}};text-decoration:none;font-weight:500;{{if .Break}}word-break:break-all;{{end}}">{{.Text}}</a>{{end}}

{{define "button"}}{{/* args: Label (text), URL, Brand; VML for Outlook desktop */}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:4px 0 24px;">
<tr><td align="left">
{{msoIf}}<v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" xmlns:w="urn:schemas-microsoft-com:office:word" href="{{.URL}}" style="height:46px;v-text-anchor:middle;width:280px;" arcsize="13%" strokecolor="{{.Brand.Accent}}" fillcolor="{{.Brand.Accent}}">
<w:anchorlock/>
<center style="color:{{.Brand.AccentLabel}};font-family:Arial,Helvetica,sans-serif;font-size:15px;font-weight:600;">{{.Label}}</center>
</v:roundrect>{{msoEnd}}
{{notMsoIf}}<table role="presentation" cellpadding="0" cellspacing="0"><tr>
<td style="background-color:{{.Brand.Accent}};border-radius:6px;">
<a href="{{.URL}}" target="_blank" rel="noopener" style="display:inline-block;padding:13px 28px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:15px;line-height:20px;font-weight:600;color:{{.Brand.AccentLabel}};text-decoration:none;border-radius:6px;">{{.Label}}</a>
</td></tr></table>{{notMsoEnd}}
</td></tr></table>{{end}}

{{define "fallback_link"}}{{/* args: L, URL, Brand */}}<p class="t3" style="margin:0 0 8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">{{.L.HTML "shared.button_fallback"}}<br>{{template "link" (dict "URL" .URL "Text" .URL "Brand" .Brand "Break" true)}}</p>{{end}}

{{define "actions"}}{{/* args: L, Intro (HTML, optional), Label, URL, Brand; nothing at all without a URL */}}{{if .URL}}{{with .Intro}}{{template "para" .}}{{end}}
{{template "button" .}}
{{template "fallback_link" .}}{{end}}{{end}}

{{define "kv_start"}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:8px 0 24px;">{{end}}

{{define "kv_row"}}{{/* args: Label, Value, Err (bool) */}}<tr><td class="t3 kv" style="padding:11px 12px 11px 0;border-top:1px solid #e5e5e5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;color:#6b6b6b;">{{.Label}}</td>
<td class="{{if .Err}}err{{else}}t1{{end}} kv" align="right" style="padding:11px 0;border-top:1px solid #e5e5e5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;font-weight:600;color:{{if .Err}}#ca2a30{{else}}#171717{{end}};word-break:break-word;">{{.Value}}</td></tr>{{end}}

{{define "kv_end"}}</table>{{end}}

{{define "notice_start"}}{{/* arg: true for an error notice */}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:0 0 24px;">
<tr>{{if .}}<td class="notice-err" style="background-color:#fff0f0;border:1px solid #efc6c5;border-radius:6px;padding:14px 18px;">{{else}}<td class="inset" style="background-color:#f5f5f5;border:1px solid #e5e5e5;border-radius:6px;padding:14px 18px;">{{end}}{{end}}

{{define "notice_end"}}</td></tr></table>{{end}}

{{define "notice_title"}}{{/* args: Text (HTML), Err (bool) */}}<p class="{{if .Err}}err{{else}}t1{{end}}" style="margin:0 0 6px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;font-weight:600;color:{{if .Err}}#ca2a30{{else}}#171717{{end}};">{{.Text}}</p>{{end}}

{{define "notice_line"}}{{/* arg: HTML or text; a value from the data */}}<p class="t1" style="margin:0 0 6px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;color:#171717;word-break:break-word;">{{.}}</p>{{end}}

{{define "notice_note"}}{{/* arg: HTML; a caption inside a notice */}}<p class="t3" style="margin:0 0 6px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;color:#6b6b6b;">{{.}}</p>{{end}}

{{define "footer_std"}}{{/* args: Why (HTML), L, Brand */}}<td align="left" class="sm-px" style="padding:24px 40px 48px;">
<p class="t3" style="margin:0 0 8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">{{.Why}}</p>
<p class="t3" style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">
{{.L.HTML "shared.footer_tagline"}} · {{template "link" (dict "URL" .Brand.SiteURL "Text" "vocdoni.io" "Brand" .Brand)}}<br>
{{.L.HTML "shared.footer_help"}} {{template "link" (dict "URL" (printf "mailto:%s" .Brand.SupportEmail) "Text" .Brand.SupportEmail "Brand" .Brand)}}</p>
</td>{{end}}

{{define "footer_otp"}}{{/* args: Why (HTML), Secured (text), Brand */}}<td align="center" class="sm-px" style="padding:24px 40px 48px;">
<p class="t3" style="margin:0 0 8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">{{.Why}}</p>
<p class="t3" style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:13px;line-height:19px;color:#6b6b6b;">{{template "link" (dict "URL" .Brand.SiteURL "Text" .Secured "Brand" .Brand)}}</p>
</td>{{end}}

{{define "para_pair"}}{{/* args: First, Second (HTML); one body paragraph */}}<p class="t1" style="margin:0 0 16px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:16px;line-height:24px;color:#171717;">{{.First}} {{.Second}}</p>{{end}}

{{define "notice_lines"}}{{/* arg: []string; keeps the line breaks of untrusted text */}}<p class="t2" style="margin:0 0 6px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Helvetica,Arial,sans-serif;font-size:14px;line-height:20px;color:#4d4d4d;word-break:break-word;">{{range $i, $line := .}}{{if $i}}<br>{{end}}{{$line}}{{end}}</p>{{end}}
```

2d. The eight emails. The four code emails share the same two files, byte for byte: create
`verification_account.html`/`.txt` and copy them to `forgot_password`, `password_setup` and
`verify_email_change` (`for n in forgot_password password_setup verify_email_change; do cp
assets/mail/verification_account.html assets/mail/$n.html; cp assets/mail/verification_account.txt
assets/mail/$n.txt; done`).

Create `assets/mail/verification_account.html`:

```html
{{define "header"}}{{template "header_vocdoni" .Brand}}{{end}}

{{define "content"}}{{template "heading" (.L.HTML "heading")}}
{{template "para" (.L.HTML "intro")}}
{{template "code_box" (dict "Code" .Code "Caption" (.L.HTML "shared.code_expires"))}}
{{template "actions" (dict "L" .L "Intro" (.L.HTML "or_button") "Label" (.L.Text "button") "URL" .Link "Brand" .Brand)}}
{{template "muted" (dict "Text" (.L.HTML "ignore") "Top" 24)}}{{end}}

{{define "footer"}}{{template "footer_std" (dict "Why" (.L.HTML "footer_why") "L" .L "Brand" .Brand)}}{{end}}
```

Create `assets/mail/verification_account.txt`:

```gotemplate
{{define "subject"}}{{.L.Subject "subject"}}{{end}}

{{define "preheader"}}{{.L.Text "preheader"}}{{end}}

{{define "plain"}}{{.L.Text "plain_code"}}
{{.L.Text "shared.code_expires"}}.
{{if .Link}}
{{.L.Text "plain_link"}} {{.Link}}
{{end}}
{{.L.Text "ignore"}}

{{template "footer" .}}{{end}}

{{define "footer"}}--
{{.L.Text "footer_why"}}
{{.L.Text "shared.footer_tagline"}} · {{.Brand.SiteURL}}
{{.L.Text "shared.footer_help"}} {{.Brand.SupportEmail}}{{end}}
```

Create `assets/mail/invite_admin.html`:

```html
{{define "header"}}{{template "header_cobrand" .}}{{end}}

{{define "content"}}{{template "heading" (.L.HTML "heading")}}
{{if .Organization}}{{template "para" (.L.HTML "intro")}}{{else}}{{template "para" (.L.HTML "intro_no_org")}}{{end}}
{{template "actions" (dict "L" .L "Label" (.L.Text "button") "URL" .Link "Brand" .Brand)}}
{{template "muted" (dict "Text" (.L.HTML "expires_note") "Top" 8)}}{{end}}

{{define "footer"}}{{template "footer_std" (dict "Why" (.L.HTML "footer_why") "L" .L "Brand" .Brand)}}{{end}}
```

Create `assets/mail/invite_admin.txt`:

```gotemplate
{{define "subject"}}{{if .Organization}}{{.L.Subject "subject"}}{{else}}{{.L.Subject "subject_no_org"}}{{end}}{{end}}

{{define "preheader"}}{{.L.Text "preheader"}}{{end}}

{{define "plain"}}{{.L.Text "heading"}}

{{if .Organization}}{{.L.Text "intro"}}{{else}}{{.L.Text "intro_no_org"}}{{end}}
{{if .Link}}
{{.L.Text "plain_link"}} {{.Link}}
{{end}}
{{.L.Text "expires_note"}}

--
{{.L.Text "footer_why"}}
{{.L.Text "shared.footer_tagline"}} · {{.Brand.SiteURL}}
{{.L.Text "shared.footer_help"}} {{.Brand.SupportEmail}}{{end}}
```

Create `assets/mail/members_import_done.html`:

```html
{{define "header"}}{{template "header_cobrand" .}}{{end}}

{{define "content"}}
{{- if eq .State "done"}}{{template "heading" (.L.HTML "heading_done")}}
{{- else if eq .State "issues"}}{{template "heading" (.L.HTML "heading_issues")}}
{{- else}}{{template "heading" (.L.HTML "heading_failed")}}{{end}}
{{$greeting := .L.HTML "greeting_no_name"}}{{if .UserName}}{{$greeting = .L.HTML "greeting"}}{{end}}
{{- $intro := .L.HTML "intro_done_no_org"}}
{{- if .Organization}}
{{- if eq .State "done"}}{{$intro = .L.HTML "intro_done"}}
{{- else if eq .State "issues"}}{{$intro = .L.HTML "intro_issues"}}
{{- else if eq .State "failed"}}{{$intro = .L.HTML "intro_failed"}}
{{- else}}{{$intro = .L.HTML "intro_empty"}}{{end}}
{{- else}}
{{- if eq .State "issues"}}{{$intro = .L.HTML "intro_issues_no_org"}}
{{- else if eq .State "failed"}}{{$intro = .L.HTML "intro_failed_no_org"}}
{{- else if eq .State "empty"}}{{$intro = .L.HTML "intro_empty_no_org"}}{{end}}
{{- end}}
{{template "para_pair" (dict "First" $greeting "Second" $intro)}}
{{template "kv_start"}}
{{template "kv_row" (dict "Label" (.L.HTML "label_processed") "Value" (.L.V .Total))}}
{{template "kv_row" (dict "Label" (.L.HTML "label_added") "Value" (.L.V .Added) "Err" (and (eq .Added 0) (gt .Total 0)))}}
{{template "kv_row" (dict "Label" (.L.HTML "label_errors") "Value" (.L.V .ErrorCount) "Err" (gt .ErrorCount 0))}}
{{template "kv_row" (dict "Label" (.L.HTML "label_completed") "Value" (.L.V .CompletedAt))}}
{{template "kv_end"}}
{{if .Issues}}{{template "notice_start" true}}
{{- if .MoreIssues}}{{template "notice_title" (dict "Text" (.L.HTML "issues_title_first") "Err" true)}}
{{- else}}{{template "notice_title" (dict "Text" (.L.HTML "issues_title") "Err" true)}}{{end}}
{{range .Issues}}{{template "notice_line" .}}
{{end}}
{{- if .MoreIssues}}{{template "notice_note" (.L.HTMLn "more_issues" .MoreIssues)}}{{end}}
{{template "notice_end"}}{{end}}
{{if eq .State "issues"}}{{template "para_small" (.L.HTML "fix_hint")}}{{end}}
{{template "actions" (dict "L" .L "Label" (.L.Text "button") "URL" .Link "Brand" .Brand)}}{{end}}

{{define "footer"}}{{template "footer_std" (dict "Why" (.L.HTML "footer_why") "L" .L "Brand" .Brand)}}{{end}}
```

Create `assets/mail/members_import_done.txt`:

```gotemplate
{{define "subject"}}
{{- if eq .State "done"}}{{if .Organization}}{{.L.Subject "subject_done"}}{{else}}{{.L.Subject "subject_done_no_org"}}{{end}}
{{- else if eq .State "issues"}}{{if .Organization}}{{.L.Subjectn "subject_issues" .ErrorCount}}{{else}}{{.L.Subjectn "subject_issues_no_org" .ErrorCount}}{{end}}
{{- else}}{{if .Organization}}{{.L.Subject "subject_failed"}}{{else}}{{.L.Subject "subject_failed_no_org"}}{{end}}{{end}}
{{- end}}

{{define "preheader"}}
{{- if eq .State "done"}}{{.L.Textn "preheader_done" .Added}}
{{- else if eq .State "issues"}}{{.L.Textn "preheader_issues" .ErrorCount}}
{{- else if eq .State "failed"}}{{.L.Text "preheader_failed"}}
{{- else}}{{.L.Text "preheader_empty"}}{{end}}
{{- end}}

{{define "plain"}}
{{- if eq .State "done"}}{{.L.Text "heading_done"}}{{else if eq .State "issues"}}{{.L.Text "heading_issues"}}{{else}}{{.L.Text "heading_failed"}}{{end}}

{{if .UserName}}{{.L.Text "greeting"}}{{else}}{{.L.Text "greeting_no_name"}}{{end}} {{template "intro" .}}

{{.L.Text "label_processed"}}: {{.L.V .Total}}
{{.L.Text "label_added"}}: {{.L.V .Added}}
{{.L.Text "label_errors"}}: {{.L.V .ErrorCount}}
{{.L.Text "label_completed"}}: {{.L.V .CompletedAt}}
{{if .Issues}}
{{if .MoreIssues}}{{.L.Text "issues_title_first"}}{{else}}{{.L.Text "issues_title"}}{{end}}
{{range .Issues}}- {{.}}
{{end}}{{if .MoreIssues}}{{.L.Textn "more_issues" .MoreIssues}}
{{end}}{{end}}
{{if eq .State "issues"}}{{.L.Text "fix_hint"}}
{{end}}
{{if .Link}}{{.L.Text "button"}}: {{.Link}}
{{end}}
--
{{.L.Text "footer_why"}}
{{.L.Text "shared.footer_tagline"}} · {{.Brand.SiteURL}}
{{.L.Text "shared.footer_help"}} {{.Brand.SupportEmail}}{{end}}

{{define "intro"}}
{{- if .Organization}}
{{- if eq .State "done"}}{{.L.Text "intro_done"}}{{else if eq .State "issues"}}{{.L.Text "intro_issues"}}
{{- else if eq .State "failed"}}{{.L.Text "intro_failed"}}{{else}}{{.L.Text "intro_empty"}}{{end}}
{{- else}}
{{- if eq .State "done"}}{{.L.Text "intro_done_no_org"}}{{else if eq .State "issues"}}{{.L.Text "intro_issues_no_org"}}
{{- else if eq .State "failed"}}{{.L.Text "intro_failed_no_org"}}{{else}}{{.L.Text "intro_empty_no_org"}}{{end}}
{{- end}}
{{- end}}
```

Create `assets/mail/support.html`:

```html
{{define "header"}}{{template "header_vocdoni" .Brand}}{{end}}

{{define "content"}}{{template "heading" (.L.HTML "heading")}}
{{template "para" (.L.HTML "intro")}}
{{template "kv_start"}}
{{template "kv_row" (dict "Label" (.L.HTML "label_title") "Value" .Title)}}
{{template "kv_row" (dict "Label" (.L.HTML "label_type") "Value" .Type)}}
{{template "kv_row" (dict "Label" (.L.HTML "label_user") "Value" .Email)}}
{{template "kv_row" (dict "Label" (.L.HTML "label_org") "Value" .Organization)}}
{{template "kv_end"}}
{{template "notice_start" false}}
{{template "notice_title" (dict "Text" (.L.HTML "label_description") "Err" false)}}
{{template "notice_lines" (.L.Lines .Description)}}
{{if .Truncated}}{{template "notice_note" (.L.HTML "truncated")}}{{end}}
{{template "notice_end"}}
{{template "muted" (dict "Text" (.L.HTML "reassure") "Top" 4)}}{{end}}

{{define "footer"}}{{template "footer_std" (dict "Why" (.L.HTML "footer_why") "L" .L "Brand" .Brand)}}{{end}}
```

Create `assets/mail/support.txt`:

```gotemplate
{{define "subject"}}{{.L.Subject "subject"}}{{end}}

{{define "preheader"}}{{.L.Text "preheader"}}{{end}}

{{define "plain"}}{{.L.Text "heading"}}

{{.L.Text "intro"}}

{{.L.Text "label_title"}}: {{.Title}}
{{.L.Text "label_type"}}: {{.Type}}
{{.L.Text "label_user"}}: {{.Email}}
{{.L.Text "label_org"}}: {{.Organization}}

{{.L.Text "label_description"}}:
{{.Description}}
{{if .Truncated}}
{{.L.Text "truncated"}}
{{end}}
{{.L.Text "reassure"}}

--
{{.L.Text "footer_why"}}
{{.L.Text "shared.footer_tagline"}} · {{.Brand.SiteURL}}
{{.L.Text "shared.footer_help"}} {{.Brand.SupportEmail}}{{end}}
```

Create `assets/mail/verification_code_otp.html`:

```html
{{define "header"}}{{template "header_org" .}}{{end}}

{{define "content"}}{{template "heading" (.L.HTML "heading")}}
{{template "para" (.L.HTML "intro")}}
{{template "code_box" (dict "Code" .Code "Caption" (.L.HTML "expires"))}}
{{template "para_small" (.L.HTML "enter")}}
{{template "muted" (dict "Text" (.L.HTML "ignore") "Top" 4)}}{{end}}

{{define "footer"}}{{template "footer_otp" (dict "Why" (.L.HTML "footer_why") "Secured" (.L.Text "footer_secured") "Brand" .Brand)}}{{end}}
```

Create `assets/mail/verification_code_otp.txt`:

```gotemplate
{{define "subject"}}{{.L.Subject "subject"}}{{end}}

{{define "preheader"}}{{.L.Text "preheader"}}{{end}}

{{/* the plain text is also the SMS: one line, at most 64 characters rendered (one UCS-2 segment, no organization name) */}}
{{define "plain"}}{{.L.Text "sms"}}{{end}}
```

2e. Strings. English is the source and the schema; Catalan is the approved mockup copy (DESIGN.md and
`emails/*_ca.html`) completed for the no-organization, no-name and plural cases; Spanish is translated from
English and reviewed (tú, "Vocdoni App").

Create `assets/mail/i18n/en.yaml`:

```yaml
# English: the source of every other locale. Translators: keep every key, keep each
# {{.Field}} exactly as written, mark emphasis with *text*, write a literal asterisk as \\*,
# never write HTML. Strings are double-quoted YAML.

shared:
  button_fallback: "If the button doesn't work, copy and paste this link into your browser:"
  code_expires: "Expires in {{.Expiry}}"
  footer_tagline: "Vocdoni, secure and verifiable digital voting"
  footer_help: "Need help? Write to"

duration:
  minute:
    one: "1 minute"
    other: "{{.N}} minutes"
  hour:
    one: "1 hour"
    other: "{{.N}} hours"
  day:
    one: "1 day"
    other: "{{.N}} days"

date:
  format: "{{.Month}} {{.Day}}, {{.Year}}"
  months: ["January", "February", "March", "April", "May", "June", "July", "August", "September",
    "October", "November", "December"]

number:
  group: ","

verification_account:
  subject: "Verify your Vocdoni account"
  preheader: "Use code {{.Code}} to activate your account. It expires in {{.Expiry}}."
  heading: "Confirm your email address"
  intro: "Thanks for creating a Vocdoni account. Use this code to verify your address:"
  or_button: "Or verify your account in one click:"
  button: "Verify account"
  ignore: "If you didn't create a Vocdoni account, you can safely ignore this email."
  footer_why: "You're receiving this email because someone registered this address on Vocdoni."
  plain_code: "Your Vocdoni verification code is: {{.Code}}"
  plain_link: "Or verify your account with this link:"

forgot_password:
  subject: "Reset your Vocdoni password"
  preheader: "Your password reset code expires in {{.Expiry}}."
  heading: "Reset your password"
  intro: "We received a request to reset the password of the Vocdoni account linked to *{{.Email}}*. Use this code:"
  or_button: "Or reset it right here:"
  button: "Reset password"
  ignore: "If you didn't ask for this, you can ignore this email: your current password will keep working."
  footer_why: "You're receiving this email because a password reset was requested for this address."
  plain_code: "Your Vocdoni password reset code is: {{.Code}}"
  plain_link: "Or reset it with this link:"

password_setup:
  subject: "Add a password to your Vocdoni account"
  preheader: "Confirm it with code {{.Code}}. It expires in {{.Expiry}}."
  heading: "Add a password"
  intro: "You asked to add a password to your Vocdoni account, where you now sign in with an external provider. Confirm it with this code:"
  or_button: "Or set it right here:"
  button: "Set password"
  ignore: "If you didn't ask for this, you can ignore this email: no password will be created and you'll keep signing in as usual."
  footer_why: "You're receiving this email because someone asked to add a password to the account of this address."
  plain_code: "Your Vocdoni password setup code is: {{.Code}}"
  plain_link: "Or set it with this link:"

verify_email_change:
  subject: "Confirm your new email address"
  preheader: "Confirm the change with code {{.Code}}. It expires in {{.Expiry}}."
  heading: "Confirm your new address"
  intro: "You asked for your Vocdoni account to use this address (*{{.Email}}*). Confirm it with this code:"
  or_button: "Or confirm it in one click:"
  button: "Confirm address"
  ignore: "If this wasn't you, you can ignore this email: your account will keep its current address."
  footer_why: "You're receiving this email because someone entered this address as the new email of a Vocdoni account."
  plain_code: "Your code to confirm your new email address is: {{.Code}}"
  plain_link: "Or confirm it with this link:"

invite_admin:
  subject: "You've been invited to manage {{.Organization}}"
  subject_no_org: "You've been invited to manage an organization on Vocdoni App"
  preheader: "Accept the invitation to join the team. It expires in {{.Expiry}}."
  heading: "You have an invitation"
  intro: "You've been invited to join the admin team of *{{.Organization}}* on Vocdoni App. You'll be able to manage members, voting processes and results."
  intro_no_org: "You've been invited to join the admin team of an organization on Vocdoni App. You'll be able to manage members, voting processes and results."
  button: "Accept invitation"
  expires_note: "The invitation expires in {{.Expiry}}. If you weren't expecting it, you can ignore this email."
  footer_why: "You're receiving this email because an organization on Vocdoni App invited this address."
  plain_link: "Accept the invitation with this link:"

members_import_done:
  subject_done: "Members import completed at {{.Organization}}"
  subject_done_no_org: "Members import completed"
  subject_issues:
    one: "Members import completed with 1 error at {{.Organization}}"
    other: "Members import completed with {{.ErrorCount}} errors at {{.Organization}}"
  subject_issues_no_org:
    one: "Members import completed with 1 error"
    other: "Members import completed with {{.ErrorCount}} errors"
  subject_failed: "No members could be imported at {{.Organization}}"
  subject_failed_no_org: "No members could be imported"
  preheader_done:
    one: "1 member added."
    other: "{{.Added}} members added."
  preheader_issues:
    one: "1 row needs your review."
    other: "{{.ErrorCount}} rows need your review."
  preheader_failed: "No member could be added. Check the file format."
  preheader_empty: "The file had no rows, so no member was added."
  heading_done: "Import completed"
  heading_issues: "Import completed with errors"
  heading_failed: "No members could be imported"
  greeting: "Hello, {{.UserName}}."
  greeting_no_name: "Hello."
  intro_done: "All done! The members import at *{{.Organization}}* has finished and every row was added."
  intro_done_no_org: "All done! The members import has finished and every row was added."
  intro_issues: "The members import at *{{.Organization}}* has finished, but some rows could not be imported."
  intro_issues_no_org: "The members import has finished, but some rows could not be imported."
  intro_failed: "The members import at *{{.Organization}}* has finished, but no member could be added. Check that the file follows the template and try again."
  intro_failed_no_org: "The members import has finished, but no member could be added. Check that the file follows the template and try again."
  intro_empty: "The members import at *{{.Organization}}* has finished, but the file had no rows, so no member was added."
  intro_empty_no_org: "The members import has finished, but the file had no rows, so no member was added."
  label_processed: "Members processed"
  label_added: "Added"
  label_errors: "Errors"
  label_completed: "Finished"
  issues_title: "Rows to review"
  issues_title_first: "First rows to review"
  more_issues:
    one: "and 1 more error"
    other: "and {{.MoreIssues}} more errors"
  fix_hint: "Fix these rows in the file and import them again; the rest are already in the member base."
  button: "Review the member base"
  footer_why: "You're receiving this email because you manage this organization on Vocdoni App."

support:
  subject: "New ticket · {{.Type}} · {{.Title}}"
  preheader: "From {{.Email}} ({{.Organization}})."
  heading: "New support request"
  intro: "A ticket was opened from Vocdoni App. The person who opened it is in copy and will get the replies in this thread."
  label_title: "Title"
  label_type: "Type"
  label_user: "User"
  label_org: "Organization"
  label_description: "Description"
  truncated: "Description cut at 1,200 characters; the full text is on the ticket."
  reassure: "If you opened this request, there's nothing else to do: we'll reply in this same thread as soon as we can."
  footer_why: "This ticket was created from the Vocdoni App support form."

verification_code_otp:
  subject: "Your code to vote at {{.Organization}}"
  preheader: "Verification code {{.Code}}. It expires in {{.ExpiryTime}}."
  heading: "Your code to vote"
  intro: "*{{.Organization}}* sent you this code to verify your identity before voting:"
  expires: "Expires in {{.ExpiryTime}}"
  enter: "Enter it on the voting page to continue."
  ignore: "If you weren't expecting this code, you can ignore this message: nobody can vote on your behalf without it."
  footer_why: "You're receiving this message because {{.Organization}} uses this address to verify your identity before voting."
  footer_secured: "Voting secured by Vocdoni"
  # also the SMS: one line, at most 64 characters rendered with a 6-digit code and "15 minutes"
  # (70 minus the "[STG] " label, one UCS-2 segment); it names no organization; no ? ( ) [ ] + * | ^ $
  # (tests read it as a regular expression)
  sms: "Code to vote: {{.Code}}. Expires in {{.ExpiryTime}}."
```

Create `assets/mail/i18n/ca.yaml`:

```yaml
# Català: còpia final de les maquetes aprovades (DESIGN.md), adaptada als casos sense
# organització, sense nom i als plurals.

shared:
  button_fallback: "Si el botó no funciona, copia i enganxa aquest enllaç al navegador:"
  code_expires: "Caduca en {{.Expiry}}"
  footer_tagline: "Vocdoni, vot digital segur i verificable"
  footer_help: "Necessites ajuda? Escriu a"

duration:
  minute:
    one: "1 minut"
    other: "{{.N}} minuts"
  hour:
    one: "1 hora"
    other: "{{.N}} hores"
  day:
    one: "1 dia"
    other: "{{.N}} dies"

date:
  format: "{{.Day}} {{.Month}} de {{.Year}}"
  months: ["de gener", "de febrer", "de març", "d’abril", "de maig", "de juny", "de juliol", "d’agost",
    "de setembre", "d’octubre", "de novembre", "de desembre"]

number:
  group: "."

verification_account:
  subject: "Verifica el teu compte de Vocdoni"
  preheader: "Fes servir el codi {{.Code}} per activar el compte. Caduca en {{.Expiry}}."
  heading: "Confirma la teva adreça de correu"
  intro: "Gràcies per crear un compte a Vocdoni. Fes servir aquest codi per verificar la teva adreça:"
  or_button: "O bé verifica el compte amb un sol clic:"
  button: "Verifica el compte"
  ignore: "Si no has creat cap compte a Vocdoni, pots ignorar aquest correu sense fer res."
  footer_why: "Reps aquest correu perquè algú ha registrat aquesta adreça a Vocdoni."
  plain_code: "El teu codi de verificació de Vocdoni és: {{.Code}}"
  plain_link: "O bé verifica el compte amb aquest enllaç:"

forgot_password:
  subject: "Restableix la contrasenya de Vocdoni"
  preheader: "El teu codi per restablir la contrasenya caduca en {{.Expiry}}."
  heading: "Restableix la contrasenya"
  intro: "Hem rebut una sol·licitud per restablir la contrasenya del compte de Vocdoni associat a *{{.Email}}*. Fes servir aquest codi:"
  or_button: "O restableix-la directament des d’aquí:"
  button: "Restableix la contrasenya"
  ignore: "Si no ho has demanat tu, pots ignorar aquest correu: la contrasenya actual continuarà funcionant."
  footer_why: "Reps aquest correu perquè s’ha demanat un restabliment de contrasenya per a aquesta adreça."
  plain_code: "El teu codi per restablir la contrasenya de Vocdoni és: {{.Code}}"
  plain_link: "O restableix-la amb aquest enllaç:"

password_setup:
  subject: "Afegeix una contrasenya al teu compte de Vocdoni"
  preheader: "Confirma-ho amb el codi {{.Code}}. Caduca en {{.Expiry}}."
  heading: "Afegeix una contrasenya"
  intro: "Has demanat afegir una contrasenya al teu compte de Vocdoni, on ara inicies sessió amb un proveïdor extern. Confirma-ho amb aquest codi:"
  or_button: "O estableix-la directament des d’aquí:"
  button: "Estableix la contrasenya"
  ignore: "Si no ho has demanat tu, pots ignorar aquest correu: no es crearà cap contrasenya i continuaràs iniciant sessió com sempre."
  footer_why: "Reps aquest correu perquè s’ha demanat afegir una contrasenya al compte d’aquesta adreça."
  plain_code: "El teu codi per afegir una contrasenya a Vocdoni és: {{.Code}}"
  plain_link: "O estableix-la amb aquest enllaç:"

verify_email_change:
  subject: "Confirma la teva nova adreça de correu"
  preheader: "Confirma el canvi amb el codi {{.Code}}. Caduca en {{.Expiry}}."
  heading: "Confirma la nova adreça"
  intro: "Has demanat que el teu compte de Vocdoni passi a fer servir aquesta adreça (*{{.Email}}*). Confirma-la amb aquest codi:"
  or_button: "O confirma-la amb un sol clic:"
  button: "Confirma l’adreça"
  ignore: "Si no has estat tu, pots ignorar aquest correu: el compte continuarà amb l’adreça de sempre."
  footer_why: "Reps aquest correu perquè algú ha indicat aquesta adreça com a nou correu d’un compte de Vocdoni."
  plain_code: "El teu codi per confirmar la nova adreça de correu és: {{.Code}}"
  plain_link: "O confirma-la amb aquest enllaç:"

invite_admin:
  subject: "T’han convidat a administrar {{.Organization}}"
  subject_no_org: "T’han convidat a administrar una organització a la Vocdoni App"
  preheader: "Accepta la invitació per unir-te a l’equip. Caduca en {{.Expiry}}."
  heading: "Tens una invitació"
  intro: "T’han convidat a formar part de l’equip d’administració de l’organització *{{.Organization}}* a la Vocdoni App. Hi podràs gestionar els membres, els processos de votació i els resultats."
  intro_no_org: "T’han convidat a formar part de l’equip d’administració d’una organització a la Vocdoni App. Hi podràs gestionar els membres, els processos de votació i els resultats."
  button: "Accepta la invitació"
  expires_note: "La invitació caduca en {{.Expiry}}. Si no l’esperaves, pots ignorar aquest correu."
  footer_why: "Reps aquest correu perquè una organització de la Vocdoni App ha convidat aquesta adreça."
  plain_link: "Accepta la invitació amb aquest enllaç:"

members_import_done:
  subject_done: "Importació de membres completada a {{.Organization}}"
  subject_done_no_org: "Importació de membres completada"
  subject_issues:
    one: "Importació completada amb 1 error a {{.Organization}}"
    other: "Importació completada amb {{.ErrorCount}} errors a {{.Organization}}"
  subject_issues_no_org:
    one: "Importació completada amb 1 error"
    other: "Importació completada amb {{.ErrorCount}} errors"
  subject_failed: "La importació de membres no s’ha pogut completar a {{.Organization}}"
  subject_failed_no_org: "La importació de membres no s’ha pogut completar"
  preheader_done:
    one: "1 membre afegit correctament."
    other: "{{.Added}} membres afegits correctament."
  preheader_issues:
    one: "1 fila per revisar."
    other: "{{.ErrorCount}} files per revisar."
  preheader_failed: "No s’ha pogut afegir cap registre; revisa el format del fitxer."
  preheader_empty: "El fitxer no contenia cap fila i no s’ha afegit cap registre."
  heading_done: "Importació completada"
  heading_issues: "Importació completada amb errors"
  heading_failed: "La importació no s’ha pogut completar"
  greeting: "Hola, {{.UserName}}."
  greeting_no_name: "Hola."
  intro_done: "Tot ha anat bé! La importació de membres a *{{.Organization}}* ha acabat i s’han afegit tots els registres."
  intro_done_no_org: "Tot ha anat bé! La importació de membres ha acabat i s’han afegit tots els registres."
  intro_issues: "La importació de membres a *{{.Organization}}* ha acabat, però algunes files no s’han pogut importar."
  intro_issues_no_org: "La importació de membres ha acabat, però algunes files no s’han pogut importar."
  intro_failed: "La importació de membres a *{{.Organization}}* ha acabat, però no s’ha pogut afegir cap registre. Comprova que el fitxer segueixi la plantilla i torna-ho a provar."
  intro_failed_no_org: "La importació de membres ha acabat, però no s’ha pogut afegir cap registre. Comprova que el fitxer segueixi la plantilla i torna-ho a provar."
  intro_empty: "La importació de membres a *{{.Organization}}* ha acabat, però el fitxer no contenia cap fila i no s’ha afegit cap registre."
  intro_empty_no_org: "La importació de membres ha acabat, però el fitxer no contenia cap fila i no s’ha afegit cap registre."
  label_processed: "Membres processats"
  label_added: "Afegits"
  label_errors: "Errors"
  label_completed: "Finalitzada"
  issues_title: "Files que cal revisar"
  issues_title_first: "Primeres files que cal revisar"
  more_issues:
    one: "i 1 error més"
    other: "i {{.MoreIssues}} errors més"
  fix_hint: "Corregeix aquestes files al fitxer i torna a importar-les; la resta ja són a la base de membres."
  button: "Revisa la base de membres"
  footer_why: "Reps aquest correu perquè administres aquesta organització a la Vocdoni App."

support:
  subject: "Nou tiquet · {{.Type}} · {{.Title}}"
  preheader: "De {{.Email}} ({{.Organization}})."
  heading: "Nova sol·licitud de suport"
  intro: "S’ha obert un tiquet des de la Vocdoni App. La persona sol·licitant està en còpia i rebrà les respostes d’aquest fil."
  label_title: "Títol"
  label_type: "Tipus"
  label_user: "Usuari"
  label_org: "Organització"
  label_description: "Descripció"
  truncated: "Descripció retallada a 1.200 caràcters; el text complet queda al tiquet."
  reassure: "Si has obert tu aquesta sol·licitud, no cal que facis res més: et respondrem en aquest mateix fil tan aviat com puguem."
  footer_why: "Aquest tiquet s’ha creat des del formulari de suport de la Vocdoni App."

verification_code_otp:
  subject: "El teu codi per votar a {{.Organization}}"
  preheader: "Codi de verificació {{.Code}}. Caduca en {{.ExpiryTime}}."
  heading: "El teu codi per votar"
  intro: "*{{.Organization}}* t’envia aquest codi per verificar la teva identitat abans de votar:"
  expires: "Caduca en {{.ExpiryTime}}"
  enter: "Introdueix-lo a la pàgina de la votació per continuar."
  ignore: "Si no esperaves aquest codi, pots ignorar aquest missatge: ningú no pot votar en nom teu sense tenir-lo."
  footer_why: "Reps aquest missatge perquè {{.Organization}} fa servir aquesta adreça per verificar la teva identitat abans de votar."
  footer_secured: "Votació protegida amb la tecnologia de Vocdoni"
  sms: "Codi per votar: {{.Code}}. Caduca en {{.ExpiryTime}}."
```

Create `assets/mail/i18n/es.yaml`:

```yaml
# Español: traducido del inglés y revisado (tuteo, sentence case, «Vocdoni App»).

shared:
  button_fallback: "Si el botón no funciona, copia y pega este enlace en tu navegador:"
  code_expires: "Caduca en {{.Expiry}}"
  footer_tagline: "Vocdoni, voto digital seguro y verificable"
  footer_help: "¿Necesitas ayuda? Escribe a"

duration:
  minute:
    one: "1 minuto"
    other: "{{.N}} minutos"
  hour:
    one: "1 hora"
    other: "{{.N}} horas"
  day:
    one: "1 día"
    other: "{{.N}} días"

date:
  format: "{{.Day}} de {{.Month}} de {{.Year}}"
  months: ["enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre",
    "octubre", "noviembre", "diciembre"]

number:
  group: "."

verification_account:
  subject: "Verifica tu cuenta de Vocdoni"
  preheader: "Usa el código {{.Code}} para activar tu cuenta. Caduca en {{.Expiry}}."
  heading: "Confirma tu dirección de correo"
  intro: "Gracias por crear una cuenta en Vocdoni. Usa este código para verificar tu dirección:"
  or_button: "O verifica tu cuenta con un solo clic:"
  button: "Verifica la cuenta"
  ignore: "Si no has creado ninguna cuenta en Vocdoni, puedes ignorar este correo sin hacer nada."
  footer_why: "Recibes este correo porque alguien ha registrado esta dirección en Vocdoni."
  plain_code: "Tu código de verificación de Vocdoni es: {{.Code}}"
  plain_link: "O verifica tu cuenta con este enlace:"

forgot_password:
  subject: "Restablece tu contraseña de Vocdoni"
  preheader: "Tu código para restablecer la contraseña caduca en {{.Expiry}}."
  heading: "Restablece tu contraseña"
  intro: "Hemos recibido una solicitud para restablecer la contraseña de la cuenta de Vocdoni asociada a *{{.Email}}*. Usa este código:"
  or_button: "O restablécela directamente desde aquí:"
  button: "Restablece la contraseña"
  ignore: "Si no lo has pedido tú, puedes ignorar este correo: tu contraseña actual seguirá funcionando."
  footer_why: "Recibes este correo porque se ha pedido restablecer la contraseña de esta dirección."
  plain_code: "Tu código para restablecer la contraseña de Vocdoni es: {{.Code}}"
  plain_link: "O restablécela con este enlace:"

password_setup:
  subject: "Añade una contraseña a tu cuenta de Vocdoni"
  preheader: "Confírmalo con el código {{.Code}}. Caduca en {{.Expiry}}."
  heading: "Añade una contraseña"
  intro: "Has pedido añadir una contraseña a tu cuenta de Vocdoni, en la que ahora inicias sesión con un proveedor externo. Confírmalo con este código:"
  or_button: "O establécela directamente desde aquí:"
  button: "Establece la contraseña"
  ignore: "Si no lo has pedido tú, puedes ignorar este correo: no se creará ninguna contraseña y seguirás iniciando sesión como siempre."
  footer_why: "Recibes este correo porque se ha pedido añadir una contraseña a la cuenta de esta dirección."
  plain_code: "Tu código para añadir una contraseña a Vocdoni es: {{.Code}}"
  plain_link: "O establécela con este enlace:"

verify_email_change:
  subject: "Confirma tu nueva dirección de correo"
  preheader: "Confirma el cambio con el código {{.Code}}. Caduca en {{.Expiry}}."
  heading: "Confirma la nueva dirección"
  intro: "Has pedido que tu cuenta de Vocdoni pase a usar esta dirección (*{{.Email}}*). Confírmala con este código:"
  or_button: "O confírmala con un solo clic:"
  button: "Confirma la dirección"
  ignore: "Si no has sido tú, puedes ignorar este correo: tu cuenta seguirá con la dirección de siempre."
  footer_why: "Recibes este correo porque alguien ha indicado esta dirección como nuevo correo de una cuenta de Vocdoni."
  plain_code: "Tu código para confirmar la nueva dirección de correo es: {{.Code}}"
  plain_link: "O confírmala con este enlace:"

invite_admin:
  subject: "Te han invitado a administrar {{.Organization}}"
  subject_no_org: "Te han invitado a administrar una organización en Vocdoni App"
  preheader: "Acepta la invitación para unirte al equipo. Caduca en {{.Expiry}}."
  heading: "Tienes una invitación"
  intro: "Te han invitado a formar parte del equipo de administración de *{{.Organization}}* en Vocdoni App. Podrás gestionar los miembros, los procesos de votación y los resultados."
  intro_no_org: "Te han invitado a formar parte del equipo de administración de una organización en Vocdoni App. Podrás gestionar los miembros, los procesos de votación y los resultados."
  button: "Acepta la invitación"
  expires_note: "La invitación caduca en {{.Expiry}}. Si no la esperabas, puedes ignorar este correo."
  footer_why: "Recibes este correo porque una organización de Vocdoni App ha invitado a esta dirección."
  plain_link: "Acepta la invitación con este enlace:"

members_import_done:
  subject_done: "Importación de miembros completada en {{.Organization}}"
  subject_done_no_org: "Importación de miembros completada"
  subject_issues:
    one: "Importación completada con 1 error en {{.Organization}}"
    other: "Importación completada con {{.ErrorCount}} errores en {{.Organization}}"
  subject_issues_no_org:
    one: "Importación completada con 1 error"
    other: "Importación completada con {{.ErrorCount}} errores"
  subject_failed: "No se ha podido importar ningún miembro en {{.Organization}}"
  subject_failed_no_org: "No se ha podido importar ningún miembro"
  preheader_done:
    one: "1 miembro añadido correctamente."
    other: "{{.Added}} miembros añadidos correctamente."
  preheader_issues:
    one: "1 fila por revisar."
    other: "{{.ErrorCount}} filas por revisar."
  preheader_failed: "No se ha podido añadir ningún registro; revisa el formato del archivo."
  preheader_empty: "El archivo no contenía ninguna fila y no se ha añadido ningún registro."
  heading_done: "Importación completada"
  heading_issues: "Importación completada con errores"
  heading_failed: "No se ha podido importar ningún miembro"
  greeting: "Hola, {{.UserName}}."
  greeting_no_name: "Hola."
  intro_done: "¡Todo ha ido bien! La importación de miembros en *{{.Organization}}* ha terminado y se han añadido todos los registros."
  intro_done_no_org: "¡Todo ha ido bien! La importación de miembros ha terminado y se han añadido todos los registros."
  intro_issues: "La importación de miembros en *{{.Organization}}* ha terminado, pero algunas filas no se han podido importar."
  intro_issues_no_org: "La importación de miembros ha terminado, pero algunas filas no se han podido importar."
  intro_failed: "La importación de miembros en *{{.Organization}}* ha terminado, pero no se ha podido añadir ningún registro. Comprueba que el archivo siga la plantilla y vuelve a intentarlo."
  intro_failed_no_org: "La importación de miembros ha terminado, pero no se ha podido añadir ningún registro. Comprueba que el archivo siga la plantilla y vuelve a intentarlo."
  intro_empty: "La importación de miembros en *{{.Organization}}* ha terminado, pero el archivo no contenía ninguna fila y no se ha añadido ningún registro."
  intro_empty_no_org: "La importación de miembros ha terminado, pero el archivo no contenía ninguna fila y no se ha añadido ningún registro."
  label_processed: "Miembros procesados"
  label_added: "Añadidos"
  label_errors: "Errores"
  label_completed: "Finalizada"
  issues_title: "Filas que hay que revisar"
  issues_title_first: "Primeras filas que hay que revisar"
  more_issues:
    one: "y 1 error más"
    other: "y {{.MoreIssues}} errores más"
  fix_hint: "Corrige estas filas en el archivo y vuelve a importarlas; el resto ya están en la base de miembros."
  button: "Revisa la base de miembros"
  footer_why: "Recibes este correo porque administras esta organización en Vocdoni App."

support:
  subject: "Nuevo tique · {{.Type}} · {{.Title}}"
  preheader: "De {{.Email}} ({{.Organization}})."
  heading: "Nueva solicitud de soporte"
  intro: "Se ha abierto un tique desde Vocdoni App. La persona solicitante está en copia y recibirá las respuestas en este hilo."
  label_title: "Título"
  label_type: "Tipo"
  label_user: "Usuario"
  label_org: "Organización"
  label_description: "Descripción"
  truncated: "Descripción recortada a 1.200 caracteres; el texto completo queda en el tique."
  reassure: "Si has abierto tú esta solicitud, no tienes que hacer nada más: te responderemos en este mismo hilo lo antes posible."
  footer_why: "Este tique se ha creado desde el formulario de soporte de Vocdoni App."

verification_code_otp:
  subject: "Tu código para votar en {{.Organization}}"
  preheader: "Código de verificación {{.Code}}. Caduca en {{.ExpiryTime}}."
  heading: "Tu código para votar"
  intro: "*{{.Organization}}* te envía este código para verificar tu identidad antes de votar:"
  expires: "Caduca en {{.ExpiryTime}}"
  enter: "Introdúcelo en la página de la votación para continuar."
  ignore: "Si no esperabas este código, puedes ignorar este mensaje: nadie puede votar en tu nombre sin tenerlo."
  footer_why: "Recibes este mensaje porque {{.Organization}} usa esta dirección para verificar tu identidad antes de votar."
  footer_secured: "Votación protegida con la tecnología de Vocdoni"
  sms: "Código para votar: {{.Code}}. Caduca en {{.ExpiryTime}}."
```

Create the seven placeholders Task 6 fills in, each a single comment line (an empty file loads, every key
falls back to English):

```bash
for l in de el eu fr it pt pt-br; do
  printf '# %s: pending translation from en.yaml (task 6); missing keys fall back to English.\n' "$l" \
    > assets/mail/i18n/$l.yaml
done
```

2f. Start-up: load the templates unconditionally, from the new flags.

Change `cmd/service/main.go` (exact diff against the state after the previous task):

```diff
--- a/cmd/service/main.go
+++ b/cmd/service/main.go
@@ -17,6 +17,7 @@ import (
 	"github.com/spf13/viper"
 	"github.com/vocdoni/saas-backend/account"
 	"github.com/vocdoni/saas-backend/api"
+	"github.com/vocdoni/saas-backend/api/apicommon"
 	"github.com/vocdoni/saas-backend/csp"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
@@ -74,6 +75,8 @@ func main() {
 	flag.String("smtpPassword", "", "SMTP password")
 	flag.String("emailFromAddress", "", "Email service from address")
 	flag.String("emailFromName", "Vocdoni", "Email service from name")
+	flag.String("mailAccentColor", mailtemplates.DefaultAccent, "accent colour of the emails (#rrggbb): buttons and links")
+	flag.String("mailAssetsURL", "", "base URL of the logo images the emails link to (default {serverURL}/mail/assets)")
 	// Optional backup SMTP relay used for email failover. It shares the same
 	// sender identity (emailFromAddress/emailFromName); only the relay differs.
 	flag.String("backupSmtpServer", "", "Backup SMTP server (optional; enables email failover)")
@@ -246,6 +249,18 @@ func main() {
 		VoterRefSecret:                 secret,
 	}
 
+	// load and check every email template and language before any service can send one (email
+	// or the OTP SMS, which renders the same templates): a broken template must stop the start-up
+	mailBrand, err := mailtemplates.NewBrand(viper.GetString("mailAccentColor"), server, viper.GetString("mailAssetsURL"))
+	if err != nil {
+		log.Fatalf("invalid mail configuration: %v", err)
+	}
+	if err := mailtemplates.Load(mailBrand); err != nil {
+		log.Fatalf("could not load the email templates: %v", err)
+	}
+	log.Infow("email templates loaded", "templates", len(mailtemplates.All), "languages", len(apicommon.SupportedLangs),
+		"accent", mailBrand.Accent, "assets", mailBrand.AssetsURL)
+
 	// overwrite the email notifications service with the SMTP service if the
 	// required parameters are set and include it in the API configuration
 	if smtpServer != "" {
@@ -285,12 +300,6 @@ func main() {
 				"backup port", backupSMTPPort)
 		}
 		cspConf.MailService = apiConf.MailService
-		// load email templates
-		if err := mailtemplates.Load(); err != nil {
-			log.Fatalf("could not load email templates: %v", err)
-		}
-		log.Infow("email templates loaded",
-			"templates", len(mailtemplates.Available()))
 	} else if backupSMTPServer != "" {
 		// guard against a confusing misconfiguration: a backup relay is useless
 		// without a primary, and the primary block above was skipped.
```

2g. The other `Load()` callers change mechanically (their data shapes change in Task 4):

```bash
sed -i '' 's/mailtemplates\.Load()/mailtemplates.Load(mailtemplates.DefaultBrand())/g' \
  csp/csp_test.go csp/notifications/challenge_test.go csp/notifications/queue_test.go api/users_test.go
```
(GNU sed: `sed -i` without `''`.) The production call sites still pass anonymous structs; they compile, but
the Docker suites fail until Task 4 because the new templates need `Expiry`/`ExpiryTime`. Do not run them in
this task.

Run: `gofumpt -l notifications/ cmd/ && go build ./... && go vet ./...`
Expected: no output.
Run: `go test -count=1 ./notifications/... ./api/apicommon/`
Expected: every package `ok`; `notifications/mailtemplates` runs 19 top-level tests (`TestLocalesComplete`
checks ca, en, es; `TestRenderAllFixtures` renders 18 fixtures × 10 languages, all ≤ 60 KB, subjects ≤ 78;
`TestOTPSMSLength` measures 50 en, 50 ca, 54 es (with the `[STG] ` prefix)).
Run: `~/.cache/saas-backend/bin/golangci-lint run ./notifications/... ./cmd/... ./csp/... ./api/...`
Expected: `0 issues.`
Run: `git grep -l 'mypinata\|fonts.googleapis' -- ':!docs'`
Expected: only `csp/handlers/handlers.go` (removed in Task 4).

Step 3: commit
`git add notifications/mailtemplates assets/mail && git commit -m "feat(mail): render every email from one layout and per-language strings" -- notifications/mailtemplates assets cmd/service/main.go csp/csp_test.go csp/notifications/challenge_test.go csp/notifications/queue_test.go api/users_test.go`

Interfaces this task provides to later tasks:
- `type Brand struct{ Accent, AccentDark, AccentLabel, AssetsURL, ServerURL, SiteURL, SupportEmail string }`,
  `func DefaultBrand() Brand`, `func NewBrand(accent, serverURL, assetsURL string) (Brand, error)`,
  `const DefaultAccent, DefaultAccentDark`.
- `func Load(brand Brand) error`; `MailTemplate{File, WebAppURI}`, `.Localized(lang) LocalizedTemplate`,
  `LocalizedTemplate.ExecTemplate(data any)`, `.ExecPlain(data any)` (both `(*notifications.Notification,
  error)`); `var All []MailTemplate`; the eight `…Notification` vars unchanged.
- Data types `CodeData`, `InviteData`, `MembersImportResult` → `NewMembersImportData(r) MembersImportData`,
  `NewSupportData(ticketType, title, description, email, organization string) SupportData`, `OTPData`;
  `func Truncate(s string, n int) string`; consts `ImportIssuesShown` (10), `ImportIssueMaxRunes` (140),
  `SupportDescriptionMaxRunes` (1200), `MaxSubjectRunes` (78), `MaxHTMLBytes`
  (60 KiB), `ImportDone|ImportIssues|ImportFailed|ImportEmpty`.
- `func Fixtures() []Fixture` (`Fixture{Name, Template, Data}`), `const FixtureOrgLogo`.
- Unexported, used by Task 5: `assetsDir` ("assets/mail").
- Removed: `Available()`, `TemplateKey`, `TemplatePlainText`, `LocalizedTemplate.Plaintext()`,
  `LocalizedTemplate.HTMLFile/YAMLFile`.

### Task 4: call sites (codes and expiry, invite, members import, support, OTP and SMS)
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `api/users.go`, `api/organization_users.go`,
`api/org_members.go`, `api/organizations.go`, `csp/notifications/challenge.go`, `csp/auth.go`,
`csp/handlers/handlers.go`, `csp/handlers/processes.go`; tests `csp/notifications/challenge_test.go`,
`csp/notifications/queue_test.go`, `csp/auth_test.go`, `api/users_test.go`, `api/lang_test.go`,
`api/org_members_test.go`, `api/organizations_test.go`
Goal: every sender passes the new data types, so the rules of the spec apply in production: real expiry in
words, no invite code, organization logo, four import outcomes with at most 10 issues, support caps, OTP
expiry from the TTL, SMS through `ExecPlain` with a 40-character name, no `DefaultOrgLogo` (AC1, AC3a, AC4).

Behaviour that must not change: who gets each mail, when, the queue items, the `expiresAt` deadlines passed to
`sendMail`, the codes and links themselves, the cooldowns and budgets in `mailUserCode`. Only the template
data changes. `time.Until(expiration)` is used where a code is resent with its stored expiration.

Step 1: update the tests (they describe the new data and copy, and fail against Task 3's call sites)

Change `csp/notifications/challenge_test.go` (exact diff against the state after the previous task):

```diff
--- a/csp/notifications/challenge_test.go
+++ b/csp/notifications/challenge_test.go
@@ -4,6 +4,7 @@ import (
 	"context"
 	"regexp"
 	"testing"
+	"time"
 
 	"github.com/ethereum/go-ethereum/common"
 	qt "github.com/frankban/quicktest"
@@ -28,7 +29,7 @@ const (
 	adminPass         = "admin123"
 	testOrgName       = "Test Organization"
 	testOrgLogo       = "https://example.com/logo.png"
-	testRemainingTime = "5m30s"
+	testRemainingTime = 5*time.Minute + 30*time.Second
 )
 
 var testOrgAddress = common.HexToAddress("0xfafa")
@@ -180,11 +181,10 @@ func TestNewEmailChallengeSent(t *testing.T) {
 	seedNotification, err := mailtemplates.
 		VerifyOTPCodeNotification.
 		Localized("en").
-		ExecPlain(struct {
-			Code, Organization string
-		}{
+		ExecPlain(mailtemplates.OTPData{
 			Code:         `(.{6})`,
 			Organization: testOrgName,
+			ExpiryTime:   testRemainingTime,
 		})
 	c.Assert(err, qt.IsNil)
 	rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
@@ -270,6 +270,8 @@ func TestNewSMSChallengeSent(t *testing.T) {
 	c.Assert(ch.Notification.ToAddress, qt.Equals, "")
 	c.Assert(ch.Notification.ToNumber, qt.Equals, testUserPhone)
 	c.Assert(ch.Notification.PlainBody, qt.Contains, "123456")
+	c.Assert(ch.Notification.PlainBody, qt.Contains, "Expires in 6 minutes")
+	c.Assert(ch.Notification.Body, qt.Equals, "") // SMS renders the plain text only
 	c.Assert(ch.Notification.Subject, qt.Contains, testOrgName)
 	// send the notification and check the result
 	c.Assert(ch.Send(context.Background(), testSMSService), qt.IsNil)
```

Change `csp/notifications/queue_test.go` (exact diff against the state after the previous task):

```diff
--- a/csp/notifications/queue_test.go
+++ b/csp/notifications/queue_test.go
@@ -105,10 +105,9 @@ func TestNotificationChallengeQueue(t *testing.T) {
 	// create a notification without to address to force an error during the
 	// sending (compose fails: a transient, retriable error)
 	c.Assert(mailtemplates.Load(mailtemplates.DefaultBrand()), qt.IsNil)
-	notification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).ExecPlain(struct {
-		Code         string
-		Organization string
-	}{"123456", testOrgName})
+	notification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).ExecPlain(mailtemplates.OTPData{
+		Code: "123456", Organization: testOrgName, ExpiryTime: testRemainingTime,
+	})
 	c.Assert(err, qt.IsNil)
 
 	c.Run("retries reached", func(c *qt.C) {
@@ -192,7 +191,7 @@ func TestNotificationChallengeQueue(t *testing.T) {
 		queue.Start()
 
 		// templates are already loaded by the parent test; do not call
-		// mailtemplates.Load(mailtemplates.DefaultBrand()) here as it races the parallel subtests that read
+		// mailtemplates.Load() here as it races the parallel subtests that read
 		// the template map concurrently.
 		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
 			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
@@ -207,10 +206,9 @@ func TestNotificationChallengeQueue(t *testing.T) {
 			c.Assert(err, qt.IsNil)
 			// parse the email body to get the verification code
 			seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
-				ExecPlain(struct {
-					Code         string
-					Organization string
-				}{`(.{6})`, testOrgName})
+				ExecPlain(mailtemplates.OTPData{
+					Code: `(.{6})`, Organization: testOrgName, ExpiryTime: testRemainingTime,
+				})
 			c.Assert(err, qt.IsNil)
 			rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
 			// verify the user
```

Change `csp/auth_test.go` (exact diff against the state after the previous task):

```diff
--- a/csp/auth_test.go
+++ b/csp/auth_test.go
@@ -150,11 +150,10 @@ func TestAuthToken(t *testing.T) {
 		c.Assert(err, qt.IsNil)
 		// parse the email body to get the verification code
 		seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
-			ExecPlain(struct {
-				Code             string
-				Organization     string
-				OrganizationLogo string
-			}{`(.{6})`, testOrgName, testOrgLogo})
+			ExecPlain(mailtemplates.OTPData{
+				// the test CSPs keep codes for 5-30 seconds, shown as "1 minute"
+				Code: `(.{6})`, Organization: testOrgName, OrganizationLogo: testOrgLogo, ExpiryTime: time.Minute,
+			})
 		c.Assert(err, qt.IsNil)
 		rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
 		// verify the user
@@ -650,11 +649,9 @@ func fetchOTPCodeFromEmail(c *qt.C, email string) string {
 	c.Assert(err, qt.IsNil)
 
 	seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
-		ExecPlain(struct {
-			Code             string
-			Organization     string
-			OrganizationLogo string
-		}{`(.{6})`, testOrgName, testOrgLogo})
+		ExecPlain(mailtemplates.OTPData{
+			Code: `(.{6})`, Organization: testOrgName, OrganizationLogo: testOrgLogo, ExpiryTime: time.Minute,
+		})
 	c.Assert(err, qt.IsNil)
 
 	rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
```

Change `api/users_test.go` (exact diff against the state after the previous task):

```diff
--- a/api/users_test.go
+++ b/api/users_test.go
@@ -34,10 +34,7 @@ func init() {
 	// wrap the mail template execution to force plain body and set the regex
 	// needle as the verification code
 	testTemplateExec := func(mt mailtemplates.LocalizedTemplate) (*notifications.Notification, error) {
-		n, err := mt.ExecTemplate(struct {
-			Code string
-			Link string
-		}{codeRgx, ""})
+		n, err := mt.ExecTemplate(mailtemplates.CodeData{Code: codeRgx, Expiry: time.Minute})
 		if err != nil {
 			return nil, err
 		}
```

Change `api/lang_test.go` (exact diff against the state after the previous task):

```diff
--- a/api/lang_test.go
+++ b/api/lang_test.go
@@ -148,9 +148,9 @@ func TestOrgDefaultLangNotifications(t *testing.T) {
 // behalf of the organization, so its language wins over the admin's lang param.
 func TestOrgLangInInvites(t *testing.T) {
 	inviteRegexps := map[string]*regexp.Regexp{
-		"en": regexp.MustCompile(`You have been invited`),
-		"es": regexp.MustCompile(`Has sido invitado`),
-		"ca": regexp.MustCompile(`Has estat convidat`),
+		"en": regexp.MustCompile(`You've been invited`),
+		"es": regexp.MustCompile(`Te han invitado`),
+		"ca": regexp.MustCompile(`T’han convidat`),
 	}
 
 	// set up an organization with catalan as default language
@@ -193,9 +193,9 @@ func TestOrgDefaultLangInMembersImport(t *testing.T) {
 	// the completion email to the admin must be in the org default language
 	assertContentMatches(t, waitForEmail(t, adminEmail), "ca",
 		map[string]*regexp.Regexp{
-			"en": regexp.MustCompile(`has been completed`),
-			"es": regexp.MustCompile(`se ha completado`),
-			"ca": regexp.MustCompile(`s'ha completat`),
+			"en": regexp.MustCompile(`Import completed`),
+			"es": regexp.MustCompile(`Importación completada`),
+			"ca": regexp.MustCompile(`Importació completada`),
 		})
 }
```

Change `api/org_members_test.go` (exact diff against the state after the previous task):

```diff
--- a/api/org_members_test.go
+++ b/api/org_members_test.go
@@ -329,7 +329,7 @@ func TestOrganizationMembers(t *testing.T) {
 
 	// Check that the completion email was sent
 	mailBody := waitForEmail(t, user.Email)
-	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`(?i)\s(has been completed)\s`),
+	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`Import completed`),
 		qt.Commentf("mail content does not, got:\n%s", mailBody))
 
 	// Test 5.1: GET /jobs — basic
```

Change `api/organizations_test.go` (exact diff against the state after the previous task):

```diff
--- a/api/organizations_test.go
+++ b/api/organizations_test.go
@@ -302,7 +302,7 @@ func TestOrganizationCreateTicketHandler(t *testing.T) {
 
 	// Check that cc'ed email arrives correctly to the mailbox
 	mailBody := waitForEmail(t, apicommon.SupportEmail)
-	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`(?i)\s(You have a new support request)\s`))
+	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`New support request`))
 
 	// Test without authentication
 	_, code = testRequest(t, http.MethodPost, "", ticketReq, "organizations", orgAddress.String(), "ticket")
```

Why each change: the OTP seeds now pass `ExpiryTime` (the plain text shows the expiry; the test CSPs use
5–30 s TTLs, shown as "1 minute"; `challenge_test` uses 5m30s, shown as "6 minutes"); `testRemainingTime`
becomes a `time.Duration` because `NewNotificationChallenge` takes one; `users_test` builds its code regexps
from `CodeData`; the copy regexes follow the new strings (the decoded body holds the plain text first).

Run: `go vet ./csp/... ./api/...`
Expected: `csp/notifications/challenge_test.go:91:3: cannot use testRemainingTime (constant 330000000000 of
int64 type time.Duration) as string value in argument to NewNotificationChallenge` (the production signature
is still the old one).

Step 2: implement

Change `api/users.go` (exact diff against the state after the previous task):

```diff
--- a/api/users.go
+++ b/api/users.go
@@ -105,10 +105,9 @@ func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
 	// send the verification mail to the user email with the verification code
 	// and the verification link
 	if err := a.sendMail(r.Context(), nil, userInfo.Email,
-		mailtemplates.VerifyAccountNotification, struct {
-			Code string
-			Link string
-		}{code, link},
+		mailtemplates.VerifyAccountNotification, mailtemplates.CodeData{
+			Code: code, Link: link, Email: userInfo.Email, Expiry: a.otpExpiry,
+		},
 		time.Now().Add(a.otpExpiry),
 	); err != nil {
 		log.Warnw("could not send verification code", "error", err)
@@ -362,10 +361,9 @@ func (a *API) resendUserVerificationCodeHandler(w http.ResponseWriter, r *http.R
 		}
 		// resend the existing verification code
 		if err := a.sendMail(r.Context(), nil, user.Email, mailtemplates.VerifyAccountNotification,
-			struct {
-				Code string
-				Link string
-			}{code, link},
+			mailtemplates.CodeData{
+				Code: code, Link: link, Email: user.Email, Expiry: time.Until(userVerification.Expiration),
+			},
 			userVerification.Expiration,
 		); err != nil {
 			log.Warnw("could not resend verification code", "error", err)
@@ -389,10 +387,9 @@ func (a *API) resendUserVerificationCodeHandler(w http.ResponseWriter, r *http.R
 	// send the verification mail to the user email with the verification code
 	// and the verification link
 	if err := a.sendMail(r.Context(), nil, user.Email, mailtemplates.VerifyAccountNotification,
-		struct {
-			Code string
-			Link string
-		}{newCode, link},
+		mailtemplates.CodeData{
+			Code: newCode, Link: link, Email: user.Email, Expiry: a.otpExpiry,
+		},
 		time.Now().Add(a.otpExpiry),
 	); err != nil {
 		log.Warnw("could not send verification code", "error", err)
@@ -616,10 +613,9 @@ func (a *API) sendEmailUpdateCode(ctx context.Context, user *db.User, pendingEma
 		return fmt.Errorf("could not build email update link: %w", err)
 	}
 	if err := a.sendMail(ctx, nil, pendingEmail, mailtemplates.VerifyEmailChangeNotification,
-		struct {
-			Code string
-			Link string
-		}{code, link},
+		mailtemplates.CodeData{
+			Code: code, Link: link, Email: pendingEmail, Expiry: a.otpExpiry,
+		},
 		expiration,
 	); err != nil {
 		return fmt.Errorf("could not send email update code: %w", err)
@@ -1101,10 +1097,9 @@ func (a *API) mailUserCode(ctx context.Context, p mailCodeParams) error {
 		return fmt.Errorf("%w: %w", errCodeSendUnchecked, err)
 	}
 	return a.sendMail(ctx, nil, user.Email, template,
-		struct {
-			Code string
-			Link string
-		}{code, link},
+		mailtemplates.CodeData{
+			Code: code, Link: link, Email: user.Email, Expiry: time.Until(expiration),
+		},
 		expiration,
 	)
 }
```

Change `api/organization_users.go` (exact diff against the state after the previous task):

```diff
--- a/api/organization_users.go
+++ b/api/organization_users.go
@@ -176,8 +176,8 @@ func (a *API) inviteOrganizationUserHandler(w http.ResponseWriter, r *http.Reque
 		Role:                invite.Role,
 		CurrentUserID:       user.ID,
 	}
-	// generate the verification code and the verification link
-	code, link, err := a.generateVerificationCodeAndLink(orgInvite, db.CodeTypeOrgInvite)
+	// generate the invitation code; the mail carries it only inside the link
+	_, link, err := a.generateVerificationCodeAndLink(orgInvite, db.CodeTypeOrgInvite)
 	if err == db.ErrAlreadyExists {
 		errors.ErrDuplicateConflict.With("user is already invited to the organization").Write(w)
 		return
@@ -192,11 +192,12 @@ func (a *API) inviteOrganizationUserHandler(w http.ResponseWriter, r *http.Reque
 	// the invite link
 	if err := a.sendMail(
 		r.Context(), org, invite.Email, mailtemplates.InviteNotification,
-		struct {
-			Organization string
-			Code         string
-			Link         string
-		}{apicommon.OrgDisplayName(org.Meta, org.Address.String()), code, link},
+		mailtemplates.InviteData{
+			Organization:     org.DisplayName(),
+			OrganizationLogo: org.LogoURL(),
+			Link:             link,
+			Expiry:           apicommon.InvitationExpiration,
+		},
 		time.Now().Add(apicommon.InvitationExpiration),
 	); err != nil {
 		// the invite was created, just the notification failed
@@ -468,11 +469,12 @@ func (a *API) updatePendingUserInvitationHandler(w http.ResponseWriter, r *http.
 	// the invite link
 	if err := a.sendMail(
 		r.Context(), org, orgInvite.NewUserEmail, mailtemplates.InviteNotification,
-		struct {
-			Organization string
-			Code         string
-			Link         string
-		}{apicommon.OrgDisplayName(org.Meta, org.Address.String()), code, link},
+		mailtemplates.InviteData{
+			Organization:     org.DisplayName(),
+			OrganizationLogo: org.LogoURL(),
+			Link:             link,
+			Expiry:           apicommon.InvitationExpiration,
+		},
 		time.Now().Add(apicommon.InvitationExpiration),
 	); err != nil {
 		log.Warnw("could not send verification code email", "error", err)
```

Change `api/org_members.go` (exact diff against the state after the previous task):

```diff
--- a/api/org_members.go
+++ b/api/org_members.go
@@ -16,18 +16,6 @@ import (
 	"go.vocdoni.io/dvote/util"
 )
 
-// MembersImportCompletionData represents the data structure for members import completion email template
-type MembersImportCompletionData struct {
-	OrganizationName string
-	UserName         string
-	Link             string
-	TotalMembers     int
-	AddedMembers     int
-	ErrorCount       int
-	Errors           []string
-	CompletedAt      time.Time
-}
-
 // sendMembersImportCompletionEmail sends an email notification when members import is completed.
 // It runs detached from the request, so ctx must come from
 // context.WithoutCancel: NotificationLang needs the request values.
@@ -38,21 +26,24 @@ func (a *API) sendMembersImportCompletionEmail(ctx context.Context, user *db.Use
 		return // Email service not configured
 	}
 
+	// without a link the mail still goes out, just without the button
 	link, err := a.buildWebAppURL("/admin/memberbase/", nil)
 	if err != nil {
 		log.Errorf("failed to build web app URL for members import completion email: %v", err)
+		link = ""
 	}
 
-	data := MembersImportCompletionData{
-		OrganizationName: org.DisplayName(),
-		UserName:         user.FirstName + " " + user.LastName,
-		TotalMembers:     progress.Total,
-		AddedMembers:     progress.Added,
+	// the data caps the listed issues, so a file with thousands of bad rows still yields a small mail
+	data := mailtemplates.NewMembersImportData(mailtemplates.MembersImportResult{
+		Organization:     org.DisplayName(),
+		OrganizationLogo: org.LogoURL(),
+		UserName:         user.FirstName,
 		Link:             link,
-		ErrorCount:       len(progress.Errors),
+		Total:            progress.Total,
+		Added:            progress.Added,
 		Errors:           progress.ErrorsAsStrings(),
 		CompletedAt:      time.Now(),
-	}
+	})
 
 	if err := a.sendMail(ctx, org, user.Email, mailtemplates.MembersImportCompletionNotification,
 		data, time.Time{}); err != nil {
```

Change `api/organizations.go` (exact diff against the state after the previous task):

```diff
--- a/api/organizations.go
+++ b/api/organizations.go
@@ -476,19 +476,8 @@ func (a *API) organizationCreateTicket(w http.ResponseWriter, r *http.Request) {
 	// people, so it must not carry the organization's language
 	lang := apicommon.NotificationLang(r.Context(), nil)
 	notification, err := mailtemplates.SupportNotification.Localized(lang).ExecTemplate(
-		struct {
-			Type         string
-			Organization string
-			Title        string
-			Description  string
-			Email        string
-		}{
-			Type:         ticketReq.TicketType,
-			Organization: apicommon.OrgDisplayName(org.Meta, org.Address.String()),
-			Title:        ticketReq.Title,
-			Description:  ticketReq.Description,
-			Email:        user.Email,
-		},
+		mailtemplates.NewSupportData(ticketReq.TicketType, ticketReq.Title, ticketReq.Description, user.Email,
+			apicommon.OrgDisplayName(org.Meta, org.Address.String())),
 	)
 	if err != nil {
 		log.Warnw("could not execute support notification template", "error", err)
```

Change `csp/notifications/challenge.go` (exact diff against the state after the previous task):

```diff
--- a/csp/notifications/challenge.go
+++ b/csp/notifications/challenge.go
@@ -96,7 +96,8 @@ func (nc *NotificationChallenge) Send(ctx context.Context, service notifications
 }
 
 // NewNotificationChallenge creates a new notification challenge based on the
-// provided parameters. It returns an error if the notification could not be
+// provided parameters. expiry is how long the code stays valid, shown to the
+// voter in words. It returns an error if the notification could not be
 // created.
 func NewNotificationChallenge(
 	cType notifications.NotificationType,
@@ -104,30 +105,39 @@ func NewNotificationChallenge(
 	userID, anchorID internal.HexBytes,
 	to, code string,
 	orgInfo OrganizationInfo,
-	remainingTime string,
+	expiry time.Duration,
 ) (
 	*NotificationChallenge, error,
 ) {
 	if userID == nil || anchorID == nil || to == "" || code == "" {
 		return nil, ErrInvalidNotificationInputs
 	}
-	n, err := mailtemplates.VerifyOTPCodeNotification.Localized(lang).ExecTemplate(struct {
-		Code             string
-		Organization     string
-		OrganizationLogo string
-		ExpiryTime       string
-	}{code, orgInfo.Name, orgInfo.Logo, remainingTime})
-	if err != nil {
-		return nil, errors.Join(ErrCreateNotification, err)
+	data := mailtemplates.OTPData{
+		Code:             code,
+		Organization:     orgInfo.Name,
+		OrganizationLogo: orgInfo.Logo,
+		ExpiryTime:       expiry,
 	}
+	template := mailtemplates.VerifyOTPCodeNotification.Localized(lang)
+	var n *notifications.Notification
+	var err error
 	switch cType {
 	case EmailChallenge:
-		n.ToAddress = to
+		if n, err = template.ExecTemplate(data); err == nil {
+			n.ToAddress = to
+		}
 	case SMSChallenge:
-		n.ToNumber = to
+		// the SMS is the plain text alone (one UCS-2 segment, no organization name)
+		if n, err = template.ExecPlain(data); err == nil {
+			n.ToNumber = to
+		}
 	default:
 		return nil, ErrInvalidNotificationType
 	}
+	if err != nil {
+		return nil, errors.Join(ErrCreateNotification, err)
+	}
 	return &NotificationChallenge{
 		OrgAddress:   orgInfo.Address,
 		UserID:       userID,
```

Change `csp/auth.go` (exact diff against the state after the previous task):

```diff
--- a/csp/auth.go
+++ b/csp/auth.go
@@ -77,7 +77,7 @@ func (c *CSP) AuthToken(anchorID, uID internal.HexBytes, to string,
 		Name:    orgName,
 		Logo:    orgLogo,
 	}
-	ch, err := notifications.NewNotificationChallenge(ctype, lang, uID, anchorID, to, code, orgInfo, c.notificationTTL.String())
+	ch, err := notifications.NewNotificationChallenge(ctype, lang, uID, anchorID, to, code, orgInfo, c.notificationTTL)
 	if err != nil {
 		log.Warnw("error composing notification challenge",
 			"userID", uID,
@@ -258,7 +258,7 @@ func (c *CSP) ResendChallenge(token internal.HexBytes, to string,
 		to,
 		code,
 		orgInfo,
-		remainingTime.String(),
+		remainingTime,
 	)
 	if err != nil {
 		log.Warnw("error composing notification challenge",
```

Change `csp/handlers/handlers.go` (exact diff against the state after the previous task):

```diff
--- a/csp/handlers/handlers.go
+++ b/csp/handlers/handlers.go
@@ -11,11 +11,8 @@ import (
 	"go.mongodb.org/mongo-driver/v2/bson"
 )
 
-const (
-	DefaultOrgName = "Vocdoni"
-	DefaultOrgLogo = "https://tomato-giant-grasshopper-196.mypinata.cloud/ipfs/" +
-		"bafkreifqyu5m5as4gvcirlog5j267um24q7y4ri6r3svhsi7fda24676ny"
-)
+// DefaultOrgName names the organization in the voter's OTP when it has no name.
+const DefaultOrgName = "Vocdoni"
 
 // CSPHandlers is a struct that contains an instance of the CSP and the main
 // database (where the process and census data is stored). It is used to handle
```

Change `csp/handlers/processes.go` (exact diff against the state after the previous task):

```diff
--- a/csp/handlers/processes.go
+++ b/csp/handlers/processes.go
@@ -1134,14 +1134,14 @@ func (c *CSPHandlers) orgMember(orgAddress common.Address, auth *db.CSPAuth) (*d
 	return c.mainDB.OrgMember(orgAddress, oid.Hex())
 }
 
-// orgNameAndLogo returns the organization display name and logo, falling back to defaults.
+// orgNameAndLogo returns the organization display name, DefaultOrgName when it has none, and its
+// logo URL, empty when it has none (the OTP email then shows a monogram; the mail templates show
+// only logos kept in this service's storage).
 func orgNameAndLogo(org *db.Organization) (name, logo string) {
-	name, logo = DefaultOrgName, DefaultOrgLogo
+	name = DefaultOrgName
 	if n := org.DisplayName(); n != "" {
 		name = n
-		if l := org.LogoURL(); l != "" {
-			logo = l
-		}
+		logo = org.LogoURL()
 	}
 	return name, logo
 }
```

Notes for the reviewer:
- `api/org_members.go`: the exported `MembersImportCompletionData` type is removed (its only user was this
  function); `ErrorsAsStrings()` still builds every string once, then `NewMembersImportData` keeps 10 of them
  truncated to 140 characters, so the email is bounded whatever the file.
- `api/organization_users.go`: the first invite no longer needs the code (it is only inside the link); the
  update path still stores it in `orgInvite.InvitationCode`.
- `csp/handlers`: `DefaultOrgLogo` and the IPFS URL are gone; `orgNameAndLogo` returns an empty logo when
  the organization has none and the OTP shows the monogram. Logos that are not ours are dropped by the
  renderer (Task 3), not here.
- `csp/notifications/challenge.go`: email renders HTML and plain text; SMS renders only the plain text (`ExecPlain`, one UCS-2 segment, no organization name). A failed render is still `ErrCreateNotification`.

Run: `go build ./... && go vet ./... && go test -count=1 ./notifications/... ./csp/testutil/ ./api/apicommon/`
Expected: vet silent, all `ok`.
Run: `git grep -l 'mypinata\|fonts.googleapis' -- ':!docs'`
Expected: no output (exit status 1).
Run (Docker): `go test -count=1 -run 'TestNewEmailChallengeSent|TestNewSMSChallengeSent|TestNotificationChallengeQueue' ./csp/notifications/`
Expected: `ok  github.com/vocdoni/saas-backend/csp/notifications`.
Run (Docker): `go test -count=1 -run 'TestAuthToken|TestVerifyAuthToken|TestResendChallenge|TestAuthTokenResendAndVerifyFlow' ./csp/`
Expected: `ok  github.com/vocdoni/saas-backend/csp`.
Run (Docker): `go test -count=1 -run 'TestOrgLangInCSPEmails|TestLanguageParameterInUserRegistration|TestOrgDefaultLangNotifications|TestOrgLangInInvites|TestOrgDefaultLangInMembersImport|TestDefaultLangIsNormalized|TestOrganizationCreateTicketHandler|TestOrganizationMembers$|TestRegisterHandler|TestVerifyAccountHandler|TestRecoverAndResetPassword|TestResendVerificationCodeHandler|TestUpdateUserInfo|TestOrganizationUsers$|TestRecoveryUnverifiedAccountGetsReset|TestPasswordSetupFlow|TestCSPVoting' ./api/`
Expected: `ok  github.com/vocdoni/saas-backend/api`. If one fails on a copy regex, print the mail body from the
failure comment and compare with the strings of Task 3; do not loosen the negative (other-language) checks.
Run: `~/.cache/saas-backend/bin/golangci-lint run ./csp/... ./api/...`
Expected: `0 issues.`

Step 3: commit
`git commit -m "feat(mail): send the redesigned emails with bounded data" -- api/users.go api/organization_users.go api/org_members.go api/organizations.go csp/notifications/challenge.go csp/auth.go csp/handlers/handlers.go csp/handlers/processes.go csp/notifications/challenge_test.go csp/notifications/queue_test.go csp/auth_test.go api/users_test.go api/lang_test.go api/org_members_test.go api/organizations_test.go`

Interfaces this task provides to later tasks: `notifications.NewNotificationChallenge(…, expiry time.Duration)`
(was `remainingTime string`); `api.MembersImportCompletionData` removed; `handlers.DefaultOrgLogo` removed.

### Task 5: logos, `GET /mail/assets/{file}`, logo generator, swagger
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `assets/mail/images/*.png` (12, new),
`notifications/mailtemplates/assets.go` (new), `notifications/mailtemplates/assets_test.go` (new),
`api/routes.go`, `api/api.go`, `api/mail_assets.go` (new), `api/mail_assets_test.go` (new),
`scripts/mail-logos/make-logos.py` (new), `docs/swagger.yaml` (generated)
Goal: the Vocdoni logos the emails link to are served by this backend from the embedded files only, with a
day of caching and a strong ETag (Goal 5, AC5).

Step 1: add the images and write the failing tests

Copy the approved PNGs (rendered from vocdoni-app `feat/fil-voter-flow` `Logo.tsx`; the generator below
reproduces them with the same pixel sizes):

```bash
mkdir -p assets/mail/images
cp ~/.cache/saas-backend/mockups/logo/vocdoni-{wordmark,mark}-{black,white,black-halo}@{2x,3x}.png assets/mail/images/
ls assets/mail/images | wc -l   # 12
```

Create `notifications/mailtemplates/assets_test.go`:

```go
package mailtemplates

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestServeAsset(t *testing.T) {
	c := qt.New(t)
	get := func(name, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/mail/assets/x", http.NoBody)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		ServeAsset(rec, req, name)
		return rec
	}
	rec := get("vocdoni-wordmark-black-halo@2x.png", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get("Content-Type"), qt.Equals, "image/png")
	c.Assert(rec.Header().Get("Cache-Control"), qt.Equals, "public, max-age=86400")
	etag := rec.Header().Get("ETag")
	c.Assert(etag, qt.Matches, `"[0-9a-f]{32}"`)
	c.Assert(rec.Body.Len() > 0, qt.IsTrue)
	c.Assert(get("vocdoni-wordmark-black-halo@2x.png", etag).Code, qt.Equals, http.StatusNotModified)
	for _, name := range []string{
		"", "../layout.html", "..%2Flayout.html", "layout.html", "i18n", "nope.png",
		"../../go.mod", "images/vocdoni-mark-black@2x.png",
	} {
		c.Assert(get(name, "").Code, qt.Equals, http.StatusNotFound, qt.Commentf("%q", name))
	}
}
```

Create `api/mail_assets_test.go`:

```go
package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestMailAssets(t *testing.T) {
	c := qt.New(t)
	get := func(path string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, testURL(path), http.NoBody)
		c.Assert(err, qt.IsNil)
		resp, err := http.DefaultClient.Do(req)
		c.Assert(err, qt.IsNil)
		c.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	resp := get("/mail/assets/vocdoni-wordmark-black-halo@2x.png")
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Header.Get("Content-Type"), qt.Equals, "image/png")
	c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "public, max-age=86400")
	c.Assert(resp.Header.Get("ETag"), qt.Not(qt.Equals), "")
	for _, path := range []string{
		"/mail/assets/unknown.png",
		"/mail/assets/..%2Flayout.html",
		"/mail/assets/..%2F..%2Fgo.mod",
		"/mail/assets/i18n",
		"/mail/assets/../layout.html",
	} {
		c.Assert(get(path).StatusCode, qt.Equals, http.StatusNotFound, qt.Commentf("%s", path))
	}
}
```

Run: `go test -count=1 -run TestServeAsset ./notifications/mailtemplates/`
Expected: build failure `undefined: ServeAsset`.

Step 2: implement

Create `notifications/mailtemplates/assets.go`:

```go
package mailtemplates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"
	"time"

	root "github.com/vocdoni/saas-backend"
)

// AssetsRoute is the public route that serves the Vocdoni logos the emails link to.
const AssetsRoute = "/mail/assets/{file}"

// asset is one embedded image with its strong ETag.
type asset struct {
	data []byte
	etag string
}

// assets indexes assets/mail/images once; only these exact names are ever served.
var assets = sync.OnceValues(func() (map[string]asset, error) {
	entries, err := fs.ReadDir(root.Assets, assetsDir+"/images")
	if err != nil {
		return nil, err
	}
	out := make(map[string]asset, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := root.Assets.ReadFile(assetsDir + "/images/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out[e.Name()] = asset{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
	}
	return out, nil
})

// ServeAsset writes the embedded mail image called name, or 404 for any other name (no paths, no
// listing). Images are cached for a day and revalidated by ETag; a changed artwork ships under a
// new file name.
func ServeAsset(w http.ResponseWriter, r *http.Request, name string) {
	index, err := assets()
	if err != nil {
		http.Error(w, "mail assets unavailable", http.StatusInternalServerError)
		return
	}
	a, ok := index[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", a.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.data))
}
```

Create `api/mail_assets.go`:

```go
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// mailAssetHandler godoc
//
//	@Summary		Get an email image
//	@Description	Get one of the Vocdoni logo images the transactional emails link to. Only the images
//	@Description	embedded in the service are served; any other name is a 404. Cached for a day and
//	@Description	revalidated by ETag.
//	@Tags			mail
//	@Produce		image/png
//	@Param			file	path		string	true	"Image file name, e.g. vocdoni-wordmark-black-halo@2x.png"
//	@Success		200		{file}		binary	"The image"
//	@Success		304		{string}	string	"Not modified: If-None-Match matched the ETag"
//	@Failure		404		{string}	string	"No such image"
//	@Router			/mail/assets/{file} [get]
func mailAssetHandler(w http.ResponseWriter, r *http.Request) {
	mailtemplates.ServeAsset(w, r, chi.URLParam(r, "file"))
}
```

Change `api/routes.go` (exact diff against the state after the previous task):

```diff
--- a/api/routes.go
+++ b/api/routes.go
@@ -130,6 +130,8 @@ const (
 	objectStorageUploadTypedEndpoint = "/storage"
 	// GET /storage/{origin}/{filename} to download an image from the object storage
 	objectStorageDownloadTypedEndpoint = "/storage/{objectName}"
+	// GET /mail/assets/{file} to get a Vocdoni logo the emails link to
+	mailAssetsEndpoint = "/mail/assets/{file}"
 
 	// POST /vote to relay an already-signed vote (public). The target process is taken
 	// from the signed vote envelope itself, so no process id appears in the path.
```

Change `api/api.go` (exact diff against the state after the previous task):

```diff
--- a/api/api.go
+++ b/api/api.go
@@ -50,6 +50,9 @@
 //	@tag.name					storage
 //	@tag.description			Object storage operations
 //
+//	@tag.name					mail
+//	@tag.description			Images the transactional emails link to
+//
 //	@tag.name					health
 //	@tag.description			Service health & info
 package api
@@ -585,6 +588,7 @@ func (a *API) initRouter() http.Handler {
 			a.stripeHandlers.HandleWebhook(w, r)
 		})
 		handle(r, http.MethodGet, objectStorageDownloadTypedEndpoint, a.objectStorage.DownloadImageInlineHandler)
+		handle(r, http.MethodGet, mailAssetsEndpoint, mailAssetHandler)
 		handle(r, http.MethodGet, jobStatusEndpoint, a.jobStatusHandler)
 		handle(r, http.MethodPost, voteEndpoint, a.relayVoteHandler)
 		handle(r, http.MethodPost, votesEndpoint, a.relayVotesHandler)
```

Create `scripts/mail-logos/make-logos.py`:

```python
#!/usr/bin/env python3
"""Regenerate assets/mail/images from the Vocdoni logo in vocdoni-app.

Reads the ten SVG paths of src/components/Layout/Logo.tsx at a git ref of a vocdoni-app checkout,
writes the wordmark and the mark in black, white and black with a white halo (legible when a mail
client forces a dark background without inverting images), and rasterizes each at 2x and 3x of
the size the emails show (wordmark 120px wide, mark 40px wide) with rsvg-convert.

Not run at build time. If the artwork changes, give the PNGs new file names: mail clients and
proxies cache /mail/assets/{file} for a day.

    scripts/mail-logos/make-logos.py --app ~/Repos/vocdoni-app --ref feat/fil-voter-flow \
        --out assets/mail/images
"""
import argparse
import pathlib
import re
import subprocess
import tempfile

WIDTHS = {'wordmark': 120, 'mark': 40}
VIEWBOXES = {'wordmark': (0, 0, 1000.4, 215.5), 'mark': (0, 0, 251.8, 215.5)}
TONES = [('black', '#000000', None), ('white', '#ffffff', None), ('black-halo', '#000000', '#ffffff')]


def svg(paths, viewbox, fill, stroke=None):
    attrs = f' stroke="{stroke}" stroke-width="22" stroke-linejoin="round" paint-order="stroke"' if stroke else ''
    pad = 12 if stroke else 0
    x, y, w, h = viewbox
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{x - pad} {y - pad} {w + 2 * pad} {h + 2 * pad}">'
            + ''.join(f'<path d="{p}" fill="{fill}"{attrs}/>' for p in paths) + '</svg>')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--app', required=True, help='path to a vocdoni-app checkout')
    parser.add_argument('--ref', default='feat/fil-voter-flow', help='git ref to read Logo.tsx from')
    parser.add_argument('--out', required=True, help='output directory, normally assets/mail/images')
    args = parser.parse_args()

    source = subprocess.run(['git', '-C', args.app, 'show', f'{args.ref}:src/components/Layout/Logo.tsx'],
                            capture_output=True, text=True, check=True).stdout
    paths = re.findall(r"\sd='([^']+)'", source)
    if len(paths) != 10:
        raise SystemExit(f'expected 10 paths in Logo.tsx, found {len(paths)}')
    shapes = {'wordmark': paths, 'mark': paths[:2]}
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    (pathlib.Path.home() / '.cache' / 'saas-backend').mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=pathlib.Path.home() / '.cache' / 'saas-backend') as tmp:
        for name, shape in shapes.items():
            for tone, fill, stroke in TONES:
                source_svg = pathlib.Path(tmp) / f'vocdoni-{name}-{tone}.svg'
                source_svg.write_text(svg(shape, VIEWBOXES[name], fill, stroke))
                for scale in (2, 3):
                    png = out / f'vocdoni-{name}-{tone}@{scale}x.png'
                    subprocess.run(['rsvg-convert', '-w', str(WIDTHS[name] * scale), '-o', str(png), str(source_svg)],
                                   check=True)
                    print(f'wrote {png}')


if __name__ == '__main__':
    main()
```

`chmod +x scripts/mail-logos/make-logos.py`. Optional check (needs a vocdoni-app checkout and `rsvg-convert`):
`mkdir -p ~/.cache/saas-backend/logos-check && python3 -I scripts/mail-logos/make-logos.py --app ~/Repos/vocdoni-app --out ~/.cache/saas-backend/logos-check`
writes 12 PNGs whose sizes match `assets/mail/images` (wordmark 240×52/360×78, halo 240×57/360×85, mark
80×69/120×103, halo mark 80×70/120×105). Do not overwrite the committed PNGs.

The route sits in the public group, so the `originSecret` middleware still applies (public traffic comes
through the edge proxy that adds the header) and so does the global throttle.

Swagger: `PATH=$(go env GOPATH)/bin:$PATH make swagger`
Expected: `docs/swagger.yaml` gains the `/mail/assets/{file}` path (about 30 lines, tag `mail`) and nothing
else. No annotation names the language list, so the ten languages need no swagger change.

Run: `go build ./... && go vet ./... && go test -count=1 ./notifications/mailtemplates/`
Expected: `ok` (20 top-level tests).
Run (Docker): `go test -count=1 -run 'TestMailAssets' ./api/`
Expected: `ok  github.com/vocdoni/saas-backend/api`.
Run: `~/.cache/saas-backend/bin/golangci-lint run ./notifications/... ./api/...`
Expected: `0 issues.`

Step 3: commit
`git add assets/mail/images notifications/mailtemplates/assets.go notifications/mailtemplates/assets_test.go api/mail_assets.go api/mail_assets_test.go scripts/mail-logos/make-logos.py && git commit -m "feat(mail): serve the vocdoni logos for the emails" -- assets/mail/images notifications/mailtemplates/assets.go notifications/mailtemplates/assets_test.go api/routes.go api/api.go api/mail_assets.go api/mail_assets_test.go scripts/mail-logos/make-logos.py docs/swagger.yaml`

Interfaces this task provides to later tasks: `func mailtemplates.ServeAsset(w, r, name string)`,
`const mailtemplates.AssetsRoute`; public route `GET /mail/assets/{file}`; the 12 PNG names the layout uses.

### Task 6: seven translations (de, el, eu, fr, it, pt, pt-br)
Model: sonnet (one dispatch per language is fine; the test change in Step 3 goes in the last one)
Repo: /Users/ferran/Repos/saas-backend   Files: `assets/mail/i18n/{de,el,eu,fr,it,pt,pt-br}.yaml`,
`notifications/mailtemplates/mailtemplates_test.go` (one line)
Goal: complete strings in the ten app locales (Goal 3), translated from the reviewed English; native review
before release is tracked outside this plan and does not block the merge.

Step 1: translate. For each language, start from a copy of `assets/mail/i18n/en.yaml` (the schema in
Task 3) and translate every value, keeping:
- every key, the nesting, and the `one`/`other` maps; the same `{{.Field}}` names, exactly as written, only
  those the English string uses (you may reorder them); `*emphasis*` around the same field;
- double-quoted YAML; no HTML; sentence case; no em dash; "Vocdoni App" untranslated in running text;
  "Vocdoni" untranslated; gender-neutral wording (address the reader directly, avoid gendered participles
  about them: for example de "du"-form, fr "vous" with neutral turns, it "tu" with neutral turns);
- no elision or article that depends on the organization's name (fr/it/pt: write "de l’organisation
  {{.Organization}}" / "dell’organizzazione {{.Organization}}" / "da organização {{.Organization}}" rather than
  attaching a contraction to the name);
- `verification_code_otp.sms` on one line, ≤ 64 characters rendered with a 6-digit code and '15 minutes' in that language (the per-locale test asserts ≤ 70 with the `[STG] ` prefix); it names no organization; use the shortest natural wording; plain apostrophes;
- the first comment line of the file in that language, saying it is translated from English and pending a
  native review.

Use exactly these locale formats (the date sentence is a template with `{{.Day}}`, `{{.Month}}`, `{{.Year}}`;
months are the list item for that month as it appears inside the date):

| Locale | `number.group` | `date.format` | `date.months` |
|---|---|---|---|
| de | `"."` | `"{{.Day}}. {{.Month}} {{.Year}}"` | Januar, Februar, März, April, Mai, Juni, Juli, August, September, Oktober, November, Dezember |
| el | `"."` | `"{{.Day}} {{.Month}} {{.Year}}"` | genitive: Ιανουαρίου, Φεβρουαρίου, Μαρτίου, Απριλίου, Μαΐου, Ιουνίου, Ιουλίου, Αυγούστου, Σεπτεμβρίου, Οκτωβρίου, Νοεμβρίου, Δεκεμβρίου |
| eu | `"."` | `"{{.Year}}ko {{.Month}} {{.Day}}a"` | urtarrilaren, otsailaren, martxoaren, apirilaren, maiatzaren, ekainaren, uztailaren, abuztuaren, irailaren, urriaren, azaroaren, abenduaren |
| fr | `" "` | `"{{.Day}} {{.Month}} {{.Year}}"` | janvier, février, mars, avril, mai, juin, juillet, août, septembre, octobre, novembre, décembre |
| it | `"."` | `"{{.Day}} {{.Month}} {{.Year}}"` | gennaio, febbraio, marzo, aprile, maggio, giugno, luglio, agosto, settembre, ottobre, novembre, dicembre |
| pt | `" "` | `"{{.Day}} de {{.Month}} de {{.Year}}"` | janeiro, fevereiro, março, abril, maio, junho, julho, agosto, setembro, outubro, novembro, dezembro |
| pt-br | `"."` | `"{{.Day}} de {{.Month}} de {{.Year}}"` | same as pt |

Duration words (`duration.minute/hour/day`, `one` and `other` with `{{.N}}`): the natural "1 minute" /
"N minutes" forms of each language (el: 1 λεπτό / {{.N}} λεπτά; eu: minutu 1 / {{.N}} minutu, and the same
pattern for ordu/egun). The `one` form may put the number after the unit where the language does (eu).

Step 2: check each file as it lands

Run: `go test -count=1 -run 'TestRenderAllFixtures|TestOTPSMSLength|TestEscaping' ./notifications/mailtemplates/`
Expected: `ok`. A `Load` error names the file, key and problem (for example
`i18n/fr.yaml: invite_admin.intro uses {{.Org}}, which the English string does not`).
Then read your file's rendered emails: `go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview` (after
Task 7) or render one in a test; check that no sentence is broken around a field.

Step 3: once all seven files are in, require completeness for every language

In `notifications/mailtemplates/mailtemplates_test.go` replace
`var completeLocales = []string{"ca", "en", "es"}` with `var completeLocales = apicommon.SupportedLangs` and
update the comment above it to "every locale file must hold every key".

Run: `go test -count=1 ./notifications/mailtemplates/`
Expected: `ok`; `TestLocalesComplete` passes for all ten languages (any missing or extra key is listed).
Run: `~/.cache/saas-backend/bin/golangci-lint run ./notifications/...`
Expected: `0 issues.`

Step 4: commit (one commit per language is fine, e.g. `feat(mail): add the german strings`; the last one
includes the test file)
`git commit -m "feat(mail): add the de, el, eu, fr, it, pt and pt-br strings" -- assets/mail/i18n/de.yaml assets/mail/i18n/el.yaml assets/mail/i18n/eu.yaml assets/mail/i18n/fr.yaml assets/mail/i18n/it.yaml assets/mail/i18n/pt.yaml assets/mail/i18n/pt-br.yaml notifications/mailtemplates/mailtemplates_test.go`

Interfaces this task provides to later tasks: none (data only). The release note must say these seven are
machine-assisted until a native review.

### Task 7: `cmd/mailpreview`, README, AGENTS.md, example.env
Model: haiku   Repo: /Users/ferran/Repos/saas-backend   Files: `cmd/mailpreview/main.go` (new), `README.md`,
`AGENTS.md`, `example.env`
Goal: a preview command that renders every email × language × fixture without sending anything (Goal 7,
AC2), and the new flags documented.

Step 1: write the command

Create `cmd/mailpreview/main.go`:

```go
// Command mailpreview renders every email in every language and fixture state to a directory, so
// they can be opened in a browser without sending anything:
//
//	go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview [-accent '#166534']
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

func main() {
	out := flag.String("out", "", "output directory (required)")
	accent := flag.String("accent", mailtemplates.DefaultAccent, "accent colour, #rrggbb")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, *accent); err != nil {
		log.Fatal(err)
	}
}

func run(out, accent string) error {
	// the logos are copied next to the pages, so the preview works offline
	brand, err := mailtemplates.NewBrand(accent, mailtemplates.DefaultBrand().ServerURL, "images")
	if err != nil {
		return err
	}
	if err := mailtemplates.Load(brand); err != nil {
		return err
	}
	if err := copyImages(filepath.Join(out, "images")); err != nil {
		return err
	}
	if err := writeOrgLogo(filepath.Join(out, "org-logo.png"), brand.Accent); err != nil {
		return err
	}
	var index strings.Builder
	index.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Mail preview</title>
<style>body{font-family:system-ui,sans-serif;margin:32px}td{padding:4px 12px 4px 0;font-size:14px}</style>
</head><body><h1>Mail preview</h1><table><tr><th>Email</th><th>Language</th><th>Subject</th><th>HTML</th></tr>`)
	over := 0
	for _, fx := range mailtemplates.Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			n, err := fx.Template.Localized(lang).ExecTemplate(fx.Data)
			if err != nil {
				return fmt.Errorf("%s in %s: %w", fx.Name, lang, err)
			}
			name := fx.Name + "_" + lang
			// show the sample organization logo, which lives in a server the preview does not run
			body := strings.ReplaceAll(n.Body, mailtemplates.FixtureOrgLogo, "org-logo.png")
			if err := os.WriteFile(filepath.Join(out, name+".html"), []byte(body), 0o600); err != nil {
				return err
			}
			text := "Subject: " + n.Subject + "\n\n" + n.PlainBody + "\n"
			if err := os.WriteFile(filepath.Join(out, name+".txt"), []byte(text), 0o600); err != nil {
				return err
			}
			size := len(n.Body)
			if size > mailtemplates.MaxHTMLBytes || utf8.RuneCountInString(n.Subject) > mailtemplates.MaxSubjectRunes {
				over++
				log.Printf("over budget: %s (%d bytes, subject %d characters)", name, size, utf8.RuneCountInString(n.Subject))
			}
			fmt.Fprintf(&index, `<tr><td>%s</td><td>%s</td><td>%s</td>`, html.EscapeString(fx.Name), lang,
				html.EscapeString(n.Subject))
			fmt.Fprintf(&index, `<td><a href="%s.html">%.1f KB</a> · <a href="%s.txt">text</a></td></tr>`,
				name, float64(size)/1024, name)
		}
	}
	index.WriteString("</table></body></html>\n")
	if err := os.WriteFile(filepath.Join(out, "index.html"), []byte(index.String()), 0o600); err != nil {
		return err
	}
	log.Printf("wrote %d emails to %s", len(mailtemplates.Fixtures())*len(apicommon.SupportedLangs), out)
	if over > 0 {
		return fmt.Errorf("%d emails over the size or subject budget", over)
	}
	return nil
}

// copyImages writes the embedded logos to dir.
func copyImages(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	entries, err := fs.ReadDir(root.Assets, "assets/mail/images")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := root.Assets.ReadFile("assets/mail/images/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeOrgLogo writes a plain square in the accent colour as the sample organization logo.
func writeOrgLogo(path, accent string) error {
	var r, g, b uint8
	if _, err := fmt.Sscanf(accent, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for x := range 96 {
		for y := range 96 {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
```

Run: `go vet ./cmd/mailpreview/ && go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview`
Expected: last line `wrote 180 emails to /Users/…/.cache/saas-backend/preview`, exit status 0; the
directory holds `index.html`, 180 `.html`, 180 `.txt`, `images/` (12 PNGs) and `org-logo.png`.
Run: `go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview-green -accent '#166534'`
Expected: same count; the button fill is #166534 in the HTML.
Run: `go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview -accent red`
Expected: exit status 1, `invalid mail accent colour "red", want #rrggbb`.

Step 2: documentation

Change `README.md` (exact diff against the state after the previous task):

````diff
--- a/README.md
+++ b/README.md
@@ -122,6 +122,24 @@ VOCDONI_SMTPPORT=1025
 
 The fake SMTP server will log all received email messages to stdout.
 
+### Email templates and preview
+
+Every email is one shared layout (`assets/mail/layout.html`, `components.html`), a content block per
+email (`assets/mail/<name>.html`, plus `<name>.txt` for the subject, preheader and plain text) and one
+string file per language (`assets/mail/i18n/<lang>.yaml`, English is the source). To look at every
+email in every language and edge case without sending anything:
+
+```bash
+go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview            # then open index.html
+go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview -accent '#166534'
+```
+
+Two optional settings brand the emails: `VOCDONI_MAILACCENTCOLOR` (`#rrggbb`, default `#1d62e9`; the
+dark-mode link colour and the button label colour are derived from it) and `VOCDONI_MAILASSETSURL`
+(base URL of the logo images, default `{serverURL}/mail/assets`, served by this API at
+`GET /mail/assets/{file}`). The logos are generated from vocdoni-app's `Logo.tsx` by
+`scripts/mail-logos/make-logos.py`.
+
 ### Running with a local Vocone (standalone Vocdoni chain)
 
 If you want to run a fully local Vocdoni chain instead of relying on the remote API,
````

Change `AGENTS.md` (exact diff against the state after the previous task):

```diff
--- a/AGENTS.md
+++ b/AGENTS.md
@@ -119,7 +119,9 @@ Component packages (each is a focused service composed in `main.go`):
 - **`account/`** — wraps the Vocdoni blockchain account: signs transactions, funds accounts via
   faucet, computes election prices. This is the "remote signer" core.
 - **`notifications/`** — `NotificationService` interface with `smtp/` (email) and `twilio/` (SMS)
-  implementations, plus `mailtemplates/` (HTML/text email templates, loaded with `mailtemplates.Load()`).
+  implementations, plus `mailtemplates/` (one layout and components in `assets/mail/`, a content block per
+  email, strings per language in `assets/mail/i18n/<lang>.yaml`; `mailtemplates.Load(brand)` runs at start-up
+  and fails on any missing string or template; `go run ./cmd/mailpreview -out <dir>` renders them all).
   `FailoverService` composes multiple services and delivers via the first that succeeds; the service
   wires it from the optional `VOCDONI_BACKUPSMTP*` vars to fail over from the primary SMTP relay to a
   backup that shares the same sender identity. The CSP drains email/SMS through
@@ -149,7 +151,7 @@ Component packages (each is a focused service composed in `main.go`):
   return these. Predefined errors in `errors_definition.go`.
 - **`internal/`** — shared primitives: `HexBytes`, birthdate parsing, phone/argon2 helpers.
 - **`cmd/`** — `service/` (the API server), `userdel/` (GDPR user erasure), `topup/` (manual wallet
-  top-up as a Stripe invoice, credited by the `invoice.paid` webhook), `localsmtp/` (dev SMTP capture).
+  top-up as a Stripe invoice, credited by the `invoice.paid` webhook), `localsmtp/` (dev SMTP capture), `mailpreview/` (renders every email to HTML files).
 - **`assets/`** are embedded via `embed.go` (`//go:embed all:assets`).
 
 ## Conventions
```

Change `example.env` (exact diff against the state after the previous task):

```diff
--- a/example.env
+++ b/example.env
@@ -26,6 +26,11 @@ VOCDONI_SMTPPASSWORD=password
 VOCDONI_EMAILFROMADDRESS=admin@email.com
 VOCDONI_EMAILFROMNAME=Vocdoni
 
+# Optional email branding: accent colour (#rrggbb) and base URL of the logo images
+# (default {serverURL}/mail/assets, served by this API)
+# VOCDONI_MAILACCENTCOLOR=#1d62e9
+# VOCDONI_MAILASSETSURL=https://api.example.org/mail/assets
+
 # Optional backup SMTP relay for email failover.
 # Leave VOCDONI_BACKUPSMTPSERVER empty to disable. When set (together with the
 # backup username and password), email is sent through the primary relay and
```

Run: `~/.cache/saas-backend/bin/golangci-lint run ./cmd/...`
Expected: `0 issues.`

Step 3: commit
`git add cmd/mailpreview/main.go && git commit -m "feat(mail): add a preview command for every email and language" -- cmd/mailpreview/main.go README.md AGENTS.md example.env`

Interfaces this task provides to later tasks: `go run ./cmd/mailpreview -out <dir> [-accent #rrggbb]` (used by
AC2 and the owner's review).

## Merge and release

1. Full suites on the branch (Docker running):
   - `go build ./... && go vet ./...` → no output.
   - `go test -count=1 -failfast -timeout=45m ./...` → every package `ok` (about 10 minutes; `api` is the slow
     one).
   - `golangci-lint run` (v2.15.0) → no issue in files this branch touches. On base 896c3b0 the whole-repo run
     already reports 5 `unhandled-error` issues in `db/csp_counters.go`; CI runs with `only-new-issues`, so they
     are not this branch's and must not grow.
   - `./scripts/check-qt-patterns.sh` → `No qt anti-patterns found!`.
   - `go mod tidy && git diff --exit-code go.mod go.sum` → no diff.
   - `PATH=$(go env GOPATH)/bin:$PATH make swagger && git diff --exit-code docs/swagger.yaml` → no diff.
   - AC3a: `git grep -l 'mypinata\|fonts.googleapis' -- ':!docs'` → no output.
   - AC2 (controller): `go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview`; screenshot ca and en,
     light and dark, at 375 and 600 px, compare with `~/.cache/saas-backend/mockups/shots/`. Known, accepted
     differences: date only (no time) in the import table, "Review the member base" button to
     `/admin/memberbase/`, halo wordmark 28 px high, monogram tile in the OTP without a logo,
     import preheader and intro omit the added-count clause (the plural scheme carries one count per string),
     'a la Vocdoni App' instead of the mockups' 'a Vocdoni' (spec copy rule).
2. Fable gate audit of the branch, rulings folded in, fixes re-reviewed.
3. Merge order: one repo, one branch: `817-email-redesign` → `main` by pull request. Title
   `feat(mail): redesign the transactional emails`; the description ends with "Closes #817" and records:
   - Docs/SDK impact (AGENTS.md rule): `GET /organizations/languages` and the accepted `defaultLang` /
     `?lang=` values grow from en/es/ca to the ten app locales, case-insensitive on input and stored lowercase;
     new public route `GET /mail/assets/{file}`; new optional settings `VOCDONI_MAILACCENTCOLOR` and
     `VOCDONI_MAILASSETSURL`. Suggest a vocdoni.io `stage` docs note on the language list and an
     integrator-SDK check of any language enum in `@vocdoni/api-types`; whether to open them is the owner's
     call, so write "pending owner decision" until then.
   - Follow-up outside this repo: confirm the new voter page in vocdoni-app passes its language to the CSP
     auth calls.
   - Release note: de, el, eu, fr, it, pt and pt-br strings are machine-assisted until a native review.
   - AC7 result (below), or which clients were not checked.
4. **Owner OK required before:** pushing the branch; opening the pull request; merging it.
5. Before release (owner, AC7): one real send of each email through the dev SMTP to test inboxes, checked in
   Gmail web and app, Apple Mail and Outlook.com in light and dark; Outlook desktop on Windows for the two code
   emails and invite_admin, and Yahoo once for verification_account, if the owner has access (otherwise the PR
   says they were not checked). Each client matches the mockups or degrades to the documented fallback (no
   radius/max-width and no organization logo in Outlook desktop, no dark swap in Gmail IMAP).
6. Release: no version bump in this repo; it ships with the next `main → stage → lts` promotion. Rollback:
   revert the merge commit (no data or schema change; queued notifications keep their rendered bodies).
7. Post-release checks: `curl -sI {serverURL}/mail/assets/vocdoni-wordmark-black-halo@2x.png` → `200`,
   `Content-Type: image/png`, `Cache-Control: public, max-age=86400`, an `ETag`; the start-up log shows
   `email templates loaded` with `templates 8 languages 10`; one OTP SMS on staging arrives in one segment.
