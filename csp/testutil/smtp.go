package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/smtp"
)

// SMTP is a wrapper around smtp.Email that adds methods useful for tests
type SMTP struct {
	smtp.Email
	config *SMTPConfig
}

type SMTPConfig struct {
	smtp.Config
	TestAPIPort int
}

var _ notifications.NotificationService = &SMTP{}

const (
	//revive:disable:unsecure-url-scheme
	searchInboxEndpoint   = "http://%s:%d/api/v2/search?kind=to&query=%s"
	deleteMessageEndpoint = "http://%s:%d/api/v1/messages/%s"
)

// New initializes the SMTP email service with the configuration. It sets the
// SMTP auth if the username and password are provided. It returns an error if
// the configuration is invalid or if the from email could not be parsed.
func (sm *SMTP) New(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*SMTPConfig)
	if !ok {
		return fmt.Errorf("invalid SMTP configuration")
	}
	sm.config = config
	return sm.Email.New(&config.Config)
}

// FindEmail searches for an email in the test API service. It sends a GET
// request to the search endpoint with the recipient's email address as a query
// parameter. If a message is found, it returns the body of the first match and
// deletes ONLY that message from the inbox. If no message is found, it returns
// an EOF error. If the request fails, it returns an error with the status code.
// This method is used for testing the email service.
//
// Only the matched message is deleted (by its ID), never the whole inbox. The
// tests share a single MailHog inbox across many concurrent senders (notably the
// CSP notification queue's worker pool, which delivers OTP emails in the
// background). Wiping the entire inbox here would race against those concurrent
// deliveries: clearing all messages on behalf of one recipient could delete
// another recipient's in-flight email before the test waiting on it ever
// retrieves it, making that wait time out with EOF. Deleting only the matched
// message keeps each recipient's lookups independent.
func (sm *SMTP) FindEmail(ctx context.Context, to string) (string, error) {
	searchEndpoint := fmt.Sprintf(searchInboxEndpoint, sm.config.SMTPServer, sm.config.TestAPIPort, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("could not create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not send request: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	//revive:disable:nested-structs
	type mailResponse struct {
		Items []struct {
			ID      string `json:"ID"`
			Content struct {
				Body string `json:"Body"`
			} `json:"Content"`
		} `json:"items"`
	}
	mailResults := mailResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&mailResults); err != nil {
		return "", fmt.Errorf("could not decode response: %v", err)
	}
	if len(mailResults.Items) == 0 {
		return "", io.EOF
	}
	match := mailResults.Items[0]
	return match.Content.Body, sm.deleteMessage(ctx, match.ID)
}

// deleteMessage removes a single message from the test inbox by its ID. A blank
// id is a no-op so a successful match with an unexpected (empty) ID does not
// fail the caller.
func (sm *SMTP) deleteMessage(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	deleteEndpoint := fmt.Sprintf(deleteMessageEndpoint, sm.config.SMTPServer, sm.config.TestAPIPort, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteEndpoint, nil)
	if err != nil {
		return fmt.Errorf("could not create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}
