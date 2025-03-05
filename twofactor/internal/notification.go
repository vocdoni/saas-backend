package internal

import (
	"context"
	"time"
)

// NotificationProvider defines the interface for sending notifications
type NotificationProvider interface {
	// SendNotification sends a notification to the specified contact
	SendNotification(ctx context.Context, notification *Notification) error
}

// Notification represents a notification to be sent
type Notification struct {
	// ToAddress is the email address to send to
	ToAddress string

	// ToNumber is the phone number to send to
	ToNumber string

	// Subject is the subject of the notification
	Subject string

	// Body is the HTML body of the notification
	Body string

	// PlainBody is the plain text body of the notification
	PlainBody string
}

// NotificationQueue manages the sending of notifications with throttling and retries
type NotificationQueue interface {
	// Add adds a notification to the queue
	Add(userID UserID, electionID ElectionID, contact string, challenge string) error

	// Start starts processing the queue
	Start()

	// Stop stops processing the queue
	Stop()
}

// NotificationConfig contains configuration for notification providers
type NotificationConfig struct {
	// ThrottlePeriod is the time to wait between sending notifications
	ThrottlePeriod time.Duration

	// MaxRetries is the maximum number of retries for failed notifications
	MaxRetries int

	// TTL is the time-to-live for notifications in the queue
	TTL time.Duration
}

// NewNotificationConfig creates a new notification configuration with default values
func NewNotificationConfig() *NotificationConfig {
	return &NotificationConfig{
		ThrottlePeriod: 500 * time.Millisecond,
		MaxRetries:     10,
		TTL:            10 * time.Minute,
	}
}

// SendChallengeFunc is a function that sends a challenge to a contact
type SendChallengeFunc func(contact string, challenge string) error
