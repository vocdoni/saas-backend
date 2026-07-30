package smtp

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
)

// newTestEmail builds an Email with a minimal valid config for composeBody,
// which only reads the sender identity.
func newTestEmail() *Email {
	return &Email{config: &Config{
		FromName:    "Vocdoni",
		FromAddress: "no-reply@example.com",
	}}
}

// TestComposeBodyEncodesSubject verifies that a non-ASCII subject is RFC 2047
// encoded rather than emitted as raw 8-bit bytes in the header (H5/M17).
func TestComposeBodyEncodesSubject(t *testing.T) {
	c := qt.New(t)

	se := newTestEmail()
	out, err := se.composeBody(&notifications.Notification{
		ToAddress: "voter@example.com",
		Subject:   "Votació oberta ✓",
		PlainBody: "hola",
		Body:      "<p>hola</p>",
	})
	c.Assert(err, qt.IsNil)
	msg := string(out)

	// the subject must be encoded-word wrapped, not left as raw UTF-8 bytes
	c.Assert(strings.Contains(msg, "Subject: =?UTF-8?q?"), qt.IsTrue,
		qt.Commentf("subject must be RFC 2047 encoded; got:\n%s", msg))
	c.Assert(strings.Contains(msg, "Votació oberta"), qt.IsFalse,
		qt.Commentf("raw non-ASCII subject must not appear unencoded"))
}

// TestComposeBodySubjectHeaderInjection verifies that CR/LF smuggled into the
// subject cannot inject additional headers: the control bytes are encoded, so
// no attacker-controlled header line appears in the output (H5).
func TestComposeBodySubjectHeaderInjection(t *testing.T) {
	c := qt.New(t)

	se := newTestEmail()
	out, err := se.composeBody(&notifications.Notification{
		ToAddress: "voter@example.com",
		Subject:   "Hi\r\nBcc: attacker@evil.example.com",
		PlainBody: "hola",
		Body:      "<p>hola</p>",
	})
	c.Assert(err, qt.IsNil)
	msg := string(out)

	// the injected Bcc header must not survive as a real header line
	c.Assert(strings.Contains(msg, "\r\nBcc: attacker@evil.example.com"), qt.IsFalse,
		qt.Commentf("CRLF in subject must be encoded, not emitted as a header; got:\n%s", msg))
	// the subject occupies exactly one header line
	headerBlock, _, _ := strings.Cut(msg, "\r\n\r\n")
	c.Assert(strings.Count(headerBlock, "Subject:"), qt.Equals, 1)
}

// TestComposeBody8bit verifies that the message parts declare 8bit (not the
// previous 7bit, which misdeclared 8-bit UTF-8 bodies) and that the body bytes
// are written literally — 8bit must not rewrite '=' in URLs the way
// quoted-printable would, which is what keeps verification links intact (M17).
func TestComposeBody8bit(t *testing.T) {
	c := qt.New(t)

	se := newTestEmail()
	out, err := se.composeBody(&notifications.Notification{
		ToAddress: "voter@example.com",
		Subject:   "hi",
		PlainBody: "café https://x.example.com/verify?code=ABC123",
		Body:      "<p>café</p>",
	})
	c.Assert(err, qt.IsNil)
	msg := string(out)

	c.Assert(strings.Contains(msg, "Content-Transfer-Encoding: 8bit"), qt.IsTrue,
		qt.Commentf("parts must declare 8bit; got:\n%s", msg))
	c.Assert(strings.Contains(msg, "7bit"), qt.IsFalse,
		qt.Commentf("parts must not declare 7bit while writing 8-bit content"))
	// the UTF-8 body is written literally under 8bit
	c.Assert(strings.Contains(msg, "café"), qt.IsTrue,
		qt.Commentf("8bit body must be written literally; got:\n%s", msg))
	// crucially, '=' in the verification URL must survive verbatim (quoted-printable
	// would have turned it into '=3D', breaking the link and code extraction)
	c.Assert(strings.Contains(msg, "code=ABC123"), qt.IsTrue,
		qt.Commentf("'=' in URLs must not be re-encoded; got:\n%s", msg))
	c.Assert(strings.Contains(msg, "code=3DABC123"), qt.IsFalse,
		qt.Commentf("body must not be quoted-printable encoded"))
}
