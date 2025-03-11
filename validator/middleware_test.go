package validator

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidateMiddleware tests the ValidateMiddleware function.
func TestValidateMiddleware(t *testing.T) {
	v := New()

	// Test struct with validation tags
	type TestStruct struct {
		Name     string `json:"name" validate:"required"`
		Email    string `json:"email" validate:"required,email"`
		Password string `json:"password" validate:"required,min=8"`
	}

	// Create a test handler that always succeeds
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Create a middleware that validates the TestStruct
	middleware := v.ValidateMiddleware(TestStruct{})

	// Test valid request
	validData := TestStruct{
		Name:     "John Doe",
		Email:    "john@example.com",
		Password: "password123",
	}
	validJSON, _ := json.Marshal(validData)
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(validJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// Apply the middleware to the test handler
	middleware(testHandler).ServeHTTP(rec, req)

	// Check the response
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if string(body) != "success" {
		t.Errorf("Expected body %q, got %q", "success", string(body))
	}

	// Test invalid request (missing required field)
	invalidData := struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}{
		Email:    "john@example.com",
		Password: "password123",
	}
	invalidJSON, _ := json.Marshal(invalidData)
	req = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(invalidJSON))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	// Apply the middleware to the test handler
	middleware(testHandler).ServeHTTP(rec, req)

	// Check the response
	resp = rec.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	// Test invalid request (invalid email)
	invalidData2 := TestStruct{
		Name:     "John Doe",
		Email:    "invalid-email",
		Password: "password123",
	}
	invalidJSON2, _ := json.Marshal(invalidData2)
	req = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(invalidJSON2))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	// Apply the middleware to the test handler
	middleware(testHandler).ServeHTTP(rec, req)

	// Check the response
	resp = rec.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	// Test invalid request (password too short)
	invalidData3 := TestStruct{
		Name:     "John Doe",
		Email:    "john@example.com",
		Password: "pass",
	}
	invalidJSON3, _ := json.Marshal(invalidData3)
	req = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(invalidJSON3))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	// Apply the middleware to the test handler
	middleware(testHandler).ServeHTTP(rec, req)

	// Check the response
	resp = rec.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	// Test invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	// Apply the middleware to the test handler
	middleware(testHandler).ServeHTTP(rec, req)

	// Check the response
	resp = rec.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}
}
