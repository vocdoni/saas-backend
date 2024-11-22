package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	texttemplate "text/template"

	"github.com/vocdoni/saas-backend/notifications"
)

// AvailableTemplates is a map that stores the filename and the absolute path
// of the email templates. The filename is the key and the path is the value.
var AvailableTemplates map[TemplateFile]string

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

// Load function reads the email templates from the specified directory.
// Returns a map with the filename and file absolute path. The filename is
// the key and the path is the value.
func Load(path string) error {
	// create a map to store the filename and file content
	htmlFiles := make(map[TemplateFile]string)
	// walk through the directory and read each file
	if err := filepath.Walk(path, func(fPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// only process regular files and files with a ".html" extension
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".html") {
			// get the absolute path of the file
			absPath, err := filepath.Abs(fPath)
			if err != nil {
				return err
			}
			// remove the ".html" extension from the filename
			filename := strings.TrimSuffix(info.Name(), ".html")
			// store the filename and content in the map
			htmlFiles[TemplateFile(filename)] = absPath
		}
		return nil
	}); err != nil {
		return err
	}
	AvailableTemplates = htmlFiles
	return nil
}

// ExecTemplate method checks if the template file exists in the available
// mail templates and if it does, it executes the template with the data
// provided. If it doesn't exist, it returns an error. If the plain body
// placeholder is not empty, it executes the plain text template with the
// data provided. It returns the notification with the body and plain body
// filled with the data provided.
func (mt MailTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	path, ok := AvailableTemplates[mt.File]
	if !ok {
		return nil, fmt.Errorf("template not found")
	}
	// create a new notification with the subject and plain body of the
	// template placeholder
	n := &notifications.Notification{
		Subject:   mt.Placeholder.Subject,
		PlainBody: mt.Placeholder.PlainBody,
	}
	// execute the template with the data provided parse the template
	tmpl, err := htmltemplate.ParseFiles(path)
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
	// if the plain body is not empty, execute the template with the data
	// provided
	if n.PlainBody != "" {
		tmpl, err := texttemplate.New("plain").Parse(n.PlainBody)
		if err != nil {
			return nil, err
		}
		buf := new(bytes.Buffer)
		if err := tmpl.Execute(buf, data); err != nil {
			return nil, err
		}
		n.PlainBody = buf.String()
	}
	return n, nil
}
