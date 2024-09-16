package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

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
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   mustMarshal(ErrGenericInternalServerError),
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
	// get the verification code from the email
	mailBody, err := testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	mailCode := strings.TrimPrefix(mailBody, VerificationCodeEmailBody)
	// verify the user
	verification := mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  mailCode,
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
	c.Assert(resp.StatusCode, qt.Equals, http.StatusUnauthorized)
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
	// try to recover the password before verifying the user (should fail)
	jsonRecover := mustMarshal(&UserInfo{
		Email: testEmail,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(usersRecoveryPasswordEndpoint), bytes.NewBuffer(jsonRecover))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusUnauthorized)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// get the verification code from the email
	mailBody, err := testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	verifyMailCode := strings.TrimPrefix(mailBody, VerificationCodeEmailBody)
	// verify the user
	verification := mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  verifyMailCode,
	})
	req, err = http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verification))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// try to recover the password after verifying the user
	req, err = http.NewRequest(http.MethodPost, testURL(usersRecoveryPasswordEndpoint), bytes.NewBuffer(jsonRecover))
	c.Assert(err, qt.IsNil)
	resp, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Body.Close(), qt.IsNil)
	// get the recovery code from the email
	mailBody, err = testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	passResetMailCode := strings.TrimPrefix(mailBody, VerificationCodeEmailBody)
	// reset the password
	newPassword := "password2"
	resetPass := mustMarshal(&UserPasswordReset{
		Email:       testEmail,
		Code:        passResetMailCode,
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
