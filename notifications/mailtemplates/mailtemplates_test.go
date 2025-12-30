package mailtemplates

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestMailTemplateLoading(t *testing.T) {
	t.Run("Load", func(_ *testing.T) {
		c := qt.New(t)

		// Test loading mail templates
		err := Load()
		c.Assert(err, qt.IsNil)

		// Test that templates are available after loading
		available := Available()
		c.Assert(available, qt.Not(qt.IsNil))
		c.Assert(len(available) > 0, qt.IsTrue)

		// Check that expected template files are loaded
		expectedTemplates := []TemplateKey{
			"verification_account",
			"verification_code_otp",
			"forgot_password",
			"invite_admin",
			"support",
			"members_import_done",
		}

		for _, templateFile := range expectedTemplates {
			for _, lang := range []string{"en", "es", "ca"} {
				_, exists := available[TemplateKey(string(templateFile)+"_"+lang)]
				c.Assert(exists, qt.IsTrue, qt.Commentf("template %s should be available", templateFile))
			}
		}
	})

	t.Run("Available", func(_ *testing.T) {
		c := qt.New(t)

		// Load templates first
		err := Load()
		c.Assert(err, qt.IsNil)

		// Test Available function
		available := Available()
		c.Assert(available, qt.Not(qt.IsNil))

		// Verify returned map structure
		for templateFile, path := range available {
			c.Assert(string(templateFile), qt.Not(qt.Equals), "")
			c.Assert(path.HTMLFile, qt.Contains, "assets/")
			c.Assert(path.HTMLFile, qt.Contains, ".html")
		}
	})
}

func TestVerifyAccountNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := VerifyAccountNotification.Localized("en")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)
		c.Assert(plaintext.Subject, qt.Equals, "Verify Your Vocdoni Account")
		c.Assert(plaintext.Body, qt.Contains, "Your Vocdoni verification code is:")
		c.Assert(plaintext.Body, qt.Contains, "{{.Code}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Link}}")
	})

	t.Run("ExecPlain", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code string
			Link string
		}{
			Code: "123456",
			Link: "https://example.com/verify?token=abc123",
		}

		// Execute plain template
		notification, err := template.ExecPlain(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify subject is populated
		c.Assert(notification.Subject, qt.Equals, "Verify Your Vocdoni Account")

		// Verify plain body is populated with data
		c.Assert(notification.PlainBody, qt.Contains, "123456")
		c.Assert(notification.PlainBody, qt.Contains, "https://example.com/verify?token=abc123")
		c.Assert(notification.PlainBody, qt.Not(qt.Contains), "{{.Code}}")
		c.Assert(notification.PlainBody, qt.Not(qt.Contains), "{{.Link}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code string
			Link string
		}{
			Code: "123456",
			Link: "https://example.com/verify?token=abc123",
		}

		// Execute HTML template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify both HTML body and plain body are populated
		c.Assert(notification.Body, qt.Not(qt.Equals), "")
		c.Assert(notification.PlainBody, qt.Not(qt.Equals), "")
		c.Assert(notification.Subject, qt.Equals, "Verify Your Vocdoni Account")

		// HTML body should contain the data
		c.Assert(notification.Body, qt.Contains, "123456")
		c.Assert(notification.Body, qt.Contains, "https://example.com/verify?token=abc123")
	})
}

func TestPasswordResetNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := PasswordResetNotification.Localized("en")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)
		c.Assert(plaintext.Subject, qt.Equals, "Vocdoni password reset")
		c.Assert(plaintext.Body, qt.Contains, "Your Vocdoni password reset code is:")
		c.Assert(plaintext.Body, qt.Contains, "{{.Code}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Link}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code string
			Link string
		}{
			Code: "RESET123",
			Link: "https://example.com/reset?token=def456",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify content
		c.Assert(notification.Subject, qt.Equals, "Vocdoni password reset")
		c.Assert(notification.PlainBody, qt.Contains, "RESET123")
		c.Assert(notification.PlainBody, qt.Contains, "https://example.com/reset?token=def456")
		c.Assert(notification.Body, qt.Contains, "RESET123")
	})
}

func TestInviteNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := InviteNotification.Localized("en")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)
		c.Assert(plaintext.Subject, qt.Equals, "Vocdoni organization invitation")
		c.Assert(plaintext.Body, qt.Contains, "Your code to join '{{.Organization}}' organization is:")
		c.Assert(plaintext.Body, qt.Contains, "{{.Code}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Link}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code         string
			Link         string
			Organization string
		}{
			Code:         "INVITE789",
			Link:         "https://example.com/invite?token=ghi789",
			Organization: "Test Organization",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify content
		c.Assert(notification.Subject, qt.Equals, "Vocdoni organization invitation")
		c.Assert(notification.PlainBody, qt.Contains, "INVITE789")
		c.Assert(notification.PlainBody, qt.Contains, "Test Organization")
		c.Assert(notification.PlainBody, qt.Contains, "https://example.com/invite?token=ghi789")
		c.Assert(notification.Body, qt.Contains, "INVITE789")
		c.Assert(notification.Body, qt.Contains, "Test Organization")
	})
}

func TestSupportNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := SupportNotification.Localized("en")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)
		c.Assert(plaintext.Subject, qt.Contains, "New {{.Type}} Ticket from {{.Email}}: {{.Title}}")
		c.Assert(plaintext.Body, qt.Contains, "You have a new support request:")
		c.Assert(plaintext.Body, qt.Contains, "{{.Title}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Type}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Email}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Organization}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.Description}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Type         string
			Email        string
			Title        string
			Description  string
			Organization string
		}{
			Type:         "Bug Report",
			Email:        "user@example.com",
			Title:        "Login Issue",
			Description:  "I cannot log in to my account",
			Organization: "Test Org",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify subject
		expectedSubject := "New Bug Report Ticket from user@example.com: Login Issue"
		c.Assert(notification.Subject, qt.Equals, expectedSubject)

		// Verify plain body content
		c.Assert(notification.PlainBody, qt.Contains, "Bug Report")
		c.Assert(notification.PlainBody, qt.Contains, "user@example.com")
		c.Assert(notification.PlainBody, qt.Contains, "Login Issue")
		c.Assert(notification.PlainBody, qt.Contains, "I cannot log in to my account")
		c.Assert(notification.PlainBody, qt.Contains, "Test Org")

		// Verify HTML body
		c.Assert(notification.Body, qt.Contains, "Bug Report")
		c.Assert(notification.Body, qt.Contains, "user@example.com")
	})
}

func TestMembersImportCompletionNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := MembersImportCompletionNotification.Localized("en")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)

		// Test template field values
		c.Assert(plaintext.Subject, qt.Contains, "Members import completed for {{.OrganizationName}}")
		c.Assert(plaintext.Body, qt.Contains, "Hello {{.UserName}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.TotalMembers}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.AddedMembers}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.ErrorCount}}")
		c.Assert(plaintext.Body, qt.Contains, "{{.CompletedAt}}")
	})

	t.Run("ExecTemplate_Success", func(_ *testing.T) {
		c := qt.New(t)

		// Test data for successful import
		data := struct {
			UserName         string
			OrganizationName string
			Link             string
			TotalMembers     int
			AddedMembers     int
			ErrorCount       int
			Errors           []string
			CompletedAt      string
		}{
			UserName:         "John Doe",
			OrganizationName: "Test Organization",
			Link:             "https://example.com/complex",
			TotalMembers:     100,
			AddedMembers:     100,
			ErrorCount:       0,
			Errors:           nil,
			CompletedAt:      "2023-10-03 12:00:00",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify subject
		expectedSubject := "Members import completed for Test Organization"
		c.Assert(notification.Subject, qt.Equals, expectedSubject)

		// Verify content
		c.Assert(notification.PlainBody, qt.Contains, "John Doe")
		c.Assert(notification.PlainBody, qt.Contains, "Test Organization")
		c.Assert(notification.PlainBody, qt.Contains, "100")
		c.Assert(notification.PlainBody, qt.Contains, "2023-10-03 12:00:00")
	})

	t.Run("ExecTemplate_WithErrors", func(_ *testing.T) {
		c := qt.New(t)

		// Test data with errors
		data := struct {
			UserName         string
			OrganizationName string
			Link             string
			TotalMembers     int
			AddedMembers     int
			ErrorCount       int
			Errors           []string
			CompletedAt      string
		}{
			UserName:         "Jane Smith",
			OrganizationName: "Error Test Org",
			Link:             "https://example.com/complex",
			TotalMembers:     50,
			AddedMembers:     45,
			ErrorCount:       5,
			Errors:           []string{"Invalid email format", "Duplicate entry", "Missing phone number"},
			CompletedAt:      "2023-10-03 13:00:00",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify error details are included
		c.Assert(notification.PlainBody, qt.Contains, "Invalid email format")
		c.Assert(notification.PlainBody, qt.Contains, "Duplicate entry")
		c.Assert(notification.PlainBody, qt.Contains, "Missing phone number")
		c.Assert(notification.PlainBody, qt.Contains, "5")
	})
}

func TestVerifyOTPCodeNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := VerifyOTPCodeNotification.Localized("ca")

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)
		plaintext, err := template.Plaintext()
		c.Assert(err, qt.IsNil)

		// Test template field values
		c.Assert(plaintext.Subject, qt.Equals, "Codi de Verificació - {{.Organization}}")
		c.Assert(plaintext.Body, qt.Contains, "El teu codi de verificació és:")
		c.Assert(plaintext.Body, qt.Contains, "{{.Code}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code             string
			Organization     string
			OrganizationLogo string
			ExpiryTime       string
		}{
			Code:             "987654",
			Organization:     "TestOrgName",
			OrganizationLogo: "https://example.com/logo.png",
			ExpiryTime:       "00h:05m:00s",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify Catalan subject
		c.Assert(notification.Subject, qt.Equals, "Codi de Verificació - TestOrgName")

		// Verify content
		c.Assert(notification.PlainBody, qt.Contains, "987654")
		c.Assert(notification.PlainBody, qt.Not(qt.Contains), "{{.Code}}")
		c.Assert(notification.Body, qt.Contains, "987654")
		c.Assert(notification.Body, qt.Not(qt.Contains), "{{.OrganizationLogo}}")
		c.Assert(notification.Body, qt.Contains, "https://example.com/logo.png")
		c.Assert(notification.Body, qt.Not(qt.Contains), "{{.Organization}}")
		c.Assert(notification.Body, qt.Contains, "TestOrgName")
	})
}

func TestMailTemplateExecution(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	t.Run("ExecTemplate_NonexistentTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Create template with nonexistent file
		template := LocalizedTemplate{
			HTMLFile: "assets/nonexistent_template_en.html",
			YAMLFile: "assets/nonexistent_template_en.yaml",
		}

		// Execute template should fail
		_, err := template.ExecTemplate(struct{}{})
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "file does not exist")
	})

	t.Run("ExecTemplate_WithComplexData", func(_ *testing.T) {
		c := qt.New(t)

		template := MembersImportCompletionNotification

		// Test with complex nested data
		data := struct {
			UserName         string
			OrganizationName string
			Link             string
			TotalMembers     int
			AddedMembers     int
			ErrorCount       int
			Errors           []string
			CompletedAt      time.Time
		}{
			UserName:         "Complex User",
			OrganizationName: "Complex Org",
			Link:             "https://example.com/complex",
			TotalMembers:     1000,
			AddedMembers:     950,
			ErrorCount:       50,
			Errors:           []string{"Error 1", "Error 2", "Error 3"},
			CompletedAt:      time.Date(2023, 10, 3, 12, 0, 0, 0, time.UTC),
		}

		// Execute template
		notification, err := template.Localized("en").ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))
		c.Assert(notification.PlainBody, qt.Contains, "Complex User")
		c.Assert(notification.PlainBody, qt.Contains, "1000")
		c.Assert(notification.PlainBody, qt.Contains, "Error 1")
	})
}

func TestMailTemplateIntegration(t *testing.T) {
	t.Run("CompleteWorkflow", func(_ *testing.T) {
		c := qt.New(t)

		// Load templates
		err := Load()
		c.Assert(err, qt.IsNil)

		// Verify templates are available
		available := Available()
		c.Assert(len(available) > 0, qt.IsTrue)

		// Test multiple template executions
		templates := []LocalizedTemplate{
			VerifyAccountNotification.Localized("en"),
			PasswordResetNotification.Localized("en"),
			InviteNotification.Localized("en"),
		}

		testData := struct {
			Code         string
			Link         string
			Organization string
		}{
			Code:         "TEST123",
			Link:         "https://test.com/link",
			Organization: "Test Org",
		}

		for _, template := range templates {
			notification, err := template.ExecTemplate(testData)
			c.Assert(err, qt.IsNil, qt.Commentf("failed for template %s", template.HTMLFile))
			c.Assert(notification, qt.Not(qt.IsNil))
			c.Assert(notification.Subject, qt.Not(qt.Equals), "")
			c.Assert(notification.Body, qt.Not(qt.Equals), "")
		}
	})

	t.Run("TemplateSanitization", func(_ *testing.T) {
		c := qt.New(t)

		err := Load()
		c.Assert(err, qt.IsNil)

		template := VerifyAccountNotification

		// Test with potentially dangerous data
		data := struct {
			Code string
			Link string
		}{
			Code: "<script>alert('xss')</script>",
			Link: "javascript:alert('xss')",
		}

		// Execute template
		notification, err := template.Localized("en").ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// HTML should be escaped in the body
		c.Assert(notification.Body, qt.Not(qt.Contains), "<script>")

		// Plain body should contain the original text
		c.Assert(notification.PlainBody, qt.Contains, "<script>alert('xss')</script>")
	})
}
