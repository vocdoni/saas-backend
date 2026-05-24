package api

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestCalculatePagination(t *testing.T) {
	t.Run("middle page has correct Next and Previous URLs", func(t *testing.T) {
		c := qt.New(t)
		// page 2 of 3 pages (30 total items, limit 10)
		p, err := calculatePagination(2, 10, 30, "https://api.example.com/v1/orgs")
		c.Assert(err, qt.IsNil)
		c.Assert(p.Next, qt.Equals, "https://api.example.com/v1/orgs?limit=10&page=3")
		c.Assert(p.Previous, qt.Equals, "https://api.example.com/v1/orgs?limit=10&page=1")
	})

	t.Run("first page has empty Previous and non-empty Next URL", func(t *testing.T) {
		c := qt.New(t)
		// page 1 of 3 pages (30 total items, limit 10)
		p, err := calculatePagination(1, 10, 30, "https://api.example.com/v1/orgs")
		c.Assert(err, qt.IsNil)
		c.Assert(p.Previous, qt.Equals, "")
		c.Assert(p.Next, qt.Equals, "https://api.example.com/v1/orgs?limit=10&page=2")
	})

	t.Run("last page has empty Next and non-empty Previous URL", func(t *testing.T) {
		c := qt.New(t)
		// page 3 of 3 pages (30 total items, limit 10)
		p, err := calculatePagination(3, 10, 30, "https://api.example.com/v1/orgs")
		c.Assert(err, qt.IsNil)
		c.Assert(p.Next, qt.Equals, "")
		c.Assert(p.Previous, qt.Equals, "https://api.example.com/v1/orgs?limit=10&page=2")
	})

	t.Run("empty baseURL keeps Next and Previous empty", func(t *testing.T) {
		c := qt.New(t)
		// page 2 of 3 pages (30 total items, limit 10) — no baseURL
		p, err := calculatePagination(2, 10, 30, "")
		c.Assert(err, qt.IsNil)
		c.Assert(p.Next, qt.Equals, "")
		c.Assert(p.Previous, qt.Equals, "")
	})

	t.Run("malformed baseURL returns empty Next and Previous", func(t *testing.T) {
		c := qt.New(t)
		// A URL with a control character is unparseable; buildPageURL must return "".
		p, err := calculatePagination(2, 10, 30, "://\x00bad")
		c.Assert(err, qt.IsNil)
		c.Assert(p.Next, qt.Equals, "")
		c.Assert(p.Previous, qt.Equals, "")
	})
}
