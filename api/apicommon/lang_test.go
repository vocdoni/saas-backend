package apicommon

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

func TestNotificationLang(t *testing.T) {
	c := qt.New(t)

	// public: setLang ran, the authenticator did not.
	public := func(lang string) context.Context {
		return context.WithValue(context.Background(), LangMetadataKey, lang)
	}
	// protected: the authenticator stored the caller before the handler ran.
	protected := func(lang string) context.Context {
		return context.WithValue(public(lang), UserMetadataKey, db.User{Email: "admin@example.com"})
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		org  *db.Organization
		want string
	}{{
		name: "public: the request param wins over the org default",
		ctx:  public("es"),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "es",
	}, {
		name: "public: the org default applies without a request param",
		ctx:  public(""),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "ca",
	}, {
		name: "public: the request param applies to user-scoped notifications",
		ctx:  public("es"),
		org:  nil,
		want: "es",
	}, {
		name: "protected: the org default wins, the caller's param is ignored",
		ctx:  protected("es"),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "ca",
	}, {
		name: "protected: the org default applies without a request param",
		ctx:  protected(""),
		org:  &db.Organization{DefaultLang: "ca"},
		want: "ca",
	}, {
		name: "protected: a nil org falls back, it never borrows the caller's param",
		ctx:  protected("es"),
		org:  nil,
		want: DefaultLang,
	}, {
		name: "protected: an org with no default falls back too",
		ctx:  protected("es"),
		org:  &db.Organization{},
		want: DefaultLang,
	}, {
		name: "falls back to the default language",
		ctx:  context.Background(),
		org:  nil,
		want: DefaultLang,
	}, {
		name: "an empty param is not a language",
		ctx:  public(""),
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
