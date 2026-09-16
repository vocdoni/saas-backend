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
// ?lang= is the caller's own language, so it only applies where the caller is
// the recipient. Pass a nil org for mail sent to the user themselves.
func NotificationLang(ctx context.Context, org *db.Organization) string {
	// authenticated means protected: only that group runs the authenticator.
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
