package mailtemplates

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// completeLocales are the languages whose files must hold every key. Task 6 of the plan widens it
// to apicommon.SupportedLangs once the seven translations land.
var completeLocales = []string{"ca", "en", "es"}

func TestLocalesComplete(t *testing.T) {
	c := qt.New(t)
	st := loaded.Load()
	en := st.locales["en"]
	for _, lang := range completeLocales {
		loc := st.locales[lang]
		for key, ref := range en.entries {
			e, ok := loc.entries[key]
			c.Assert(ok, qt.IsTrue, qt.Commentf("%s is missing %s", lang, key))
			if ok {
				c.Assert(e.shape(), qt.Equals, ref.shape(), qt.Commentf("%s %s", lang, key))
			}
		}
		for key := range loc.entries {
			_, ok := en.entries[key]
			c.Assert(ok, qt.IsTrue, qt.Commentf("%s has %s, which English does not", lang, key))
		}
	}
}

func TestRenderAllFixtures(t *testing.T) {
	c := qt.New(t)
	for _, fx := range Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			comment := qt.Commentf("%s in %s", fx.Name, lang)
			n, err := fx.Template.Localized(lang).ExecTemplate(fx.Data)
			c.Assert(err, qt.IsNil, comment)
			c.Assert(len(n.Body) <= MaxHTMLBytes, qt.IsTrue, qt.Commentf("%s in %s: %d bytes", fx.Name, lang, len(n.Body)))
			c.Assert(utf8.RuneCountInString(n.Subject) <= MaxSubjectRunes, qt.IsTrue, qt.Commentf("%s: %q", comment, n.Subject))
			c.Assert(n.Subject, qt.Not(qt.Equals), "", comment)
			c.Assert(n.PlainBody, qt.Not(qt.Equals), "", comment)
			for _, leftover := range []string{"{{", "}}", "ZgotmplZ", "<no value>", "%!"} {
				c.Assert(n.Body, qt.Not(qt.Contains), leftover, comment)
				c.Assert(n.PlainBody, qt.Not(qt.Contains), leftover, comment)
			}
			c.Assert(n.Body, qt.Contains, `<html lang="`+lang+`"`, comment)
			c.Assert(n.Body, qt.Contains, "<!--[if mso]>", comment)
			if fx.Template != VerifyOTPCodeNotification { // the voter email shows Vocdoni only in the footer
				c.Assert(n.Body, qt.Contains, DefaultBrand().AssetsURL+"/vocdoni-wordmark-black-halo@2x.png", comment)
			}
			c.Assert(n.Body, qt.Not(qt.Contains), "/ipfs/", comment) // no third-party image gateway
			c.Assert(n.Body, qt.Not(qt.Contains), "<link", comment)  // no web fonts
		}
	}
}

func TestOTPSMSLength(t *testing.T) {
	c := qt.New(t)
	data := OTPData{Code: "472913", Organization: fixtureOrg, ExpiryTime: 15 * time.Minute}
	for _, lang := range apicommon.SupportedLangs {
		c.Run(lang, func(c *qt.C) {
			n, err := VerifyOTPCodeNotification.Localized(lang).ExecPlain(data)
			c.Assert(err, qt.IsNil)
			sms := "[STG] " + n.PlainBody // the longest label prefix the SMS sender adds
			c.Assert(utf8.RuneCountInString(sms) <= 70, qt.IsTrue,
				qt.Commentf("%d runes: %q", utf8.RuneCountInString(sms), sms))
			c.Assert(n.PlainBody, qt.Not(qt.Contains), "\n")
			c.Assert(n.PlainBody, qt.Contains, "472913")
			c.Assert(n.PlainBody, qt.Not(qt.Contains), fixtureOrg) // the SMS names no organization
			c.Assert(n.Body, qt.Equals, "")                        // ExecPlain renders no HTML
		})
	}
}

func TestEscaping(t *testing.T) {
	c := qt.New(t)
	data := NewSupportData(`<img src=x onerror=alert(1)>`, `"><script>alert(1)</script>`,
		"line one <script>alert(2)</script>\nline two", "a@b.c", `Org <b>&</b>`)
	n, err := SupportNotification.Localized("en").ExecTemplate(data)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), "<script>")
	c.Assert(n.Body, qt.Not(qt.Contains), "<img src=x")
	c.Assert(n.Body, qt.Not(qt.Contains), "<b>&")
	c.Assert(n.Body, qt.Contains, "line one &lt;script&gt;alert(2)&lt;/script&gt;<br>line two")
	c.Assert(n.PlainBody, qt.Contains, "line one <script>alert(2)</script>\nline two")
	c.Assert(n.Subject, qt.Equals, `New ticket · <img src=x onerror=alert(1)> · "><script>alert(1)</script>`)

	// a hostile link never becomes a javascript: URL
	n, err = VerifyAccountNotification.Localized("en").ExecTemplate(CodeData{
		Code: "<b>1</b>", Link: "javascript:alert(1)", Expiry: time.Minute,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), `href="javascript:`)
	c.Assert(n.Body, qt.Contains, "&lt;b&gt;1&lt;/b&gt;")
}

func TestImportIssueCap(t *testing.T) {
	c := qt.New(t)
	var fx Fixture
	for _, f := range Fixtures() {
		if f.Name == "members_import_done_manyerrors" {
			fx = f
		}
	}
	n, err := fx.Template.Localized("en").ExecTemplate(fx.Data)
	c.Assert(err, qt.IsNil)
	c.Assert(len(n.Body) <= MaxHTMLBytes, qt.IsTrue)
	c.Assert(regexp.MustCompile(`invalid email address`).FindAllString(n.Body, -1), qt.HasLen, ImportIssuesShown)
	c.Assert(n.Body, qt.Contains, "and 2,990 more errors")
	c.Assert(n.PlainBody, qt.Contains, "and 2,990 more errors")
	c.Assert(n.Subject, qt.Equals, "Members import completed with 3,000 errors at Ateneu Popular de Gràcia")
	c.Assert(n.Body, qt.Contains, "Review the member base")
	c.Assert(n.Body, qt.Contains, "November 25, 2026")
	c.Assert(n.Body, qt.Not(qt.Contains), "09:42")
}

func TestImportStates(t *testing.T) {
	c := qt.New(t)
	base := MembersImportResult{Organization: "Org", UserName: "Ana", Link: "https://app.example.org/admin/memberbase/"}
	for _, tc := range []struct {
		total, added int
		errors       []string
		subject      string
		heading      string
	}{
		{10, 10, nil, "Members import completed at Org", "Import completed"},
		{10, 9, []string{"row 2: bad"}, "Members import completed with 1 error at Org", "Import completed with errors"},
		{10, 0, make([]string, 10), "No members could be imported at Org", "No members could be imported"},
		{0, 0, nil, "No members could be imported at Org", "No members could be imported"},
	} {
		r := base
		r.Total, r.Added, r.Errors = tc.total, tc.added, tc.errors
		n, err := MembersImportCompletionNotification.Localized("en").ExecTemplate(NewMembersImportData(r))
		c.Assert(err, qt.IsNil)
		c.Assert(n.Subject, qt.Equals, tc.subject)
		c.Assert(n.Body, qt.Contains, ">"+tc.heading+"</h1>")
		if tc.added == 0 {
			c.Assert(strings.ToLower(n.Subject+n.PlainBody), qt.Not(qt.Contains), "completed")
			c.Assert(strings.ToLower(n.PlainBody), qt.Not(qt.Contains), "success")
		}
	}
	// no name, no organization, no link: no dangling clause, no button
	n, err := MembersImportCompletionNotification.Localized("en").ExecTemplate(
		NewMembersImportData(MembersImportResult{Total: 2, Added: 2}))
	c.Assert(err, qt.IsNil)
	c.Assert(n.Subject, qt.Equals, "Members import completed")
	c.Assert(n.PlainBody, qt.Contains, "Hello. All done!")
	c.Assert(n.Body, qt.Not(qt.Contains), "v:roundrect")
	c.Assert(n.Body, qt.Not(qt.Contains), "Review the member base")
	c.Assert(n.Body, qt.Not(qt.Contains), " at <strong>")
}

func TestSubjectFitting(t *testing.T) {
	c := qt.New(t)
	n, err := SupportNotification.Localized("ca").ExecTemplate(NewSupportData("Incidència",
		strings.Repeat("No puc publicar una votació ", 10), "x", "a@b.c", "Org"))
	c.Assert(err, qt.IsNil)
	c.Assert(utf8.RuneCountInString(n.Subject), qt.Equals, MaxSubjectRunes)
	c.Assert(n.Subject, qt.Matches, `Nou tiquet · Incidència · No puc publicar.*…`)

	n, err = VerifyOTPCodeNotification.Localized("en").ExecTemplate(OTPData{
		Code: "1", Organization: strings.Repeat("Ö", 200), ExpiryTime: time.Minute,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(utf8.RuneCountInString(n.Subject), qt.Equals, MaxSubjectRunes)
	// the body keeps the full name: it wraps, it is never cut
	c.Assert(n.Body, qt.Contains, strings.Repeat("Ö", 200))
}

func TestOrgLogoInMail(t *testing.T) {
	c := qt.New(t)
	local := DefaultBrand().ServerURL + "/storage/0f3a.png"
	data := InviteData{Organization: "Org", OrganizationLogo: local, Link: "https://app.example.org/x", Expiry: time.Hour}
	n, err := InviteNotification.Localized("en").ExecTemplate(data)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Contains, `<!--[if !mso]><!--><table role="presentation" cellpadding="0" cellspacing="0" align="right">`)
	c.Assert(n.Body, qt.Contains, `<img src="`+local+`" height="32"`)

	for _, rejected := range []string{"https://tracker.example.com/p.png", DefaultBrand().ServerURL + "/storage/x.svg", ""} {
		data.OrganizationLogo = rejected
		n, err := InviteNotification.Localized("en").ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(n.Body, qt.Not(qt.Contains), `height="32"`, qt.Commentf("%q", rejected))
		if rejected != "" {
			c.Assert(n.Body, qt.Not(qt.Contains), rejected)
		}
	}

	otp := OTPData{Code: "1", Organization: "ateneu", OrganizationLogo: "https://example.com/logo.png", ExpiryTime: time.Minute}
	n, err = VerifyOTPCodeNotification.Localized("en").ExecTemplate(otp)
	c.Assert(err, qt.IsNil)
	c.Assert(n.Body, qt.Not(qt.Contains), "example.com/logo.png")
	c.Assert(n.Body, qt.Contains, `font-weight:600;color:#171717;">A</td>`) // the monogram
}

func TestCodeEmailsPlainText(t *testing.T) {
	c := qt.New(t)
	// api/users_test.go builds its code regexps from the first plain-text line in English
	for mt, first := range map[MailTemplate]string{
		VerifyAccountNotification:     "Your Vocdoni verification code is: 4e92af",
		PasswordResetNotification:     "Your Vocdoni password reset code is: 4e92af",
		PasswordSetupNotification:     "Your Vocdoni password setup code is: 4e92af",
		VerifyEmailChangeNotification: "Your code to confirm your new email address is: 4e92af",
	} {
		n, err := mt.Localized("en").ExecTemplate(CodeData{
			Code: "4e92af", Link: "https://app.example.org" + mt.WebAppURI + "?code=4e92af", Expiry: 15 * time.Minute,
		})
		c.Assert(err, qt.IsNil)
		lines := strings.Split(n.PlainBody, "\n")
		c.Assert(lines[0], qt.Equals, first)
		c.Assert(lines[1], qt.Equals, "Expires in 15 minutes.")
		c.Assert(n.PlainBody, qt.Contains, ": https://app.example.org"+mt.WebAppURI+"?code=4e92af")
		c.Assert(n.Body, qt.Contains, "Expires in 15 minutes")
	}
}

func TestLocalizedFallsBack(t *testing.T) {
	c := qt.New(t)
	for _, lang := range []string{"", "xx", "EN", "pt_BR"} {
		n, err := VerifyAccountNotification.Localized(lang).ExecTemplate(CodeData{Code: "1", Expiry: time.Minute})
		c.Assert(err, qt.IsNil)
		c.Assert(n.Subject, qt.Equals, "Verify your Vocdoni account")
	}
	c.Assert(slices.Contains(apicommon.SupportedLangs, apicommon.DefaultLang), qt.IsTrue)
}
