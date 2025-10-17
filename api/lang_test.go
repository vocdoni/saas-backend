package api

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

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

		// Set up the test environment similar to TestCSPVoting
		token := testCreateUser(t, "superpassword123")
		orgAddress := testCreateOrganization(t, token)

		// Create a basic census and bundle for testing
		authFields := db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		}
		twoFaFields := db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail,
		}
		censusID := testCreateCensus(t, token, orgAddress, authFields, twoFaFields)
		// Add a test member to the organization
		member := apicommon.OrgMember{
			Name:         "Juan",
			Surname:      "Pérez",
			MemberNumber: "ESP001",
			Email:        fmt.Sprintf("testLang%s@example.com", lang),
		}
		members := []apicommon.OrgMember{member}

		// Add members to the organization
		membersRequest := apicommon.AddMembersRequest{Members: members}
		requestAndAssertCode(200, t, "POST", token, membersRequest,
			"organizations", orgAddress.String(), "members")

		// Get the organization members to obtain their IDs
		orgMembersResp := requestAndParse[apicommon.OrganizationMembersResponse](
			t, "GET", token, nil,
			"organizations", orgAddress.String(), "members")

		c.Assert(len(orgMembersResp.Members), qt.Equals, 1, qt.Commentf("should have 1 member"))

		memberID := orgMembersResp.Members[0].ID

		// Create a group with the member
		createGroupReq := apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Language Test Group",
			Description: "Group for testing language parameter",
			MemberIDs:   []string{memberID},
		}

		groupResp := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, "POST", token, createGroupReq,
			"organizations", orgAddress.String(), "groups")

		groupID := groupResp.ID

		// Publish the group-based census
		publishGroupRequest := apicommon.PublishCensusGroupRequest{
			AuthFields:  authFields,
			TwoFaFields: twoFaFields,
		}

		requestAndParse[map[string]any](
			t, "POST", token, publishGroupRequest,
			"census", censusID, "group", groupID, "publish")

		// Create a mock process ID for the bundle
		processID := make([]byte, 32)
		for i := range processID {
			processID[i] = byte(i)
		}

		bundleID, _ := testCreateBundle(t, token, censusID, [][]byte{processID})

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

func waitForEmail(t *testing.T, emailTo string) string {
	c := qt.New(t)

	// Wait for and retrieve the email
	var mailBody string
	var err error
	maxRetries := 10
	for i := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		mailBody, err = testMailService.FindEmail(ctx, emailTo)
		cancel()
		if err == nil {
			break
		}
		t.Logf("Waiting for email, attempt %d/%d...", i+1, maxRetries)
		time.Sleep(1000 * time.Millisecond)
	}
	c.Assert(err, qt.IsNil, qt.Commentf("failed to receive email after %d attempts", maxRetries))
	return mailBody
}

func assertContentMatches(t *testing.T, content, lang string, regexps map[string]*regexp.Regexp) {
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
