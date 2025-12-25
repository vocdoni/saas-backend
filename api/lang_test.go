package api

import (
	"fmt"
	"regexp"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
)

// TestLanguageParameterInEmails tests that the lang parameter is correctly processed
// and affects the language of emails sent to users during CSP authentication.
func TestLanguageParameterInEmails(t *testing.T) {
	test := func(lang string) {
		c := qt.New(t)

		// Set up test environment with user, org, and census
		token := testCreateUser(t, "superpassword123")
		orgAddress := testCreateOrganization(t, token)
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

		// Test with Spanish language parameter
		authReq := &handlers.AuthRequest{
			Name:         member.Name,
			Surname:      member.Surname,
			MemberNumber: member.MemberNumber,
			Email:        member.Email,
		}

		// Make the request with lang query parameter
		query := "0"
		if lang != "" {
			query = fmt.Sprintf("0?lang=%s", lang)
		}
		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", query)
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		mailBody := waitForEmail(t, member.Email)
		assertContentMatches(t, mailBody, lang,
			map[string]*regexp.Regexp{
				"es": regexp.MustCompile(`(?i)\s(código|verificación|cuenta)\s`),
				"en": regexp.MustCompile(`(?i)\s(code|verification|account)\s`),
				"ca": regexp.MustCompile(`(?i)\s(codi|verificació|compte)\s`),
			})
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

		mailBody := waitForEmail(t, userInfo.Email)
		assertContentMatches(t, mailBody, lang,
			map[string]*regexp.Regexp{
				"es": regexp.MustCompile(`(?i)\s(código|verificación|cuenta)\s`),
				"en": regexp.MustCompile(`(?i)\s(code|verification|account)\s`),
				"ca": regexp.MustCompile(`(?i)\s(codi|verificació|compte)\s`),
			})
	}
	t.Run("Spanish Registration Email", func(*testing.T) { test("es") })
	t.Run("English Registration Email", func(*testing.T) { test("en") })
	t.Run("Catalan Registration Email", func(*testing.T) { test("ca") })
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
