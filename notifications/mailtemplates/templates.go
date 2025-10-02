package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"path/filepath"
	"strings"
	texttemplate "text/template"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/notifications"
	"gopkg.in/yaml.v3"
)

// availableTemplates is a map that stores the loaded templates
var availableTemplates map[TemplateFile]LocalizedTemplate

// TemplateFile represents a template key. Every template should
// have a key that identifies it, which is the filename without the extension.
type TemplateFile string

// MailTemplate represents a mail template with multiple language variants
type MailTemplate struct {
	File      string // Base filename (e.g., "verification_account")
	WebAppURI string // App-specific URI for links
}

// TemplatePlainText represents the YAML content
type TemplatePlainText struct {
	Subject string `yaml:"subject"`
	Body    string `yaml:"body"`
}

// Localized returns the appropriate template for the given language.
// Falls back to English if the language is not found.
func (mt MailTemplate) Localized(lang string) LocalizedTemplate {
	templateKey := mt.File + "_" + lang

	if template, exists := availableTemplates[TemplateFile(templateKey)]; exists {
		return template
	}

	// Fallback to English
	englishKey := mt.File + "_en"
	if template, exists := availableTemplates[TemplateFile(englishKey)]; exists {
		return template
	}

	// Return empty template if nothing found
	return LocalizedTemplate{}
}

// LocalizedTemplate struct represents an email template. It includes the HTML file path
// and the subject and plain body defined in the YAML.
type LocalizedTemplate struct {
	HTMLFile  string
	PlainText TemplatePlainText
}

// Available function returns the available email templates. It returns a map
// with the template key and the LocalizedTemplate struct.
func Available() map[TemplateFile]LocalizedTemplate {
	return availableTemplates
}

// Load function reads the email templates from embedded assets. It reads both
// HTML and YAML files from the "assets" directory and creates MailTemplate
// structs for each HTML+YAML pair. It returns an error if the directory
// could not be read or if the files could not be parsed.
func Load() error {
	// reset the map to store the templates
	availableTemplates = make(map[TemplateFile]LocalizedTemplate)

	// read files from embedded assets
	entries, err := root.Assets.ReadDir("assets")
	if err != nil {
		return err
	}

	// Group files by base name and language
	htmlFiles := make(map[string]string) // "verification_account_en" -> "assets/verification_account_en.html"
	yamlFiles := make(map[string]string) // "verification_account_en" -> "assets/verification_account_en.yaml"

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		baseName := strings.TrimSuffix(name, filepath.Ext(name))

		switch strings.ToLower(filepath.Ext(name)) {
		case ".html":
			htmlFiles[baseName] = "assets/" + name
		case ".yaml", ".yml":
			yamlFiles[baseName] = "assets/" + name
		default:
			// ignore
		}
	}

	// Create MailTemplate for each HTML+YAML pair
	for baseName, htmlPath := range htmlFiles {
		if yamlPath, exists := yamlFiles[baseName]; exists {
			// Load YAML content
			yamlContent, err := root.Assets.ReadFile(yamlPath)
			if err != nil {
				return fmt.Errorf("failed to read YAML file %s: %w", yamlPath, err)
			}

			var content TemplatePlainText
			if err := yaml.Unmarshal(yamlContent, &content); err != nil {
				return fmt.Errorf("failed to parse YAML file %s: %w", yamlPath, err)
			}

			// Store the template
			availableTemplates[TemplateFile(baseName)] = LocalizedTemplate{
				HTMLFile:  htmlPath,
				PlainText: content,
			}
		}
	}

	return nil
}

// ExecTemplate method executes both the HTML template and the YAML-based
// plain text template with the provided data. It returns a notification
// with both HTML body and plain text body filled.
func (lmt LocalizedTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	// Execute HTML template
	htmlContent, err := root.Assets.ReadFile(lmt.HTMLFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTML file %s: %w", lmt.HTMLFile, err)
	}

	htmlTmpl, err := htmltemplate.New("html").Parse(string(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML template: %w", err)
	}

	var htmlBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, data); err != nil {
		return nil, fmt.Errorf("failed to execute HTML template: %w", err)
	}

	// Execute plain text and subject templates using ExecPlain
	plainNotification, err := lmt.ExecPlain(data)
	if err != nil {
		return nil, err
	}

	// Combine HTML with plain text results
	return &notifications.Notification{
		Subject:   plainNotification.Subject,
		Body:      htmlBuf.String(),
		PlainBody: plainNotification.PlainBody,
	}, nil
}

// ExecPlain method executes only the plain text template from YAML content
// with the provided data. This method is useful for SMS notifications or
// when only plain text is needed.
func (lmt LocalizedTemplate) ExecPlain(data any) (*notifications.Notification, error) {
	// Execute plain text template from YAML
	plainTmpl, err := texttemplate.New("plain").Parse(lmt.PlainText.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plain text template: %w", err)
	}

	var plainBuf bytes.Buffer
	if err := plainTmpl.Execute(&plainBuf, data); err != nil {
		return nil, fmt.Errorf("failed to execute plain text template: %w", err)
	}

	// Execute subject template
	subjectTmpl, err := texttemplate.New("subject").Parse(lmt.PlainText.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to parse subject template: %w", err)
	}

	var subjectBuf bytes.Buffer
	if err := subjectTmpl.Execute(&subjectBuf, data); err != nil {
		return nil, fmt.Errorf("failed to execute subject template: %w", err)
	}

	return &notifications.Notification{
		Subject:   subjectBuf.String(),
		PlainBody: plainBuf.String(),
	}, nil
}
