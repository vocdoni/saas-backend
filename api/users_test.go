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
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()

	// Test invalid body
	resp, code := testRequest(t, http.MethodPost, "", "invalid body", usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrMalformedBody)))

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
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrDuplicateConflict.With("user already exists"))))

	// Test empty last name
	userInfo.Email = "valid2@test.com"
	userInfo.LastName = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrMalformedBody.Withf("last name is empty"))))

	// Test empty first name
	userInfo.LastName = "last"
	userInfo.FirstName = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrMalformedBody.Withf("first name is empty"))))

	// Test invalid email
	userInfo.FirstName = "first"
	userInfo.Email = "invalid"
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrEmailMalformed)))

	// Test empty email
	userInfo.Email = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrEmailMalformed)))

	// Test short password
	userInfo.Email = "valid2@test.com"
	userInfo.Password = "short"
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Equals, string(mustMarshal(ErrPasswordTooShort)))

	// Test empty password
	userInfo.Password = ""
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestVerifyAccountHandler(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()

	// Register a user with short expiration time
	VerificationCodeExpiration = 5 * time.Second
	userInfo := &UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	_, code := testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Try to login (should fail)
	loginInfo := &UserInfo{
		Email:    testEmail,
		Password: testPass,
	}
	_, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Get the verification code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	mailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(mailCode) > 1, qt.IsTrue)

	// Wait to expire the verification code
	time.Sleep(VerificationCodeExpiration)

	// Try to verify the user (should fail)
	verification := &UserVerification{
		Email: testEmail,
		Code:  mailCode[1],
	}
	_, code = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Resend the verification code
	_, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Get the new verification code from the email
	mailBody, err = testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	mailCode = verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(mailCode) > 1, qt.IsTrue)

	// Verify the user
	verification.Code = mailCode[1]
	_, code = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Try to verify the user again (should fail)
	_, code = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Try to login again (should succeed)
	_, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
}

func TestRecoverAndResetPassword(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()

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
