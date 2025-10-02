// Package mailtemplates provides predefined email templates for various notification types
// such as account verification, password reset, and organization invitations,
// along with utilities for rendering email content.
package mailtemplates

import (
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

// LocalizedMailTemplate represents a mail template with multiple language variants
type LocalizedMailTemplate struct {
	Templates map[string]MailTemplate // key is language code (en, es, ca)
	WebAppURI string
}

// GetTemplate returns the appropriate template for the given language.
// Falls back to English if the language is not found.
func (lmt LocalizedMailTemplate) GetTemplate(lang string) MailTemplate {
	if template, exists := lmt.Templates[lang]; exists {
		log.Warnf("GetTemplate(%s) 1", lang)
		return template
	}
	// Fallback to English if language not found
	if template, exists := lmt.Templates["es"]; exists {
		log.Warnf("GetTemplate(%s) 2", lang)

		return template
	}
	// If English is not found either, return the first available template
	for _, template := range lmt.Templates {
		log.Warnf("GetTemplate(%s) 3", lang)

		return template
	}
	// This should never happen if templates are properly defined
	return MailTemplate{}
}

// ExecTemplate executes the template for the given language with the provided data.
// Falls back to English if the language is not found.
func (lmt LocalizedMailTemplate) ExecTemplate(data any, lang string) (*notifications.Notification, error) {
	template := lmt.GetTemplate(lang)
	return template.ExecTemplate(data)
}

// ExecPlain executes the plain text template for the given language with the provided data.
// Falls back to English if the language is not found.
func (lmt LocalizedMailTemplate) ExecPlain(data any, lang string) (*notifications.Notification, error) {
	template := lmt.GetTemplate(lang)
	return template.ExecPlain(data)
}

// VerifyAccountNotification is the notification to be sent when a user creates
// an account and needs to verify it.
var VerifyAccountNotification = LocalizedMailTemplate{
	Templates: map[string]MailTemplate{
		"en": {
			File: "verification_account",
			Placeholder: notifications.Notification{
				Subject: "Voxxoni verification code",
				PlainBody: `Your Voxxxni verification code is: {{.Code}}

You can also use this link to verify your account: {{.Link}}`,
			},
		},
		"es": {
			File: "verification_account_es",
			Placeholder: notifications.Notification{
				Subject: "Código de verificación de Vocdoni",
				PlainBody: `Tu código de verificación de Vocdoni es: {{.Code}}

También puedes usar este enlace para verificar tu cuenta: {{.Link}}`,
			},
		},
		"ca": {
			File: "verification_account_ca",
			Placeholder: notifications.Notification{
				Subject: "Codi de verificació de Vocdoni",
				PlainBody: `El teu codi de verificació de Vocdoni és: {{.Code}}

També pots utilitzar aquest enllaç per verificar el teu compte: {{.Link}}`,
			},
		},
	},
	WebAppURI: "/account/verify",
}

// VerifyOTPCodeNotification is the notification to be sent when a user wants
// to login using OTP code.
var VerifyOTPCodeNotification = LocalizedMailTemplate{
	Templates: map[string]MailTemplate{
		"en": {
			File: "verification_code_otp",
			Placeholder: notifications.Notification{
				Subject:   "Verification Code - Vocdoni",
				PlainBody: `Your verification code is: {{.Code}}`,
			},
		},
		"es": {
			File: "verification_code_otp_es",
			Placeholder: notifications.Notification{
				Subject:   "Código de Verificación - Vocdoni",
				PlainBody: `Tu código de verificación es: {{.Code}}`,
			},
		},
		"ca": {
			File: "verification_code_otp_ca",
			Placeholder: notifications.Notification{
				Subject:   "Codi de Verificació - Vocdoni",
				PlainBody: `El teu codi de verificació és: {{.Code}}`,
			},
		},
	},
}

// PasswordResetNotification is the notification to be sent when a user requests
// a password reset.
var PasswordResetNotification = LocalizedMailTemplate{
	Templates: map[string]MailTemplate{
		"en": {
			File: "forgot_password",
			Placeholder: notifications.Notification{
				Subject: "Vocdoni password reset",
				PlainBody: `Your Vocdoni password reset code is: {{.Code}}

You can also use this link to reset your password: {{.Link}}`,
			},
		},
		"es": {
			File: "forgot_password_es",
			Placeholder: notifications.Notification{
				Subject: "Restablecimiento de contraseña de Vocdoni",
				PlainBody: `Tu código de restablecimiento de contraseña de Vocdoni es: {{.Code}}

También puedes usar este enlace para restablecer tu contraseña: {{.Link}}`,
			},
		},
		"ca": {
			File: "forgot_password_ca",
			Placeholder: notifications.Notification{
				Subject: "Restabliment de contrasenya de Vocdoni",
				PlainBody: `El teu codi de restabliment de contrasenya de Vocdoni és: {{.Code}}

També pots utilitzar aquest enllaç per restablir la teva contrasenya: {{.Link}}`,
			},
		},
	},
	WebAppURI: "/account/password/reset",
}

// InviteNotification is the notification to be sent when a user is invited
// to be an admin of an organization.
var InviteNotification = LocalizedMailTemplate{
	Templates: map[string]MailTemplate{
		"en": {
			File: "invite_admin",
			Placeholder: notifications.Notification{
				Subject: "Vocdoni organization invitation",
				PlainBody: `Your code to join '{{.Organization}}' organization is: {{.Code}}

You can also use this link to join the organization: {{.Link}}`,
			},
		},
		"es": {
			File: "invite_admin_es",
			Placeholder: notifications.Notification{
				Subject: "Invitación a organización de Vocdoni",
				PlainBody: `Tu código para unirte a la organización '{{.Organization}}' es: {{.Code}}

También puedes usar este enlace para unirte a la organización: {{.Link}}`,
			},
		},
		"ca": {
			File: "invite_admin_ca",
			Placeholder: notifications.Notification{
				Subject: "Invitació a organització de Vocdoni",
				PlainBody: `El teu codi per unir-te a l'organització '{{.Organization}}' és: {{.Code}}

També pots utilitzar aquest enllaç per unir-te a l'organització: {{.Link}}`,
			},
		},
	},
	WebAppURI: "/account/invite",
}

// SupportNotification is a notification that does not require any template
var SupportNotification = LocalizedMailTemplate{
	Templates: map[string]MailTemplate{
		"en": {
			File: "support",
			Placeholder: notifications.Notification{
				Subject: "New {{.Type}} Ticket from {{.Email}}: {{.Title}}",
				PlainBody: `You have a new support request:

		Title: {{.Title}}
		Type: {{.Type}}
		User: {{.Email}}
		Organization: {{.Organization}}

		Description:
		{{.Description}}`,
			},
		},
		"es": {
			File: "support_es",
			Placeholder: notifications.Notification{
				Subject: "Nuevo Ticket {{.Type}} de {{.Email}}: {{.Title}}",
				PlainBody: `Tienes una nueva solicitud de soporte:

		Título: {{.Title}}
		Tipo: {{.Type}}
		Usuario: {{.Email}}
		Organización: {{.Organization}}

		Descripción:
		{{.Description}}`,
			},
		},
		"ca": {
			File: "support_ca",
			Placeholder: notifications.Notification{
				Subject: "Nou Ticket {{.Type}} de {{.Email}}: {{.Title}}",
				PlainBody: `Tens una nova sol·licitud de suport:

		Títol: {{.Title}}
		Tipus: {{.Type}}
		Usuari: {{.Email}}
		Organització: {{.Organization}}

		Descripció:
		{{.Description}}`,
			},
		},
	},
}
