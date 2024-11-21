package notifications

import (
	"bytes"
	"context"
	htmltemplate "html/template"
	texttemplate "text/template"
)

// Notification represents a notification to be sent, it can be an email or an
// SMS. It contains the recipient's name, address, number, the subject and the
// body of the message. The recipient's name and address are used for emails,
// while the recipient's number is used for SMS. The EnableTracking flag
// indicates if the links that the notification contains should be tracked or
// not.
type Notification struct {
	ToName         string
	ToAddress      string
	ToNumber       string
	Subject        string
	Body           string
	PlainBody      string
	EnableTracking bool
}

// ExecTemplate method fills the plain body and the body of the notification
// with the data provided. It executes the plain body template first and only
// if it is not empty. Then, it executes the template with the path provided
// and the data. The path is the absolute path to the template file. If the
// path is empty, the method skips the template execution. It returns an error
// if the plain body or the template could not be executed.
func (n *Notification) ExecTemplate(path string, data any) error {
	// if the plain body is not empty, execute the template with the data
	// provided
	if n.PlainBody != "" {
		tmpl, err := texttemplate.New("plain").Parse(n.PlainBody)
		if err != nil {
			return err
		}
		buf := new(bytes.Buffer)
		if err := tmpl.Execute(buf, data); err != nil {
			return err
		}
		n.PlainBody = buf.String()
		n.Body = n.PlainBody
	}
	// if the path is not empty, execute the template with the data provided
	if path != "" {
		// parse the template
		tmpl, err := htmltemplate.ParseFiles(path)
		if err != nil {
			return err
		}
		// inflate the template with the data
		buf := new(bytes.Buffer)
		if err := tmpl.Execute(buf, data); err != nil {
			return err
		}
		// set the body of the notification
		n.Body = buf.String()
	}
	return nil
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
