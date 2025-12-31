// Package notifications provides functionality for sending and managing notification challenges
// such as SMS and email verifications for the CSP (Census Service Provider).
package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// ChallengeType represents the type of notification challenge to be sent (SMS or email).
type ChallengeType string

const (
	// SMSChallenge is a challenge to be sent by SMS.
	SMSChallenge ChallengeType = "sms"
	// EmailChallenge is a challenge to be sent by email.
	EmailChallenge ChallengeType = "email"
)

var (
	// ErrInvalidNotificationInputs is returned when the notification challenge
	// is trying to be created with invalid parameters.
	ErrInvalidNotificationInputs = fmt.Errorf("missing required parameters")
	// ErrInvalidNotificationType is returned when the notification challenge
	// is trying to be created with an invalid type.
	ErrInvalidNotificationType = fmt.Errorf("invalid notification type")
	// ErrCreateNotification is returned when the notification challenge could
	// not be created.
	ErrCreateNotification = fmt.Errorf("error creating notification")
	// ErrInvalidNotificationService is returned when the notification
	// challenge is trying to be sent with an invalid service
	ErrInvalidNotificationService = fmt.Errorf("invalid notification service")
)

type OrganizationMeta struct {
	Name string
	Logo string
}

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

// Valid methid checks if the notification challenge is valid. A valid
// notification challenge must have a user ID, a bundle ID, a valid type and
// a notification.
func (nc *NotificationChallenge) Valid() bool {
	switch nc.Type {
	case SMSChallenge, EmailChallenge:
		return nc.UserID != nil && nc.BundleID != nil && nc.Notification != nil
	default:
		return false
	}
}

// Send sends the notification challenge using the provided notification
// service. It returns an error if the notification could not be sent.
func (nc *NotificationChallenge) Send(ctx context.Context, service notifications.NotificationService) error {
	if nc == nil || !nc.Valid() {
		return ErrInvalidNotificationInputs
	}
	if service == nil {
		return ErrInvalidNotificationService
	}

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
func NewNotificationChallenge(
	cType ChallengeType,
	lang string,
	uID, bID internal.HexBytes,
	to, code string,
	orgMeta OrganizationMeta,
	remainingTime string,
) (
	*NotificationChallenge, error,
) {
	if uID == nil || bID == nil || to == "" || code == "" {
		return nil, ErrInvalidNotificationInputs
	}
	n, err := mailtemplates.VerifyOTPCodeNotification.Localized(lang).ExecTemplate(struct {
		Code             string
		Organization     string
		OrganizationLogo string
		ExpiryTime       string
	}{code, orgMeta.Name, orgMeta.Logo, remainingTime})
	if err != nil {
		return nil, errors.Join(ErrCreateNotification, err)
	}
	switch cType {
	case EmailChallenge:
		n.ToAddress = to
	case SMSChallenge:
		n.ToNumber = to
	default:
		return nil, ErrInvalidNotificationType
	}
	return &NotificationChallenge{
		UserID:       uID,
		BundleID:     bID,
		Notification: n,
		Type:         cType,
	}, nil
}
