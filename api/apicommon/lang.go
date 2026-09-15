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

// NotificationLang resolves the language of a notification:
//
//	public endpoints:    ?lang= -> org.DefaultLang -> DefaultLang
//	protected endpoints: org.DefaultLang -> DefaultLang
//
// The ?lang= param is the caller's own language, so it only applies where the
// caller is the recipient. On a protected endpoint they act for the
// organization, and the organization's language wins. Pass a nil org for mail
// sent to the user themselves, such as registration or password reset.
func NotificationLang(ctx context.Context, org *db.Organization) string {
	// authenticated means protected: the authenticator middleware, the only
	// writer of the user value, runs on that group alone.
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
