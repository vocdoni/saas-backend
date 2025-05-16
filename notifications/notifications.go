// Package notifications provides functionality for sending notifications via different channels
// such as email and SMS, with support for various notification services.
package notifications

import "context"

// NotificationType represents the type of notification to be sent (email or SMS).
type NotificationType string

const (
	// Email represents an email notification type.
	Email NotificationType = "email"
	// SMS represents an SMS notification type.
	SMS NotificationType = "sms"
)

// Notification represents a notification to be sent, it can be an email or an
// SMS. It contains the recipient's name, address, number, the subject and the
// body of the message. The recipient's name and address are used for emails,
// while the recipient's number is used for SMS. The EnableTracking flag
// indicates if the links that the notification contains should be tracked or
// not.
type Notification struct {
	ToName         string `json:"toName"`
	ToAddress      string `json:"toAddress"`
	ToNumber       string `json:"toNumber"`
	Subject        string `json:"subject"`
	Body           string `json:"body"`
	PlainBody      string `json:"plainBody"`
	EnableTracking bool   `json:"enableTracking"`
	ReplyTo        string `json:"replyTo"`
}

// NotificationService is the interface that must be implemented by any
// notification service. It contains the methods New and SendNotification.
// Init is used to initialize the service with the configuration, and
// SendNotification is used to send a notification.
type NotificationService interface {
	// New initializes the notification service with the configuration. Each
	// service implementation can have its own configuration type, which is
	// passed as an argument to this method and must be casted to the correct
	// type inside the method.
	New(conf any) error
	// SendNotification sends a notification to the recipient. The notification
	// contains the recipient's name, address, number, the subject and the body
	// of the message. This method cannot be blocking, so it must return an
	// error if the notification could not be sent or if the context is done.
	SendNotification(context.Context, *Notification) error
}
