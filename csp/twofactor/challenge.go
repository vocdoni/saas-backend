package twofactor

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

type ChallengeType string

const (
	SMSChallenge   ChallengeType = "sms"
	EmailChallenge ChallengeType = "email"
)

// NotificationChallenge represents a challenge to be sent to a user for
// verification, either by SMS or email. It contains creation time (to handle
// expiration), retries (to avoid abuse), and success status (to track
// delivery).
type NotificationChallenge struct {
	Type         ChallengeType
	UserID       internal.HexBytes
	ElectionID   internal.HexBytes
	Notification *notifications.Notification
	CreatedAt    time.Time
	Retries      int
	Success      bool
}

// String returns a string representation of the notification challenge.
func (nc *NotificationChallenge) String() string {
	to := nc.Notification.ToAddress
	if nc.Type == SMSChallenge {
		to = nc.Notification.ToNumber
	}
	return fmt.Sprintf("(To:%s)[%s]", to, nc.Notification.PlainBody)
}

// Send sends the notification challenge using the provided notification
// service. It returns an error if the notification could not be sent.
func (nc *NotificationChallenge) Send(service notifications.NotificationService) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := service.SendNotification(ctx, nc.Notification); err != nil {
		return err
	}
	nc.Success = true
	return nil
}

// NewNotificationChallenge creates a new notification challenge based on the
// provided parameters. It returns an error if the notification could not be
// created.
func NewNotificationChallenge(challengeType ChallengeType, userID, electionID internal.HexBytes, contact, optCode string) (*NotificationChallenge, error) {
	n, err := mailtemplates.VerifyOTPCodeNotification.ExecTemplate(struct {
		Code string
	}{optCode})
	if err != nil {
		return nil, err
	}
	switch challengeType {
	case EmailChallenge:
		n.ToAddress = contact
	case SMSChallenge:
		n.ToNumber = contact
	default:
		return nil, fmt.Errorf("invalid notification type")
	}
	return &NotificationChallenge{
		UserID:       userID,
		ElectionID:   electionID,
		Notification: n,
		Type:         challengeType,
	}, nil
}
