package mailtemplates

import (
	"strings"
	"time"
)

const (
	// ImportIssuesShown is the most import issues an email lists.
	ImportIssuesShown = 10
	// ImportIssueMaxRunes caps each listed import issue.
	ImportIssueMaxRunes = 140
)

// Members import outcomes, chosen in Go so the template never decides what "completed" means.
const (
	ImportDone   = "done"   // every row was added
	ImportIssues = "issues" // some rows were added, some failed
	ImportFailed = "failed" // no row was added
	ImportEmpty  = "empty"  // the file had no rows
)

// CodeData is the data of the emails that carry a code and a link: verification_account,
// forgot_password, password_setup and verify_email_change.
type CodeData struct {
	Code   string
	Link   string
	Email  string        // the address the code is for
	Expiry time.Duration // how long the code stays valid
}

// InviteData is the data of invite_admin.
type InviteData struct {
	Organization     string // display name, empty when the organization has none
	OrganizationLogo string // org.LogoURL(); shown only if it passes orgLogoURL
	Link             string
	Expiry           time.Duration
}

// MembersImportData is the data of members_import_done; build it with NewMembersImportData.
type MembersImportData struct {
	Organization     string
	OrganizationLogo string
	UserName         string
	Link             string // empty when it could not be built: the button is omitted
	State            string // ImportDone, ImportIssues, ImportFailed or ImportEmpty
	Total            int
	Added            int
	ErrorCount       int
	Issues           []string // at most ImportIssuesShown, each at most ImportIssueMaxRunes
	MoreIssues       int      // ErrorCount minus the issues listed
	CompletedAt      time.Time
}

// MembersImportResult is what a finished members import reports.
type MembersImportResult struct {
	Organization     string
	OrganizationLogo string
	UserName         string
	Link             string
	Total            int
	Added            int
	Errors           []string
	CompletedAt      time.Time
}

// NewMembersImportData chooses the outcome and bounds the issue list, so a file with thousands of
// bad rows still yields a small email.
func NewMembersImportData(r MembersImportResult) MembersImportData {
	d := MembersImportData{
		Organization:     r.Organization,
		OrganizationLogo: r.OrganizationLogo,
		UserName:         strings.TrimSpace(r.UserName),
		Link:             r.Link,
		Total:            r.Total,
		Added:            r.Added,
		ErrorCount:       len(r.Errors),
		CompletedAt:      r.CompletedAt,
	}
	switch {
	case r.Total == 0:
		d.State = ImportEmpty
	case r.Added == 0:
		d.State = ImportFailed
	case len(r.Errors) > 0:
		d.State = ImportIssues
	default:
		d.State = ImportDone
	}
	shown := r.Errors[:min(len(r.Errors), ImportIssuesShown)]
	for _, issue := range shown {
		d.Issues = append(d.Issues, Truncate(issue, ImportIssueMaxRunes))
	}
	d.MoreIssues = len(r.Errors) - len(shown)
	return d
}
