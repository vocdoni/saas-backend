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
// an organization: the organization's default language wins when set, then
// the ?lang= request param (stored in the context by the setLang middleware),
// then DefaultLang.
//
// Organizations get a default language at creation, so in practice the param
// only applies to user-scoped mails — pass a nil org for those — and to
// organizations created before the field existed.
func NotificationLang(ctx context.Context, org *db.Organization) string {
	if org != nil && org.DefaultLang != "" {
		return org.DefaultLang
	}
	if lang, ok := ctx.Value(LangMetadataKey).(string); ok && lang != "" {
		return lang
	}
	return DefaultLang
}
