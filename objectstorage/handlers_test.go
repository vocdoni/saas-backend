package objectstorage

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestObjectIDFromName(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		name   string
		in     string
		wantID string
		wantOK bool
	}{
		{"png", "abc123.png", "abc123", true},
		{"jpg", "Img9.jpg", "Img9", true},
		{"jpeg", "Img9.jpeg", "Img9", true},
		{"json", "deadbeef.json", "deadbeef", true},
		{"double extension rejected", "abc.json.bak", "", false},
		{"path traversal rejected", "abc.jpg/../../etc/passwd", "", false},
		{"no extension rejected", "abc123", "", false},
		{"unknown extension rejected", "abc.gif", "", false},
		{"non-alphanumeric id rejected", "ab_c.png", "", false},
		{"empty rejected", "", "", false},
	}
	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			id, ok := objectIDfromName(tc.in)
			c.Assert(ok, qt.Equals, tc.wantOK)
			c.Assert(id, qt.Equals, tc.wantID)
		})
	}
}
