package mailtemplates

import (
	"fmt"
	"strings"
	"time"
)

// Fixture is a sample email: Load renders every fixture in every language, the tests check them
// and cmd/mailpreview writes them out.
type Fixture struct {
	Name     string // unique, e.g. "members_import_done_manyerrors"
	Template MailTemplate
	Data     any
}

// FixtureOrgLogo is the organization logo of the fixtures: a stored object under DefaultBrand's
// server URL, so it passes the logo filter. cmd/mailpreview swaps it for a local sample image.
const FixtureOrgLogo = "http://localhost:8080/storage/0f3a5c7e9b1d2f4a6c8e0b2d4f6a8c0e.png"

const (
	fixtureCode     = "4e92af"
	fixtureOrg      = "Ateneu Popular de Gràcia"
	fixtureLongOrg  = "Col·legi Oficial d’Enginyeria Informàtica de Catalunya i les Illes Balears"
	fixtureWebApp   = "https://app.vocdoni.io"
	fixtureLongMail = "mariona.soler.i.puigdollers.de.la.vall.del.riu@cooperativa-llum-del-nord-exemple.cat"
)

// Fixtures returns every sample email, the edge cases of the design included.
func Fixtures() []Fixture {
	completed := time.Date(2026, time.November, 25, 9, 42, 0, 0, time.UTC)
	codeEmail := func(name string, mt MailTemplate, email string) Fixture {
		return Fixture{Name: name, Template: mt, Data: CodeData{
			Code:   fixtureCode,
			Link:   fixtureWebApp + mt.WebAppURI + "?code=" + fixtureCode + "&email=" + strings.ReplaceAll(email, "@", "%40"),
			Email:  email,
			Expiry: 15 * time.Minute,
		}}
	}
	importData := func(r MembersImportResult) MembersImportData {
		r.Organization, r.OrganizationLogo, r.UserName = fixtureOrg, FixtureOrgLogo, "Mariona"
		r.Link, r.CompletedAt = fixtureWebApp+"/admin/memberbase/", completed
		return NewMembersImportData(r)
	}
	issues := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("row %d: invalid email address <user%d@example>", i+2, i)
		}
		return out
	}
	longIssue := []string{"row 2: " + strings.Repeat("the value of the group column is not one we know about; ", 6)}
	invite := InviteData{
		Organization:     fixtureOrg,
		OrganizationLogo: FixtureOrgLogo,
		Link:             fixtureWebApp + "/account/invite?code=9f2c4e7a1b8d3f605c2e9a4b7d1f8e30&email=mariona%40exemple.cat",
		Expiry:           5 * 24 * time.Hour,
	}
	inviteNoOrg := invite
	inviteNoOrg.Organization, inviteNoOrg.OrganizationLogo = "", ""
	inviteExternalLogo := invite
	inviteExternalLogo.OrganizationLogo = "https://tracker.example.com/pixel.png"
	importNoName := importData(MembersImportResult{Total: 1248, Added: 0, Errors: issues(1248)})
	importNoName.UserName, importNoName.Organization, importNoName.Link = "", "", ""
	longDescription := strings.Repeat("The publish button keeps loading and then says \"transaction failed\".\n\n", 30)
	otp := OTPData{Code: "472913", Organization: fixtureOrg, OrganizationLogo: FixtureOrgLogo, ExpiryTime: 15 * time.Minute}
	otpLong := otp
	otpLong.Organization, otpLong.OrganizationLogo = fixtureLongOrg, ""
	return []Fixture{
		codeEmail("verification_account", VerifyAccountNotification, "mariona@exemple.cat"),
		codeEmail("verification_account_longemail", VerifyAccountNotification, fixtureLongMail),
		codeEmail("forgot_password", PasswordResetNotification, "mariona@exemple.cat"),
		codeEmail("password_setup", PasswordSetupNotification, "mariona@exemple.cat"),
		codeEmail("verify_email_change", VerifyEmailChangeNotification, "nova.adreca@exemple.cat"),
		{Name: "invite_admin", Template: InviteNotification, Data: invite},
		{Name: "invite_admin_noorg", Template: InviteNotification, Data: inviteNoOrg},
		{Name: "invite_admin_externallogo", Template: InviteNotification, Data: inviteExternalLogo},
		{
			Name: "members_import_done", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1248}),
		},
		{
			Name: "members_import_done_errors", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1245, Errors: issues(3)}),
		},
		{
			Name: "members_import_done_oneerror", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 1248, Added: 1247, Errors: longIssue}),
		},
		{
			Name: "members_import_done_manyerrors", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{Total: 3250, Added: 250, Errors: issues(3000)}),
		},
		{
			Name: "members_import_done_allfailed_noname_noorg_nolink", Template: MembersImportCompletionNotification,
			Data: importNoName,
		},
		{
			Name: "members_import_done_empty", Template: MembersImportCompletionNotification,
			Data: importData(MembersImportResult{}),
		},
		{Name: "support", Template: SupportNotification, Data: NewSupportData("Bug", "I cannot publish a vote",
			"When I publish the process the button keeps loading.\nI tried two browsers.", "mariona@exemple.cat", fixtureOrg)},
		{Name: "support_longdesc", Template: SupportNotification, Data: NewSupportData(
			"Bug <b>", strings.Repeat("Very long title ", 10), "<script>alert(1)</script>\n"+longDescription,
			fixtureLongMail, fixtureLongOrg)},
		{Name: "verification_code_otp", Template: VerifyOTPCodeNotification, Data: otp},
		{Name: "verification_code_otp_longorg", Template: VerifyOTPCodeNotification, Data: otpLong},
	}
}
