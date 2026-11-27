package errorcatalog_test

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal/errorcatalog"
)

func TestParseDirValid(t *testing.T) {
	c := qt.New(t)
	entries, err := errorcatalog.ParseDir("testdata/valid")
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 1)
	c.Assert(entries[0].GoName, qt.Equals, "ErrFoo")
	c.Assert(entries[0].Code, qt.Equals, 40001)
	c.Assert(entries[0].HTTPStatus, qt.Equals, 400)
	c.Assert(entries[0].Message, qt.Equals, "some error")
}

func TestParseDirUnknownStatus(t *testing.T) {
	c := qt.New(t)
	_, err := errorcatalog.ParseDir("testdata/unknown-status")
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(strings.Contains(err.Error(), "unknown"), qt.IsTrue,
		qt.Commentf("expected error to mention 'unknown', got: %v", err))
	c.Assert(strings.Contains(err.Error(), "ErrBad"), qt.IsTrue,
		qt.Commentf("expected error to name the variable, got: %v", err))
}

func TestParseDirNonLiteralMessage(t *testing.T) {
	c := qt.New(t)
	_, err := errorcatalog.ParseDir("testdata/nonlit-msg")
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(strings.Contains(err.Error(), "ErrBad"), qt.IsTrue,
		qt.Commentf("expected error to name the variable, got: %v", err))
}
