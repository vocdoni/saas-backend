package smtp

import (
	"bytes"
	"fmt"
	"mime"
	"net/mail"
	"net/textproto"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
)

func TestClassifySendError(t *testing.T) {
	c := qt.New(t)

	// 5xx replies are permanent, bare or wrapped
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 550, Msg: "no such user"})),
		qt.IsTrue)
	c.Assert(notifications.IsPermanentSendError(classifySendError(
		fmt.Errorf("could not send the message: %w", &textproto.Error{Code: 550, Msg: "no such user"}))), qt.IsTrue)
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 500})), qt.IsTrue)
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 599})), qt.IsTrue)

	// 4xx deferrals and codes outside 5xx stay transient
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 451, Msg: "try later"})),
		qt.IsFalse)
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 499})), qt.IsFalse)
	c.Assert(notifications.IsPermanentSendError(classifySendError(&textproto.Error{Code: 600})), qt.IsFalse)

	// nil and non-SMTP errors are returned unchanged
	c.Assert(classifySendError(nil), qt.IsNil)
	netErr := fmt.Errorf("dial tcp: connection refused")
	c.Assert(classifySendError(netErr), qt.Equals, netErr)
}

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
