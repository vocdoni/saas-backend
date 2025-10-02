package api

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
)

// TestLanguageParameterInEmails tests that the lang parameter is correctly processed
// and affects the language of emails sent to users during CSP authentication.
func TestLanguageParameterInEmails(t *testing.T) {
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
	members := []map[string]any{
		{
			"name":         "Juan",
			"surname":      "Pérez",
			"memberNumber": "ESP001",
			"email":        "juan.perez@example.com",
		},
	}

	// Add members to the organization
	membersRequest := map[string]any{"members": members}
	requestAndAssertCode(200, t, "POST", token, membersRequest,
		"organizations", orgAddress.String(), "members")

	// Get the organization members to obtain their IDs
	orgMembersResp := requestAndParse[map[string]any](
		t, "GET", token, nil,
		"organizations", orgAddress.String(), "members")

	membersList, ok := orgMembersResp["members"].([]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("members should be an array"))
	c.Assert(len(membersList), qt.Equals, 1, qt.Commentf("should have 1 member"))

	member := membersList[0].(map[string]any)
	memberID := member["id"].(string)

	// Create a group with the member
	createGroupReq := map[string]any{
		"title":       "Language Test Group",
		"description": "Group for testing language parameter",
		"memberIDs":   []string{memberID},
	}

	groupResp := requestAndParse[map[string]any](
		t, "POST", token, createGroupReq,
		"organizations", orgAddress.String(), "groups")

	groupID := groupResp["id"].(string)

	// Publish the group-based census
	publishGroupRequest := map[string]any{
		"authFields":  authFields,
		"twoFaFields": twoFaFields,
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

	t.Run("Spanish Language Email Test", func(t *testing.T) {
		c := qt.New(t)

		// Test with Spanish language parameter
		authReq := &handlers.AuthRequest{
			Name:         "Juan",
			Surname:      "Pérez",
			MemberNumber: "ESP001",
			Email:        "juan.perez@example.com",
		}

		// Make the request with lang=es query parameter
		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", "0?lang=es")
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		// Wait for and retrieve the email
		var mailBody string
		var err error
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			mailBody, err = testMailService.FindEmail(ctx, "juan.perez@example.com")
			cancel()
			if err == nil {
				break
			}
			t.Logf("Waiting for Spanish email, attempt %d/%d...", i+1, maxRetries)
			time.Sleep(1000 * time.Millisecond)
		}
		c.Assert(err, qt.IsNil, qt.Commentf("failed to receive Spanish email after %d attempts", maxRetries))

		// Check that the email contains Spanish text
		spanishRegex := regexp.MustCompile(`(?i)código|codigo`)
		c.Assert(spanishRegex.MatchString(mailBody), qt.IsTrue,
			qt.Commentf("email should contain Spanish word 'código' or 'codigo', got: %s", mailBody))

		t.Logf("Spanish email content verified: found Spanish text in email")
	})

	t.Run("English Language Email Test", func(t *testing.T) {
		c := qt.New(t)

		// Clear any previous emails
		time.Sleep(2 * time.Second)

		// Test with English language parameter
		authReq := &handlers.AuthRequest{
			Name:         "Juan",
			Surname:      "Pérez",
			MemberNumber: "ESP001",
			Email:        "juan.perez@example.com",
		}

		// Make the request with lang=en query parameter
		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", "0?lang=en")
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		// Wait for and retrieve the email
		var mailBody string
		var err error
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			mailBody, err = testMailService.FindEmail(ctx, "juan.perez@example.com")
			cancel()
			if err == nil {
				break
			}
			t.Logf("Waiting for English email, attempt %d/%d...", i+1, maxRetries)
			time.Sleep(1000 * time.Millisecond)
		}
		c.Assert(err, qt.IsNil, qt.Commentf("failed to receive English email after %d attempts", maxRetries))

		// Check that the email contains English text
		englishRegex := regexp.MustCompile(`(?i)\bcode\b`)
		c.Assert(englishRegex.MatchString(mailBody), qt.IsTrue,
			qt.Commentf("email should contain English word 'code', got: %s", mailBody))

		// Also verify it doesn't contain Spanish text
		spanishRegex := regexp.MustCompile(`(?i)código|codigo`)
		c.Assert(spanishRegex.MatchString(mailBody), qt.IsFalse,
			qt.Commentf("email should not contain Spanish text when lang=en, got: %s", mailBody))

		t.Logf("English email content verified: found English text and no Spanish text in email")
	})

	t.Run("Default Language Test (No Lang Parameter)", func(t *testing.T) {
		c := qt.New(t)

		// Clear any previous emails
		time.Sleep(2 * time.Second)

		// Test without language parameter (should default to English)
		authReq := &handlers.AuthRequest{
			Name:         "Juan",
			Surname:      "Pérez",
			MemberNumber: "ESP001",
			Email:        "juan.perez@example.com",
		}

		// Make the request without lang parameter
		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", "0")
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		// Wait for and retrieve the email
		var mailBody string
		var err error
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			mailBody, err = testMailService.FindEmail(ctx, "juan.perez@example.com")
			cancel()
			if err == nil {
				break
			}
			t.Logf("Waiting for default language email, attempt %d/%d...", i+1, maxRetries)
			time.Sleep(1000 * time.Millisecond)
		}
		c.Assert(err, qt.IsNil, qt.Commentf("failed to receive default language email after %d attempts", maxRetries))

		// Check that the email contains English text (default)
		englishRegex := regexp.MustCompile(`(?i)\bcode\b`)
		c.Assert(englishRegex.MatchString(mailBody), qt.IsTrue,
			qt.Commentf("email should contain English word 'code' by default, got: %s", mailBody))

		t.Logf("Default language email content verified: found English text in email")
	})

	t.Run("Catalan Language Email Test", func(t *testing.T) {
		c := qt.New(t)

		// Clear any previous emails
		time.Sleep(2 * time.Second)

		// Test with Catalan language parameter
		authReq := &handlers.AuthRequest{
			Name:         "Juan",
			Surname:      "Pérez",
			MemberNumber: "ESP001",
			Email:        "juan.perez@example.com",
		}

		// Make the request with lang=ca query parameter
		authResp := requestAndParse[handlers.AuthResponse](t, "POST", "", authReq,
			"process", "bundle", bundleID, "auth", "0?lang=ca")
		c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

		// Wait for and retrieve the email
		var mailBody string
		var err error
		maxRetries := 10
		for i := 0; i < maxRetries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			mailBody, err = testMailService.FindEmail(ctx, "juan.perez@example.com")
			cancel()
			if err == nil {
				break
			}
			t.Logf("Waiting for Catalan email, attempt %d/%d...", i+1, maxRetries)
			time.Sleep(1000 * time.Millisecond)
		}
		c.Assert(err, qt.IsNil, qt.Commentf("failed to receive Catalan email after %d attempts", maxRetries))

		// Check that the email contains Catalan text (codi is the Catalan word for code)
		catalanRegex := regexp.MustCompile(`(?i)\bcodi\b`)
		c.Assert(catalanRegex.MatchString(mailBody), qt.IsTrue,
			qt.Commentf("email should contain Catalan word 'codi', got: %s", mailBody))

		t.Logf("Catalan email content verified: found Catalan text in email")
	})
}

// TestLanguageParameterInUserRegistration tests the language parameter in user registration emails
func TestLanguageParameterInUserRegistration(t *testing.T) {
	regexps := map[string]*regexp.Regexp{
		"es": regexp.MustCompile(`(?i)\s(código|verificación|cuenta)\s`),
		"en": regexp.MustCompile(`(?i)\s(code|verification|account)\s`),
		"ca": regexp.MustCompile(`(?i)\s(codi|verificació|compte)\s`),
	}

	test := func(lang string) {
		c := qt.New(t)
		userInfo := db.User{
			Email:     fmt.Sprintf("test.%s@example.com", lang),
			Password:  "password123",
			FirstName: "María",
			LastName:  "García",
		}
		// Register user with lang parameter
		requestAndAssertCode(200, t, "POST", "", userInfo, fmt.Sprintf("users?lang=%s", lang))

		// Wait for and retrieve the verification email
		var mailBody string
		var err error
		maxRetries := 10
		for i := range maxRetries {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			mailBody, err = testMailService.FindEmail(ctx, userInfo.Email)
			cancel()
			if err == nil {
				break
			}
			t.Logf("Waiting for registration email, attempt %d/%d...", i+1, maxRetries)
			time.Sleep(1000 * time.Millisecond)
		}
		c.Assert(err, qt.IsNil, qt.Commentf("failed to receive registration email after %d attempts", maxRetries))

		// Check for text in registration email
		for reLang, re := range regexps {
			if reLang == lang {
				c.Assert(mailBody, qt.Matches, re,
					qt.Commentf("registration email should match %s, got:\n%s", re, mailBody))
			} else {
				c.Assert(mailBody, qt.Not(qt.Matches), re,
					qt.Commentf("registration email should not match %s when lang=%s, got:\n%s", re, lang, re.FindAllString(mailBody, -1)))
			}
		}
	}
	t.Run("Spanish Registration Email", func(*testing.T) { test("es") })
	t.Run("English Registration Email", func(*testing.T) { test("en") })
	t.Run("Catalan Registration Email", func(*testing.T) { test("ca") })
}
