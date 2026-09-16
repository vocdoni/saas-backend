package api

import (
	"fmt"
	"regexp"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

// otpEmailRegexps matches a distinctive word of the CSP OTP email in each
// supported language.
var otpEmailRegexps = map[string]*regexp.Regexp{
	"es": regexp.MustCompile(`(?i)\s(código|verificación|cuenta)\s`),
	"en": regexp.MustCompile(`(?i)\s(code|verification|account)\s`),
	"ca": regexp.MustCompile(`(?i)\s(codi|verificació|compte)\s`),
}

// TestOrgLangInCSPEmails tests that the organization language decides the CSP
// authentication OTP emails when the voter sends no lang parameter. One who
// does overrides it (see TestOrgDefaultLangNotifications).
func TestOrgLangInCSPEmails(t *testing.T) {
	test := func(lang string) {
		c := qt.New(t)

		// Set up test environment with user, org, and census
		token := testCreateUser(t, "superpassword123")
		orgAddress := testCreateOrganization(t, token)
		if lang != "" {
			requestAndAssertCode(200, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: lang},
				"organizations", orgAddress.String())
		}
		member := newOrgMember()
		addedMembers := postOrgMembers(t, token, orgAddress, member)
		censusID, _, _ := createGroupBasedCensus(t, token, orgAddress,
			db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsSurname,
				db.OrgMemberAuthFieldsMemberNumber,
			},
			db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
			},
			addedMembers[0].ID)
		bundleID, _ := postProcessBundle(t, token, censusID, randomProcessID())

		authReq := &handlers.AuthRequest{
			Name:         member.Name,
			Surname:      member.Surname,
			MemberNumber: member.MemberNumber,
			Email:        member.Email,
		}

		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", "0")
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		assertContentMatches(t, waitForEmail(t, member.Email), lang, otpEmailRegexps)
	}

	t.Run("Default CSP Auth Email", func(*testing.T) { test("") })
	t.Run("English CSP Auth Email", func(*testing.T) { test("en") })
	t.Run("Catalan CSP Auth Email", func(*testing.T) { test("ca") })
	t.Run("Spanish CSP Auth Email", func(*testing.T) { test("es") })
}

// TestLanguageParameterInUserRegistration tests the language parameter in user registration emails
func TestLanguageParameterInUserRegistration(t *testing.T) {
	test := func(lang string) {
		userInfo := db.User{
			Email:     fmt.Sprintf("testLang%s@example.com", lang),
			Password:  "password123",
			FirstName: "María",
			LastName:  "García",
		}
		// Register user with lang parameter
		requestAndAssertCode(200, t, "POST", "", userInfo, fmt.Sprintf("users?lang=%s", lang))

		assertContentMatches(t, waitForEmail(t, userInfo.Email), lang, otpEmailRegexps)
	}
	t.Run("Spanish Registration Email", func(*testing.T) { test("es") })
	t.Run("English Registration Email", func(*testing.T) { test("en") })
	t.Run("Catalan Registration Email", func(*testing.T) { test("ca") })
}

// TestOrgDefaultLangNotifications tests the public side of the rule: the
// organization defaultLang applies, and a voter's lang param overrides it. The
// protected side is TestOrgLangInInvites.
func TestOrgDefaultLangNotifications(t *testing.T) {
	c := qt.New(t)

	// set up an organization with catalan as default language
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateOrganization(t, token)
	requestAndAssertCode(200, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: "ca"},
		"organizations", orgAddress.String())

	members := newOrgMembers(2)
	addedMembers := postOrgMembers(t, token, orgAddress, members...)
	censusID, _, _ := createGroupBasedCensus(t, token, orgAddress,
		db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		},
		db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail,
		},
		addedMembers[0].ID, addedMembers[1].ID)
	bundleID, _ := postProcessBundle(t, token, censusID, randomProcessID())

	authReq := func(m apicommon.OrgMember) *handlers.AuthRequest {
		return &handlers.AuthRequest{
			Name:         m.Name,
			Surname:      m.Surname,
			MemberNumber: m.MemberNumber,
			Email:        m.Email,
		}
	}

	// without a lang param, the CSP auth email must fall back to the org
	// default language
	authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq(members[0]),
		"process", "bundle", bundleID, "auth", "0")
	c.Assert(authResp.AuthToken, qt.Not(qt.HasLen), 0)
	assertContentMatches(t, waitForEmail(t, members[0].Email), "ca", otpEmailRegexps)

	// a resend without a lang param must also fall back to the org default
	resendResp := requestAndParse[handlers.AuthResponse](t, "POST", "",
		&handlers.AuthResendRequest{AuthToken: authResp.AuthToken, Email: members[0].Email},
		"process", "bundle", bundleID, "auth", "resend")
	c.Assert(resendResp.AuthToken, qt.Not(qt.HasLen), 0)
	assertContentMatches(t, waitForEmail(t, members[0].Email), "ca", otpEmailRegexps)

	// a voter's explicit lang param overrides the org default: the CSP endpoints
	// are public, so the param is the recipient's own choice
	authResp = requestAndParse[handlers.AuthResponse](t, "POST", "", authReq(members[1]),
		"process", "bundle", bundleID, "auth", "0?lang=es")
	c.Assert(authResp.AuthToken, qt.Not(qt.HasLen), 0)
	assertContentMatches(t, waitForEmail(t, members[1].Email), "es", otpEmailRegexps)
}

// TestOrgLangInInvites tests the protected side: inviting a user is sent on
// behalf of the organization, so its language wins over the admin's lang param.
func TestOrgLangInInvites(t *testing.T) {
	inviteRegexps := map[string]*regexp.Regexp{
		"en": regexp.MustCompile(`You have been invited`),
		"es": regexp.MustCompile(`Has sido invitado`),
		"ca": regexp.MustCompile(`Has estat convidat`),
	}

	// set up an organization with catalan as default language
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateOrganization(t, token)
	requestAndAssertCode(200, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: "ca"},
		"organizations", orgAddress.String())

	// the admin invites someone while asking for spanish
	invitee := "invitee@example.com"
	requestAndAssertCode(200, t, "POST", token,
		&apicommon.OrganizationInvite{Email: invitee, Role: db.ViewerRole},
		"organizations", orgAddress.String(), "users?lang=es")

	assertContentMatches(t, waitForEmail(t, invitee), "ca", inviteRegexps)
}

// TestOrgDefaultLangInMembersImport tests that the async members-import
// completion email uses the org default language, even though it is sent from
// a goroutine that outlives the request.
func TestOrgDefaultLangInMembersImport(t *testing.T) {
	c := qt.New(t)

	// set up an organization with catalan as default language
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateOrganization(t, token)
	requestAndAssertCode(200, t, "PUT", token, &apicommon.OrganizationInfo{DefaultLang: "ca"},
		"organizations", orgAddress.String())
	adminEmail := requestAndParse[apicommon.UserInfo](t, "GET", token, nil, usersMeEndpoint).Email

	// import members asynchronously and wait for the job to complete
	asyncResp := requestAndParse[apicommon.AddMembersResponse](t, "POST", token,
		&apicommon.AddMembersRequest{Members: newOrgMembers(2)},
		"organizations", orgAddress.String(), "members?async=true")
	var jobID internal.HexBytes
	jobID.SetBytes(asyncResp.JobID)
	jobStatus := pollOrgJob(t, token, orgAddress.String(), jobID.String())
	c.Assert(jobStatus.Status, qt.Equals, db.JobStatusCompleted)

	// the completion email to the admin must be in the org default language
	assertContentMatches(t, waitForEmail(t, adminEmail), "ca",
		map[string]*regexp.Regexp{
			"en": regexp.MustCompile(`has been completed`),
			"es": regexp.MustCompile(`se ha completado`),
			"ca": regexp.MustCompile(`s'ha completat`),
		})
}

func assertContentMatches(t *testing.T, content, lang string, regexps map[string]*regexp.Regexp) {
	t.Helper()
	c := qt.New(t)
	for regexLang, re := range regexps {
		if regexLang == lang ||
			(lang == "" && regexLang == apicommon.DefaultLang) {
			c.Assert(content, qt.Matches, re,
				qt.Commentf("content should match %s, got:\n%s", re, content))
		} else {
			c.Assert(content, qt.Not(qt.Matches), re,
				qt.Commentf("content should not match %s when lang=%s, got:\n%s", re, lang, re.FindAllString(content, -1)))
		}
	}
}
