package notifications

import "context"

type Notification struct {
	To      string
	Subject string
	Body    string
}

type NotificationService interface {
	Init(conf any) error
	SendNotification(context.Context, *Notification) error
}
