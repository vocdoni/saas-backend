package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

var verificationCodeRgx, passwordResetRgx *regexp.Regexp

func init() {
	// create a regex to find the verification code in the email
	codeRgx := fmt.Sprintf(`(.{%d})`, VerificationCodeLength*2)
	// load the email templates
	if err := mailtemplates.Load(); err != nil {
		panic(err)
	}
	// wrap the mail template execution to force plain body and set the regex
	// needle as the verification code
	testTemplateExec := func(mt mailtemplates.MailTemplate) (*notifications.Notification, error) {
		n, err := mt.ExecTemplate(struct {
			Code string
			Link string
		}{codeRgx, ""})
		if err != nil {
			return nil, err
		}
		// force plain body
		n.Body = n.PlainBody
		return n, nil
	}
	// compose notification with the verification code regex needle
	verifyNotification, err := testTemplateExec(mailtemplates.VerifyAccountNotification)
	if err != nil {
		panic(err)
	}
	// clean the notification body to get only the verification code and
	// compile the regex
	verificationCodeRgx = regexp.MustCompile(strings.Split(verifyNotification.PlainBody, "\n")[0])
	// compose notification with the password reset code regex needle
	passwordResetNotification, err := testTemplateExec(mailtemplates.PasswordResetNotification)
	if err != nil {
		panic(err)
	}
	// clean the notification body to get only the password reset code and
	passwordResetRgx = regexp.MustCompile(strings.Split(passwordResetNotification.PlainBody, "\n")[0])
}

func TestRegisterHandler(t *testing.T) {
	c := qt.New(t)

	// Test invalid body
	resp, code := testRequest(t, http.MethodPost, "", "invalid body", usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	// Just check that the response contains the expected error code
	c.Assert(string(resp), qt.Contains, "40004")

	// Test valid registration
	userInfo := &UserInfo{
		Email:     "valid@test.com",
		Password:  "password",
		FirstName: "first",
		LastName:  "last",
	}
	_, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Test duplicate user
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusConflict)
	// Just check that the response contains the expected error code
	c.Assert(string(resp), qt.Contains, "40901")

	// Test empty last name
	userInfo.Email = "valid2@test.com"
	userInfo.LastName = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "last name is empty")

	// Test empty first name
	userInfo.LastName = "last"
	userInfo.FirstName = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "first name is empty")

	// Test invalid email
	userInfo.FirstName = "first"
	userInfo.Email = "invalid"
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "invalid email format")

	// Test empty email
	userInfo.Email = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "invalid email format")

	// Test short password
	userInfo.Email = "valid2@test.com"
	userInfo.Password = "short"
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "password must be at least 8 characters")

	// Test empty password
	userInfo.Password = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestVerifyAccountHandler(t *testing.T) {
	c := qt.New(t)

	// Register a user with short expiration time
	VerificationCodeExpiration = 5 * time.Second
	token := testCreateUser(t, testPass)

	// get the user to verify the token works
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("%s\n", resp)
}

func TestRecoverAndResetPassword(t *testing.T) {
	c := qt.New(t)

	// Register a user
	userInfo := &UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	_, code := testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Get the verification code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	verifyMailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(verifyMailCode) > 1, qt.IsTrue)

	// Verify the user
	verification := &UserVerification{
		Email: testEmail,
		Code:  verifyMailCode[1],
	}
	_, code = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Request password recovery
	recoverInfo := &UserInfo{
		Email: testEmail,
	}
	_, code = testRequest(t, http.MethodPost, "", recoverInfo, usersRecoveryPasswordEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Get the recovery code from the email
	mailBody, err = testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	passResetMailCode := passwordResetRgx.FindStringSubmatch(mailBody)
	c.Assert(len(passResetMailCode) > 1, qt.IsTrue)

	// Reset the password
	newPassword := "password2"
	resetPass := &UserPasswordReset{
		Email:       testEmail,
		Code:        passResetMailCode[1],
		NewPassword: newPassword,
	}
	_, code = testRequest(t, http.MethodPost, "", resetPass, usersResetPasswordEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Try to login with the old password (should fail)
	loginInfo := &UserInfo{
		Email:    testEmail,
		Password: testPass,
	}
	_, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Try to login with the new password (should succeed)
	loginInfo.Password = newPassword
	_, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
}

func TestUserWithOrganization(t *testing.T) {
	c := qt.New(t)

	// Create a user
	token := testCreateUser(t, "superpassword123")

	// Get the user to verify the token works
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("%s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, token)

	// Get the organization
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("%s\n", resp)
}
