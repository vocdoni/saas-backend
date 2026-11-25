// Package mailtemplates renders the transactional emails: one shared layout and set of
// components (assets/mail), one content block per email, and the strings of every language in
// assets/mail/i18n/<lang>.yaml.
package mailtemplates

// VerifyAccountNotification is sent when a user creates an account and needs to verify it.
var VerifyAccountNotification = MailTemplate{
	File:      "verification_account",
	WebAppURI: "/account/verify",
}

// VerifyOTPCodeNotification is the voter's CSP challenge, by email or (plain text) by SMS.
var VerifyOTPCodeNotification = MailTemplate{
	File: "verification_code_otp",
}

// PasswordResetNotification is sent when a user requests a password reset.
var PasswordResetNotification = MailTemplate{
	File:      "forgot_password",
	WebAppURI: "/account/password/reset",
}

// PasswordSetupNotification is sent when a signed-in account without a password (OAuth-only)
// asks to add one, with the code that proves it owns the mailbox.
var PasswordSetupNotification = MailTemplate{
	File:      "password_setup",
	WebAppURI: "/account/password/setup",
}

// VerifyEmailChangeNotification is sent to the new address when a user asks to change their
// account email, with the code that confirms it.
var VerifyEmailChangeNotification = MailTemplate{
	File:      "verify_email_change",
	WebAppURI: "/account/email/verify",
}

// InviteNotification is sent when a user is invited to administer an organization.
var InviteNotification = MailTemplate{
	File:      "invite_admin",
	WebAppURI: "/account/invite",
}

// SupportNotification is sent to the support desk, with the user in CC, for a new ticket.
var SupportNotification = MailTemplate{
	File: "support",
}

// MembersImportCompletionNotification is sent to the admin who ran a members import when it ends.
var MembersImportCompletionNotification = MailTemplate{
	File: "members_import_done",
}

// All lists every email; Load compiles each one.
var All = []MailTemplate{
	VerifyAccountNotification,
	PasswordResetNotification,
	PasswordSetupNotification,
	VerifyEmailChangeNotification,
	InviteNotification,
	MembersImportCompletionNotification,
	SupportNotification,
	VerifyOTPCodeNotification,
}
