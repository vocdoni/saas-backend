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

// NotificationLang resolves the language for a notification sent on behalf of
// an organization: the explicit ?lang= request param (stored in the context by
// the setLang middleware) wins, then the organization's default language, then
// DefaultLang.
func NotificationLang(ctx context.Context, org *db.Organization) string {
	if lang, ok := ctx.Value(LangMetadataKey).(string); ok && lang != "" {
		return lang
	}
	if org != nil && org.DefaultLang != "" {
		return org.DefaultLang
	}
	return DefaultLang
}
