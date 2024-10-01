package notifications

import (
	"os"
	"path/filepath"
	"strings"
)

// MailTemplate represents an email template key. Every email template should
// have a key that identifies it, which is the filename without the extension.
type MailTemplate string

// GetMailTemplates reads the email templates from the specified directory.
// Returns a map with the filename and file absolute path. The filename is the
// key and the path is the value.
func GetMailTemplates(templatesPath string) (map[MailTemplate]string, error) {
	// create a map to store the filename and file content
	htmlFiles := make(map[MailTemplate]string)
	// walk through the directory and read each file
	err := filepath.Walk(templatesPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// only process regular files and files with a ".html" extension
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".html") {
			// get the absolute path of the file
			absPath, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			// remove the ".html" extension from the filename
			filename := strings.TrimSuffix(info.Name(), ".html")
			// store the filename and content in the map
			htmlFiles[MailTemplate(filename)] = absPath
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return htmlFiles, nil
}
