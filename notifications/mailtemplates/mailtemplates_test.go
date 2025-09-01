package mailtemplates

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
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
		expectedTemplates := []TemplateFile{
			"verification_account",
			"verification_code_otp",
			"forgot_password",
			"invite_admin",
			"support",
			"members_import_done",
			"welcome",
		}

		for _, templateFile := range expectedTemplates {
			_, exists := available[templateFile]
			c.Assert(exists, qt.IsTrue, qt.Commentf("template %s should be available", templateFile))
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
			c.Assert(path, qt.Not(qt.Equals), "")
			c.Assert(path, qt.Contains, "assets/")
			c.Assert(path, qt.Contains, ".html")
		}
	})
}

func TestVerifyAccountNotification(t *testing.T) {
	c := qt.New(t)

	// Load templates first
	err := Load()
	c.Assert(err, qt.IsNil)

	template := VerifyAccountNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "verification_account")
		c.Assert(template.Placeholder.Subject, qt.Equals, "Vocdoni verification code")
		c.Assert(template.WebAppURI, qt.Equals, "/account/verify")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "Your Vocdoni verification code is:")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Code}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Link}}")
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
		c.Assert(notification.Subject, qt.Equals, "Vocdoni verification code")

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
		c.Assert(notification.Subject, qt.Equals, "Vocdoni verification code")

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

	template := PasswordResetNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "forgot_password")
		c.Assert(template.Placeholder.Subject, qt.Equals, "Vocdoni password reset")
		c.Assert(template.WebAppURI, qt.Equals, "/account/password/reset")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "Your Vocdoni password reset code is:")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Code}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Link}}")
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

	template := InviteNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "invite_admin")
		c.Assert(template.Placeholder.Subject, qt.Equals, "Vocdoni organization invitation")
		c.Assert(template.WebAppURI, qt.Equals, "/account/invite")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "You code to join to '{{.Organization}}' organization is:")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Code}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Link}}")
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

	template := SupportNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "support")
		c.Assert(template.Placeholder.Subject, qt.Contains, "New {{.Type}} Ticket from {{.Email}}: {{.Title}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "You have a new support request:")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Title}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Type}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Email}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Organization}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Description}}")
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

	template := MembersImportCompletionNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "members_import_done")
		c.Assert(template.Placeholder.Subject, qt.Contains, "Members import completed for {{.OrganizationName}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "Hello {{.UserName}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.TotalMembers}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.AddedMembers}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.ErrorCount}}")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.CompletedAt}}")
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

	template := VerifyOTPCodeNotification

	t.Run("TemplateStructure", func(_ *testing.T) {
		c := qt.New(t)

		// Test template field values
		c.Assert(string(template.File), qt.Equals, "verification_code_otp")
		c.Assert(template.Placeholder.Subject, qt.Equals, "Codi de Verificació - Vocdoni")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "El teu codi de verificació és:")
		c.Assert(template.Placeholder.PlainBody, qt.Contains, "{{.Code}}")
	})

	t.Run("ExecTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Test data
		data := struct {
			Code string
		}{
			Code: "987654",
		}

		// Execute template
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// Verify Catalan subject
		c.Assert(notification.Subject, qt.Equals, "Codi de Verificació - Vocdoni")

		// Verify content
		c.Assert(notification.PlainBody, qt.Contains, "987654")
		c.Assert(notification.PlainBody, qt.Not(qt.Contains), "{{.Code}}")
		c.Assert(notification.Body, qt.Contains, "987654")
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
		template := MailTemplate{
			File: "nonexistent_template",
			Placeholder: notifications.Notification{
				Subject:   "Test Subject",
				PlainBody: "Test Body",
			},
		}

		// Execute template should fail
		_, err := template.ExecTemplate(struct{}{})
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "template not found")
	})

	t.Run("ExecPlain_EmptyPlaceholder", func(_ *testing.T) {
		c := qt.New(t)

		// Create template with empty placeholder
		template := MailTemplate{
			File:        "verification_account",
			Placeholder: notifications.Notification{},
		}

		// Execute plain template
		notification, err := template.ExecPlain(struct{}{})
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))
		c.Assert(notification.PlainBody, qt.Equals, "")
		c.Assert(notification.Subject, qt.Equals, "")
	})

	t.Run("ExecPlain_InvalidTemplate", func(_ *testing.T) {
		c := qt.New(t)

		// Create template with invalid template syntax
		template := MailTemplate{
			File: "verification_account",
			Placeholder: notifications.Notification{
				Subject:   "Invalid {{.MissingCloseBrace",
				PlainBody: "Valid body",
			},
		}

		// Execute plain template should fail
		_, err := template.ExecPlain(struct{}{})
		c.Assert(err, qt.Not(qt.IsNil))
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
		notification, err := template.ExecTemplate(data)
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
		templates := []MailTemplate{
			VerifyAccountNotification,
			PasswordResetNotification,
			InviteNotification,
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
			c.Assert(err, qt.IsNil, qt.Commentf("failed for template %s", template.File))
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
		notification, err := template.ExecTemplate(data)
		c.Assert(err, qt.IsNil)
		c.Assert(notification, qt.Not(qt.IsNil))

		// HTML should be escaped in the body
		c.Assert(notification.Body, qt.Not(qt.Contains), "<script>")

		// Plain body should contain the original text
		c.Assert(notification.PlainBody, qt.Contains, "<script>alert('xss')</script>")
	})
}
