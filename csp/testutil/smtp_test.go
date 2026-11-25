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
