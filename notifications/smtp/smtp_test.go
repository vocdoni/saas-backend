package smtp

import (
	"bytes"
	"mime"
	"net/mail"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
)

func TestComposeBodySubjectCannotInjectHeaders(t *testing.T) {
	c := qt.New(t)
	se := &Email{config: &Config{FromAddress: "noreply@example.com", FromName: "Vocdoni"}}

	// a support ticket title (or an organization name) ends up in the subject
	body, err := se.composeBody(&notifications.Notification{
		ToAddress: "support@example.com",
		Subject:   "New ticket: hi\r\nBcc: victim@example.com\r\nX-Evil: 1",
		PlainBody: "body",
		Body:      "<p>body</p>",
	})
	c.Assert(err, qt.IsNil)
	msg, err := mail.ReadMessage(bytes.NewReader(body))
	c.Assert(err, qt.IsNil)
	c.Assert(msg.Header.Get("Bcc"), qt.Equals, "")
	c.Assert(msg.Header.Get("X-Evil"), qt.Equals, "")

	// plain ASCII subjects are left as they are, non-ASCII ones are MIME-encoded and decode back
	c.Assert(encodeHeader("Verification Code - Org"), qt.Equals, "Verification Code - Org")
	decoded, err := new(mime.WordDecoder).DecodeHeader(encodeHeader("Codi de Verificació - Org"))
	c.Assert(err, qt.IsNil)
	c.Assert(decoded, qt.Equals, "Codi de Verificació - Org")
}
