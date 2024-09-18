package notifications

import "context"

type Notification struct {
	ToName    string
	ToAddress string
	ToNumber  string
	Subject   string
	Body      string
	PlainBody string
}

type NotificationService interface {
	Init(conf any) error
	SendNotification(context.Context, *Notification) error
}
