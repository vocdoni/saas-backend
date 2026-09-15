package apicommon

import (
	"context"
	"slices"

	"github.com/vocdoni/saas-backend/db"
)

// SupportedLangs lists the languages with notification template variants in
// assets/ (see notifications/mailtemplates).
var SupportedLangs = []string{"en", "es", "ca"}

// IsValidLang reports whether lang is one of the supported notification
// languages.
func IsValidLang(lang string) bool {
	return slices.Contains(SupportedLangs, lang)
}

// NotificationLang resolves the language of a notification. The ?lang= request
// param (stored in the context by the setLang middleware) is the language of
// whoever made the request, so it only speaks for mail that person is the
// audience of, which is what the route group tells us:
//
//	public endpoints:    ?lang= -> org.DefaultLang -> DefaultLang
//	protected endpoints: org.DefaultLang -> DefaultLang
//
// On a protected endpoint the caller is authenticated and acting on behalf of
// the organization — inviting an admin, importing members — so the recipients
// are the organization's people and its language decides, never the caller's
// dashboard locale. On a public one the requester is the recipient: a voter
// authenticating against the CSP, or a user registering or resetting a
// password (pass a nil org for those).
func NotificationLang(ctx context.Context, org *db.Organization) string {
	// the authenticator middleware runs only on the protected group, so the
	// user value is what distinguishes the two.
	if _, authenticated := UserFromContext(ctx); !authenticated {
		if lang, ok := ctx.Value(LangMetadataKey).(string); ok && lang != "" {
			return lang
		}
	}
	if org != nil && org.DefaultLang != "" {
		return org.DefaultLang
	}
	return DefaultLang
}
