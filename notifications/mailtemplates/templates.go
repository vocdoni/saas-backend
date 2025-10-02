package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"path/filepath"
	"strings"
	texttemplate "text/template"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications"
	"gopkg.in/yaml.v3"
)

// availableTemplates is a map that stores the loaded templates
var availableTemplates map[TemplateKey]LocalizedTemplate

// TemplateKey represents a template key. Every template should
// have a key that identifies it, which is the filename without the extension.
type TemplateKey string

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
// Falls back to apicommon.DefaultLang if the language is not found.
func (mt MailTemplate) Localized(lang string) LocalizedTemplate {
	templateKey := mt.File + "_" + lang
	if template, exists := availableTemplates[TemplateKey(templateKey)]; exists {
		return template
	}

	defaultLangKey := mt.File + "_" + apicommon.DefaultLang
	if template, exists := availableTemplates[TemplateKey(defaultLangKey)]; exists {
		return template
	}

	// Return empty template if nothing found
	return LocalizedTemplate{}
}

// LocalizedTemplate struct represents an email template.
// It contains the HTML and YAML file paths
type LocalizedTemplate struct {
	HTMLFile string
	YAMLFile string
}

// Available function returns the available email templates. It returns a map
// with the template key and the LocalizedTemplate struct.
func Available() map[TemplateKey]LocalizedTemplate {
	return availableTemplates
}

// Load function reads the email templates from embedded assets. It reads both
// HTML and YAML files from the "assets" directory and creates MailTemplate
// structs for each HTML+YAML pair. It returns an error if the directory
// could not be read or if the files could not be parsed.
func Load() error {
	// reset the map to store the templates
	availableTemplates = make(map[TemplateKey]LocalizedTemplate)

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
			// Store the template
			availableTemplates[TemplateKey(baseName)] = LocalizedTemplate{
				HTMLFile: htmlPath,
				YAMLFile: yamlPath,
			}
		}
	}
	return nil
}

// ExecTemplate method executes both the HTML template and the YAML-based
// plain text template with the provided data. It returns a notification
// with both HTML body and plain text body filled.
func (lmt LocalizedTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	// Load HTML content
	htmlContent, err := root.Assets.ReadFile(lmt.HTMLFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTML file %s: %w", lmt.HTMLFile, err)
	}

	// Execute HTML template
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

// PlainText unmarshals the content of the YAML file.
func (lmt LocalizedTemplate) Plaintext() (*TemplatePlainText, error) {
	// Load YAML content
	yamlContent, err := root.Assets.ReadFile(lmt.YAMLFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file %s: %w", lmt.YAMLFile, err)
	}

	var plaintext TemplatePlainText
	if err := yaml.Unmarshal(yamlContent, &plaintext); err != nil {
		return nil, fmt.Errorf("failed to parse YAML file %s: %w", lmt.YAMLFile, err)
	}

	return &plaintext, nil
}

// ExecPlain method executes only the plain text template from YAML content
// with the provided data. This method is useful for SMS notifications or
// when only plain text is needed.
func (lmt LocalizedTemplate) ExecPlain(data any) (*notifications.Notification, error) {
	plaintext, err := lmt.Plaintext()
	if err != nil {
		return nil, err
	}

	// Execute plain text template from YAML
	plainTmpl, err := texttemplate.New("plain").Parse(plaintext.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plain text template: %w", err)
	}

	var plainBuf bytes.Buffer
	if err := plainTmpl.Execute(&plainBuf, data); err != nil {
		return nil, fmt.Errorf("failed to execute plain text template: %w", err)
	}

	// Execute subject template
	subjectTmpl, err := texttemplate.New("subject").Parse(plaintext.Subject)
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
