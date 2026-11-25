package mailtemplates

import (
	"path"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/objectstorage"
)

const (
	// SupportDescriptionMaxRunes caps the support ticket description in the email.
	SupportDescriptionMaxRunes = 1200
)

// SupportData is the data of support.
type SupportData struct {
	Type         string
	Title        string
	Description  string // at most SupportDescriptionMaxRunes, see NewSupportData
	Truncated    bool   // the description was cut
	Email        string
	Organization string
}

// NewSupportData caps the description, keeping its line breaks.
func NewSupportData(ticketType, title, description, email, organization string) SupportData {
	cut := Truncate(description, SupportDescriptionMaxRunes)
	return SupportData{
		Type:         ticketType,
		Title:        title,
		Description:  cut,
		Truncated:    cut != description,
		Email:        email,
		Organization: organization,
	}
}

// OTPData is the data of verification_code_otp, by email and by SMS.
type OTPData struct {
	Code             string
	Organization     string
	OrganizationLogo string
	ExpiryTime       time.Duration
}

// orgLogoExtensions are the raster formats every mail client displays.
var orgLogoExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

// orgLogoURL returns the canonical URL of an organization logo when it may appear in mail: only a
// raster image kept in this service's object storage, so an organization cannot put a tracking
// pixel or an external image in mail sent to other people. Anything else returns "" and the
// email uses its no-logo layout.
func orgLogoURL(raw, serverURL string) string {
	storage := objectstorage.Client{ServerURL: serverURL}
	name, ok := storage.LocalName(strings.TrimSpace(raw))
	if !ok || strings.ContainsAny(name, "/?#\\") {
		return ""
	}
	ext := strings.ToLower(path.Ext(name))
	for _, allowed := range orgLogoExtensions {
		if ext == allowed {
			return storage.LocalURL(name)
		}
	}
	return ""
}
