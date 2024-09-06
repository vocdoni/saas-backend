package api

import (
	"bytes"
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
