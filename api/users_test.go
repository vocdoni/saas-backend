package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

	registerURL := testURL(usersEndpoint)
	testCases := []apiTestCase{
		{
			uri:            registerURL,
			method:         http.MethodPost,
			body:           []byte("invalid body"),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrMalformedBody),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid@test.com",
				Password:  "password",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusOK,
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid@test.com",
				Password:  "password",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusConflict,
			expectedBody:   mustMarshal(ErrDuplicateConflict.With("user already exists")),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid2@test.com",
				Password:  "password",
				FirstName: "first",
				LastName:  "",
			}),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrMalformedBody.Withf("last name is empty")),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid2@test.com",
				Password:  "password",
				FirstName: "",
				LastName:  "last",
			}),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrMalformedBody.Withf("first name is empty")),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "invalid",
				Password:  "password",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrEmailMalformed),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "",
				Password:  "password",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrEmailMalformed),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid2@test.com",
				Password:  "short",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusBadRequest,
			expectedBody:   mustMarshal(ErrPasswordTooShort),
		},
		{
			uri:    registerURL,
			method: http.MethodPost,
			body: mustMarshal(&UserInfo{
				Email:     "valid2@test.com",
				Password:  "",
				FirstName: "first",
				LastName:  "last",
			}),
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, testCase := range testCases {
		req, err := http.NewRequest(testCase.method, testCase.uri, bytes.NewBuffer(testCase.body))
		c.Assert(err, qt.IsNil)

		resp, err := http.DefaultClient.Do(req)
		c.Assert(err, qt.IsNil)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				c.Errorf("error closing response body: %v", err)
			}
		}()
		c.Assert(resp.StatusCode, qt.Equals, testCase.expectedStatus)
		if testCase.expectedBody != nil {
			body, err := io.ReadAll(resp.Body)
			c.Assert(err, qt.IsNil)
			c.Assert(strings.TrimSpace(string(body)), qt.Equals, string(testCase.expectedBody))
		}
	}
}

func TestVerifyAccountHandler(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()
	// register a user with short expiration time
	VerificationCodeExpiration = 5 * time.Second
	jsonUser := mustMarshal(&UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	})
	req, err := http.NewRequest(http.MethodPost, testURL(usersEndpoint), bytes.NewBuffer(jsonUser))
	c.Assert(err, qt.IsNil)
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to login (should fail)
	jsonLogin := mustMarshal(&UserInfo{
		Email:    testEmail,
		Password: testPass,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(jsonLogin))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusUnauthorized)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to verify the user (should fail)
	// get the verification code from the email
	mailBody, err := testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	// get the verification code from the email using the regex
	mailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	// verify the user
	verification := mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  mailCode[1],
	})
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	// wait to expire the verification code
	time.Sleep(VerificationCodeExpiration)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusUnauthorized)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// resend the verification code and verify the user
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserCodeEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// get the verification code from the email
	mailBody, err = testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	mailCode = verificationCodeRgx.FindStringSubmatch(mailBody)
	// verify the user
	verification = mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  mailCode[1],
	})
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to verify the user again (should fail)
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusBadRequest)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to login again
	req, err = http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(jsonLogin))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
}

func TestRecoverAndResetPassword(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()
	// register a user
	jsonUser := mustMarshal(&UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	})
	req, err := http.NewRequest(http.MethodPost, testURL(usersEndpoint), bytes.NewBuffer(jsonUser))
	c.Assert(err, qt.IsNil)
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// verify the user (to be able to recover the password)
	mailBody, err := testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	// create a regex to find the verification code in the email
	verifyMailCode := passwordResetRgx.FindStringSubmatch(mailBody)
	// verify the user
	verification := mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  verifyMailCode[1],
	})
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to recover the password after verifying the user
	jsonRecover := mustMarshal(&UserInfo{
		Email: testEmail,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(usersRecoveryPasswordEndpoint), bytes.NewBuffer(jsonRecover))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// get the recovery code from the email
	mailBody, err = testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	// update the regex to find the recovery code in the email
	passResetMailCode := passwordResetRgx.FindStringSubmatch(mailBody)
	// reset the password
	newPassword := "password2"
	resetPass := mustMarshal(&UserPasswordReset{
		Email:       testEmail,
		Code:        passResetMailCode[1],
		NewPassword: newPassword,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(usersResetPasswordEndpoint), bytes.NewBuffer(resetPass))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to login with the old password (should fail)
	jsonLogin := mustMarshal(&UserInfo{
		Email:    testEmail,
		Password: testPass,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(jsonLogin))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusUnauthorized)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to login with the new password
	jsonLogin = mustMarshal(&UserInfo{
		Email:    testEmail,
		Password: newPassword,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(jsonLogin))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
}
