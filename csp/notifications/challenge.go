// Package notifications provides functionality for sending and managing notification challenges
// such as SMS and email verifications for the CSP (Census Service Provider).
package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// ChallengeType represents the type of notification challenge to be sent (SMS or email).
type ChallengeType = notifications.NotificationType

const (
	// SMSChallenge is a challenge to be sent by SMS.
	SMSChallenge = notifications.SMS
	// EmailChallenge is a challenge to be sent by email.
	EmailChallenge = notifications.Email
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

type OrganizationInfo struct {
	Address common.Address
	Name    string
	Logo    string
}

// NotificationChallenge represents a challenge to be sent to a user for
// verification, either by SMS or email. It contains creation time (to handle
// expiration), retries (to avoid abuse), and success status (to track
// delivery).
type NotificationChallenge struct {
	Type         notifications.NotificationType
	UserID       internal.HexBytes
	AnchorID     internal.HexBytes
	OrgAddress   common.Address
	Notification *notifications.Notification
	CreatedAt    time.Time
	// ExpiresAt is the deadline after which the OTP code is considered stale.
	// The queue drops the challenge without sending if this time has passed.
	ExpiresAt time.Time
	Retries   int
	Success   bool
}

// Valid methid checks if the notification challenge is valid. A valid
// notification challenge must have a user ID, an anchor ID, a valid type and
// a notification.
func (nc *NotificationChallenge) Valid() bool {
	switch nc.Type {
	case SMSChallenge, EmailChallenge:
		return nc.UserID != nil && nc.AnchorID != nil && nc.Notification != nil
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
// provided parameters. expiry is how long the code stays valid, shown to the
// voter in words. It returns an error if the notification could not be
// created.
func NewNotificationChallenge(
	cType notifications.NotificationType,
	lang string,
	userID, anchorID internal.HexBytes,
	to, code string,
	orgInfo OrganizationInfo,
	expiry time.Duration,
) (
	*NotificationChallenge, error,
) {
	if userID == nil || anchorID == nil || to == "" || code == "" {
		return nil, ErrInvalidNotificationInputs
	}
	data := mailtemplates.OTPData{
		Code:             code,
		Organization:     orgInfo.Name,
		OrganizationLogo: orgInfo.Logo,
		ExpiryTime:       expiry,
	}
	template := mailtemplates.VerifyOTPCodeNotification.Localized(lang)
	var n *notifications.Notification
	var err error
	switch cType {
	case EmailChallenge:
		if n, err = template.ExecTemplate(data); err == nil {
			n.ToAddress = to
		}
	case SMSChallenge:
		// the SMS is the plain text alone (one UCS-2 segment, no organization name)
		if n, err = template.ExecPlain(data); err == nil {
			n.ToNumber = to
		}
	default:
		return nil, ErrInvalidNotificationType
	}
	if err != nil {
		return nil, errors.Join(ErrCreateNotification, err)
	}
	return &NotificationChallenge{
		OrgAddress:   orgInfo.Address,
		UserID:       userID,
		AnchorID:     anchorID,
		Notification: n,
		Type:         cType,
	}, nil
}
