// Package mailtemplates provides predefined email templates for various notification types
// such as account verification, password reset, and organization invitations,
// along with utilities for rendering email content.
package mailtemplates

// VerifyAccountNotification is the notification to be sent when a user creates
// an account and needs to verify it.
var VerifyAccountNotification = MailTemplate{
	File:      "verification_account",
	WebAppURI: "/account/verify",
}

// VerifyOTPCodeNotification is the notification to be sent when a user wants
// to login using OTP code.
var VerifyOTPCodeNotification = MailTemplate{
	File: "verification_code_otp",
}

// PasswordResetNotification is the notification to be sent when a user requests
// a password reset.
var PasswordResetNotification = MailTemplate{
	File:      "forgot_password",
	WebAppURI: "/account/password/reset",
}

// InviteNotification is the notification to be sent when a user is invited
// to be an admin of an organization.
var InviteNotification = MailTemplate{
	File:      "invite_admin",
	WebAppURI: "/account/invite",
}

// SupportNotification is the notification to be sent when a user creates
// a new support ticket.
var SupportNotification = MailTemplate{
	File: "support",
}

// MembersImportCompletionNotification is the notification to be sent when
// a members import process is completed.
var MembersImportCompletionNotification = MailTemplate{
	File: "members_import_done",
}
