package smtp

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

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

func TestComposeBodyQuotedPrintable(t *testing.T) {
	c := qt.New(t)
	se := &Email{config: &Config{FromAddress: "noreply@vocdoni.app", FromName: "Vocdoni"}}

	// accented text, a line far beyond 76 characters and an "=" that QP must escape
	plain := "Codi de verificació: 4e92af\nEnllaç: https://app.vocdoni.io/account/verify?code=4e92af&email=" +
		strings.Repeat("mariona.soler.", 10) + "%40exemple.cat\nGràcies."
	html := `<p style="color:#171717;">Confirma l'adreça d'<strong>Àngels</strong></p>` + strings.Repeat("<td>ç</td>", 40)
	body, err := se.composeBody(&notifications.Notification{
		ToAddress: "mariona@exemple.cat",
		Subject:   "Verifica el teu compte",
		PlainBody: plain,
		Body:      html,
	})
	c.Assert(err, qt.IsNil)

	msg, err := mail.ReadMessage(bytes.NewReader(body))
	c.Assert(err, qt.IsNil)
	date, err := msg.Header.Date()
	c.Assert(err, qt.IsNil)
	c.Assert(time.Since(date) < time.Minute, qt.IsTrue)
	c.Assert(msg.Header.Get("Message-ID"), qt.Matches, `<[0-9a-f]{32}@vocdoni\.app>`)

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	c.Assert(err, qt.IsNil)
	c.Assert(mediaType, qt.Equals, "multipart/alternative")
	reader := multipart.NewReader(msg.Body, params["boundary"])
	want := []struct{ mediaType, content string }{{"text/plain", plain}, {"text/html", html}}
	for _, w := range want {
		part, err := reader.NextRawPart()
		c.Assert(err, qt.IsNil)
		c.Assert(part.Header.Get("Content-Transfer-Encoding"), qt.Equals, "quoted-printable")
		c.Assert(part.Header.Get("Content-Type"), qt.Equals, w.mediaType+`; charset="UTF-8"`)
		raw, err := io.ReadAll(part)
		c.Assert(err, qt.IsNil)
		for line := range strings.SplitSeq(string(raw), "\r\n") {
			c.Assert(len(line) <= 76, qt.IsTrue, qt.Commentf("encoded line too long: %q", line))
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		c.Assert(err, qt.IsNil)
		c.Assert(strings.ReplaceAll(string(decoded), "\r\n", "\n"), qt.Equals, w.content)
	}
	_, err = reader.NextRawPart()
	c.Assert(err, qt.Equals, io.EOF)
}
