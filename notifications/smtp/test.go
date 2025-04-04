package smtp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	searchInboxTestEndpoint = "http://%s:%d/api/v2/search?kind=to&query=%s"
	clearInboxTestEndpoint  = "http://%s:%d/api/v1/messages"
)

// FindEmail searches for an email in the test API service. It sends a GET
// request to the search endpoint with the recipient's email address as a query
// parameter. If the email is found, it returns the email body and clears the
// inbox. If the email is not found, it returns an EOF error. If the request
// fails, it returns an error with the status code. This method is used for
// testing the email service.
func (sm *Email) FindEmail(ctx context.Context, to string) (string, error) {
	searchEndpoint := fmt.Sprintf(searchInboxTestEndpoint, sm.config.SMTPServer, sm.config.TestAPIPort, to)
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
	return mailResults.Items[0].Content.Body, sm.clear()
}

func (sm *Email) clear() error {
	clearEndpoint := fmt.Sprintf(clearInboxTestEndpoint, sm.config.SMTPServer, sm.config.TestAPIPort)
	req, err := http.NewRequest(http.MethodDelete, clearEndpoint, nil)
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
