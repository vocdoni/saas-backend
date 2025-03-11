package notifications

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
	BundleID     internal.HexBytes
	Notification *notifications.Notification
	CreatedAt    time.Time
	Retries      int
	Success      bool
}

// Send sends the notification challenge using the provided notification
// service. It returns an error if the notification could not be sent.
func (nc *NotificationChallenge) Send(ctx context.Context, service notifications.NotificationService) error {
	internalCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := service.SendNotification(internalCtx, nc.Notification); err != nil {
		nc.Success = false
		return err
	}
	nc.Success = true
	return nil
}

// NewNotificationChallenge creates a new notification challenge based on the
// provided parameters. It returns an error if the notification could not be
// created.
func NewNotificationChallenge(challengeType ChallengeType, uID, bID internal.HexBytes, to, code string) (
	*NotificationChallenge, error,
) {
	if uID == nil || bID == nil || to == "" || code == "" {
		return nil, fmt.Errorf("missing required parameters")
	}
	n, err := mailtemplates.VerifyOTPCodeNotification.ExecTemplate(struct {
		Code string
	}{code})
	if err != nil {
		return nil, err
	}
	switch challengeType {
	case EmailChallenge:
		n.ToAddress = to
	case SMSChallenge:
		n.ToNumber = to
	default:
		return nil, fmt.Errorf("invalid notification type")
	}
	return &NotificationChallenge{
		UserID:       uID,
		BundleID:     bID,
		Notification: n,
		Type:         challengeType,
	}, nil
}
