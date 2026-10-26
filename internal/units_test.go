package internal

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestByteUnits(t *testing.T) {
	c := qt.New(t)
	c.Assert(KiB, qt.Equals, 1024)
	c.Assert(MiB, qt.Equals, 1024*1024)
	c.Assert(GiB, qt.Equals, 1024*1024*1024)
}
