package smtp

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestConfigTimeout(t *testing.T) {
	c := qt.New(t)

	const wantTimeout = 5 * time.Second
	se := new(Email)
	err := se.New(&Config{
		FromName:    "Test",
		FromAddress: "test@example.com",
		SMTPServer:  "localhost",
		SMTPPort:    1025,
		SMTPTimeout: wantTimeout,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(se.config.SMTPTimeout, qt.Equals, wantTimeout)
}
