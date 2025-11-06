package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// These regexes are used to extract verification codes from emails
var (
	verificationCodeRgx *regexp.Regexp
	passwordResetRgx    *regexp.Regexp
)

func init() {
	// create a regex to find the verification code in the email
	codeRgx := fmt.Sprintf(`(.{%d})`, apicommon.VerificationCodeLength*2)
	// load the email templates
	if err := mailtemplates.Load(); err != nil {
		panic(err)
	}
	// wrap the mail template execution to force plain body and set the regex
	// needle as the verification code
	testTemplateExec := func(mt mailtemplates.LocalizedTemplate) (*notifications.Notification, error) {
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
	verifyNotification, err := testTemplateExec(mailtemplates.VerifyAccountNotification.Localized("en"))
	if err != nil {
		panic(err)
	}
	// clean the notification body to get only the verification code and
	// compile the regex
	verificationCodeRgx = regexp.MustCompile(strings.Split(verifyNotification.PlainBody, "\n")[0])
	// compose notification with the password reset code regex needle
	passwordResetNotification, err := testTemplateExec(mailtemplates.PasswordResetNotification.Localized("en"))
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
	userInfo := &apicommon.UserInfo{
		Email:     "valid@test.com",
		Password:  "password",
		FirstName: "first",
		LastName:  "last",
	}
	// Using the response directly
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

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
	_, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestVerifyAccountHandler(t *testing.T) {
	// Register a user with short expiration time
	apicommon.VerificationCodeExpiration = 5 * time.Second
	token := testCreateUser(t, testPass)

	// get the user to verify the token works
	user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
	t.Logf("%+v\n", user)
}

func TestRecoverAndResetPassword(t *testing.T) {
	c := qt.New(t)

	// Register a user
	userInfo := &apicommon.UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	resp, code := testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the verification code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	verifyMailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(verifyMailCode) > 1, qt.IsTrue)

	// Verify the user
	verification := &apicommon.UserVerification{
		Email: testEmail,
		Code:  verifyMailCode[1],
	}
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Request password recovery
	recoverInfo := &apicommon.UserInfo{
		Email: testEmail,
	}
	resp, code = testRequest(t, http.MethodPost, "", recoverInfo, usersRecoveryPasswordEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the recovery code from the email
	mailBody, err = testMailService.FindEmail(ctx, testEmail)
	c.Assert(err, qt.IsNil)
	passResetMailCode := passwordResetRgx.FindStringSubmatch(mailBody)
	c.Assert(len(passResetMailCode) > 1, qt.IsTrue)

	// Reset the password
	newPassword := "password2"
	resetPass := &apicommon.UserPasswordReset{
		Email:       testEmail,
		Code:        passResetMailCode[1],
		NewPassword: newPassword,
	}
	resp, code = testRequest(t, http.MethodPost, "", resetPass, usersResetPasswordEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Try to login with the old password (should fail)
	loginInfo := &apicommon.UserInfo{
		Email:    testEmail,
		Password: testPass,
	}
	_, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Try to login with the new password (should succeed)
	loginInfo.Password = newPassword
	resp, code = testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
}

func TestUserWithOrganization(t *testing.T) {
	c := qt.New(t)

	// Create a user
	token := testCreateUser(t, "superpassword123")

	// Get the user to verify the token works
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("%s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, token)

	// Get the organization
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("%s\n", resp)
}

func TestResendVerificationCodeHandler(t *testing.T) {
	c := qt.New(t)

	// Test invalid body
	resp, code := testRequest(t, http.MethodPost, "", "invalid body", verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40004")

	// Test empty email
	verification := &apicommon.UserVerification{
		Email: "",
	}
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40005")

	// Test non-existent user
	verification.Email = "nonexistent@test.com"
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
	c.Assert(string(resp), qt.Contains, "40001")

	// Create a user and verify them to test already verified user scenario
	userInfo := &apicommon.UserInfo{
		Email:     "verified@test.com",
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	resp, code = testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the verification code from the email and verify the user
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, "verified@test.com")
	c.Assert(err, qt.IsNil)
	verifyMailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(verifyMailCode) > 1, qt.IsTrue)

	// Verify the user
	verificationCode := &apicommon.UserVerification{
		Email: "verified@test.com",
		Code:  verifyMailCode[1],
	}
	resp, code = testRequest(t, http.MethodPost, "", verificationCode, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test already verified user
	verification.Email = "verified@test.com"
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40015")

	// Create an unverified user for testing active verification code scenarios
	unverifiedUserInfo := &apicommon.UserInfo{
		Email:     "unverified@test.com",
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	resp, code = testRequest(t, http.MethodPost, "", unverifiedUserInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Clear the email from the first registration
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = testMailService.FindEmail(ctx, "unverified@test.com")
	c.Assert(err, qt.IsNil)

	// Test resending verification code with active code (attempts remaining)
	verification.Email = "unverified@test.com"
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response to check expiration is returned
	var resendResp apicommon.UserVerification
	err = json.Unmarshal(resp, &resendResp)
	c.Assert(err, qt.IsNil)
	c.Assert(resendResp.Expiration.After(time.Now()), qt.IsTrue)

	// Verify email was sent
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err = testMailService.FindEmail(ctx, "unverified@test.com")
	c.Assert(err, qt.IsNil)
	c.Assert(mailBody, qt.Not(qt.Equals), "")

	// Test resending multiple times until max attempts
	for i := 2; i < apicommon.VerificationCodeMaxAttempts; i++ {
		resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("attempt %d response: %s", i+1, resp))
	}

	// Test max attempts reached
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40017")

	// Parse the error response to check expiration data is returned
	var errorResp struct {
		Code  int                        `json:"code"`
		Error string                     `json:"error"`
		Data  apicommon.UserVerification `json:"data"`
	}
	err = json.Unmarshal(resp, &errorResp)
	c.Assert(err, qt.IsNil)
	c.Assert(errorResp.Data.Expiration.After(time.Now()), qt.IsTrue)

	// Test expired verification code scenario
	// Temporarily set a very short expiration time
	originalExpiration := apicommon.VerificationCodeExpiration
	apicommon.VerificationCodeExpiration = 100 * time.Millisecond

	// Create another user with short expiration time
	expiredUserInfo := &apicommon.UserInfo{
		Email:     "expired@test.com",
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	}
	resp, code = testRequest(t, http.MethodPost, "", expiredUserInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Wait for the verification code to expire
	time.Sleep(200 * time.Millisecond)

	// Test resending with expired code (should generate new code)
	verification.Email = "expired@test.com"
	resp, code = testRequest(t, http.MethodPost, "", verification, verifyUserCodeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify email was sent with new code
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err = testMailService.FindEmail(ctx, "expired@test.com")
	c.Assert(err, qt.IsNil)
	c.Assert(mailBody, qt.Not(qt.Equals), "")

	// Extract the new verification code to ensure it works
	newVerifyMailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(newVerifyMailCode) > 1, qt.IsTrue)

	// Verify the user can be verified with the new code
	newVerification := &apicommon.UserVerification{
		Email: "expired@test.com",
		Code:  newVerifyMailCode[1],
	}
	resp, code = testRequest(t, http.MethodPost, "", newVerification, verifyUserEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Restore original expiration time
	apicommon.VerificationCodeExpiration = originalExpiration
}
