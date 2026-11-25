package apicommon

import (
	"context"
	"slices"
	"strings"

	"github.com/vocdoni/saas-backend/db"
)

// SupportedLangs lists the notification languages: exactly the lowercase locale codes of the
// web app (vocdoni-app src/i18n/locales), each with a string file in assets/mail/i18n.
var SupportedLangs = []string{"ca", "de", "el", "en", "es", "eu", "fr", "it", "pt", "pt-br"}

// NormalizeLang returns the canonical form of a language code: trimmed and lowercased. Codes are
// normalized once where they enter the service (the ?lang= parameter, an organization's
// defaultLang); past that point IsValidLang and the template lookup match exactly.
func NormalizeLang(lang string) string {
	return strings.ToLower(strings.TrimSpace(lang))
}

// IsValidLang reports whether lang is one of the supported notification
// languages. It matches exactly, so callers normalize with NormalizeLang first.
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
		// rows stored before defaultLang was normalized on write
		return NormalizeLang(org.DefaultLang)
	}
	return DefaultLang
}
