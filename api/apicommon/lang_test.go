package apicommon

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

func TestNotificationLang(t *testing.T) {
	c := qt.New(t)

	ctxWith := func(lang string) context.Context {
		return context.WithValue(context.Background(), LangMetadataKey, lang)
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		org  *db.Organization
		want string
	}{{
		name: "org default wins over the request param",
		ctx:  ctxWith("es"),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "ca",
	}, {
		name: "org default applies without a request param",
		ctx:  context.Background(),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "ca",
	}, {
		name: "request param applies to an org with no default",
		ctx:  ctxWith("es"),
		org:  &db.Organization{},
		want: "es",
	}, {
		name: "request param applies to user-scoped notifications",
		ctx:  ctxWith("es"),
		org:  nil,
		want: "es",
	}, {
		name: "falls back to the default language",
		ctx:  context.Background(),
		org:  nil,
		want: DefaultLang,
	}, {
		name: "an empty param is not a language",
		ctx:  ctxWith(""),
		org:  &db.Organization{},
		want: DefaultLang,
	}} {
		c.Run(tc.name, func(c *qt.C) {
			c.Assert(NotificationLang(tc.ctx, tc.org), qt.Equals, tc.want)
		})
	}
}

func TestIsValidLang(t *testing.T) {
	c := qt.New(t)
	for _, lang := range SupportedLangs {
		c.Assert(IsValidLang(lang), qt.IsTrue)
	}
	c.Assert(IsValidLang(""), qt.IsFalse)
	c.Assert(IsValidLang("xx"), qt.IsFalse)
	// exact match only: the API stores what it validates
	c.Assert(IsValidLang("EN"), qt.IsFalse)
}
