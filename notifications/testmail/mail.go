package testmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"

	"github.com/vocdoni/saas-backend/notifications"
)

type TestMailConfig struct {
	FromAddress  string
	SMTPUser     string
	SMTPPassword string
	Host         string
	SMTPPort     int
	APIPort      int
}

type TestMail struct {
	config *TestMailConfig
}

func (tm *TestMail) Init(rawConfig any) error {
	config, ok := rawConfig.(*TestMailConfig)
	if !ok {
		return fmt.Errorf("invalid TestMail configuration")
	}
	tm.config = config
	return nil
}

func (tm *TestMail) SendNotification(_ context.Context, notification *notifications.Notification) error {
	auth := smtp.PlainAuth("", tm.config.SMTPUser, tm.config.SMTPPassword, tm.config.Host)
	smtpAddr := fmt.Sprintf("%s:%d", tm.config.Host, tm.config.SMTPPort)
	msg := []byte("To: " + notification.ToAddress + "\r\n" +
		"Subject: " + notification.Subject + "\r\n" +
		"\r\n" +
		notification.Body + "\r\n")
	return smtp.SendMail(smtpAddr, auth, tm.config.FromAddress, []string{notification.ToAddress}, msg)
}

func (tm *TestMail) FindEmail(ctx context.Context, to string) (string, error) {
	searchEndpoint := fmt.Sprintf("http://%s:%d/api/v2/search?kind=to&query=%s", tm.config.Host, tm.config.APIPort, to)
	req, err := http.NewRequestWithContext(ctx, "GET", searchEndpoint, nil)
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
	return mailResults.Items[0].Content.Body, nil
}
