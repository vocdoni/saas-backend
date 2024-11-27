package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/notifications"
)

// availableTemplates is a map that stores the filename and the absolute path
// of the email templates. The filename is the key and the path is the value.
var availableTemplates map[TemplateFile]string

// TemplateFile represents an email template key. Every email template should
// have a key that identifies it, which is the filename without the extension.
type TemplateFile string

// MailTemplate struct represents an email template. It includes the file key
// and the notification placeholder to be sent. The file key is the filename
// of the template without the extension. The notification placeholder includes
// the plain body template to be used as a fallback for email clients that do
// not support HTML, and the mail subject.
type MailTemplate struct {
	File        TemplateFile
	Placeholder notifications.Notification
	WebAppURI   string
}

// Available function returns the available email templates. It returns a map
// with the filename and the absolute path of the email templates. The filename
// is the key and the path is the value.
func Available() map[TemplateFile]string {
	return availableTemplates
}

// Load function reads the email templates from embedded assets. It reads the
// html files from the "assets" directory and stores the filename and the file
// path in the availableTemplates map. It returns an error if the directory
// could not be read or if the files could not be read.
func Load() error {
	// reset the map to store the filename and file paths
	availableTemplates = make(map[TemplateFile]string)
	// read files from embedded assets
	entries, err := root.Assets.ReadDir("assets")
	if err != nil {
		return err
	}
	// walk through the directory and read each file
	for _, entry := range entries {
		// only process regular files and files with a ".html" extension
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".html") {
			// store the filename and the path in the map
			name := strings.TrimSuffix(entry.Name(), ".html")
			availableTemplates[TemplateFile(name)] = "assets/" + entry.Name()
		}
	}
	return nil
}

// ExecTemplate method checks if the template file exists in the available
// mail templates and if it does, it executes the template with the data
// provided. If it doesn't exist, it returns an error. If the plain body
// placeholder is not empty, it executes the plain text template with the
// data provided. It returns the notification with the body and plain body
// filled with the data provided.
func (mt MailTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	path, ok := availableTemplates[mt.File]
	if !ok {
		return nil, fmt.Errorf("template not found")
	}
	// create a notification with the plain body placeholder inflated
	n, err := mt.ExecPlain(data)
	if err != nil {
		return nil, err
	}
	// set the mail subject
	n.Subject = mt.Placeholder.Subject
	// parse the html template file
	content, err := root.Assets.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tmpl, err := htmltemplate.New(string(mt.File)).Parse(string(content))
	if err != nil {
		return nil, err
	}
	// inflate the template with the data
	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, data); err != nil {
		return nil, err
	}
	// set the body of the notification
	n.Body = buf.String()
	return n, nil
}

// ExecPlain method executes the plain body placeholder template with the data
// provided. If the placeholder plain body is not empty, it executes the plain
// text template with the data provided. If it is empty, just returns an empty
// notification. It resulting notification and an error if the defined template
// could not be executed.
//
// This method also allows to notifications services that do not support HTML
// emails to use a mail template.
func (mt MailTemplate) ExecPlain(data any) (*notifications.Notification, error) {
	n := &notifications.Notification{}
	if mt.Placeholder.PlainBody != "" {
		// parse the placeholder plain body template
		tmpl, err := texttemplate.New("plain").Parse(mt.Placeholder.PlainBody)
		if err != nil {
			return nil, err
		}
		// inflate the template with the data
		buf := new(bytes.Buffer)
		if err := tmpl.Execute(buf, data); err != nil {
			return nil, err
		}
		// return the notification with the plain body filled with the data
		n.PlainBody = buf.String()
	}
	return n, nil
}
