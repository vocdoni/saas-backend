package objectstorage

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestLocalName(t *testing.T) {
	c := qt.New(t)

	cases := []struct {
		name      string
		serverURL string
		url       string
		wantName  string
		wantOK    bool
	}{
		{"canonical", "https://host", "https://host/storage/abc.json", "abc.json", true},
		{"trailing slash on serverURL", "https://host/", "https://host/storage/abc.json", "abc.json", true},
		{"legacy double slash pointer", "https://host", "https://host//storage/abc.json", "abc.json", true},
		{"relative reference", "https://host", "/storage/abc.json", "abc.json", true},
		{"relative with empty serverURL", "", "/storage/abc.json", "abc.json", true},
		{"different host", "https://host", "https://other/storage/abc.json", "", false},
		{"host-prefix false positive", "https://host", "https://hoststorage/abc.json", "", false},
		{"external absolute, empty serverURL", "", "https://other/storage/abc.json", "", false},
		{"ipfs reference", "https://host", "ipfs://bafy.../meta", "", false},
	}
	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			osc := &Client{ServerURL: tc.serverURL}
			name, ok := osc.LocalName(tc.url)
			c.Assert(ok, qt.Equals, tc.wantOK)
			c.Assert(name, qt.Equals, tc.wantName)
		})
	}
}
