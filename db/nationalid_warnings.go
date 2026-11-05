package db

import (
	"strings"

	"github.com/vocdoni/saas-backend/internal"
)

// NationalIDWarningDuplicate is the NationalIDWarning class of members (or import lines) whose
// nationalIds normalize to the same canonical DNI/NIE. Every holder of the value gets one.
const NationalIDWarningDuplicate = "duplicate_after_normalization"

// NationalIDWarning is a warning about a member's nationalId: either the member cannot sign in
// (letter_mismatch, invalid, unsupported_document, missing) or shares a canonical DNI/NIE with
// another member (duplicate_after_normalization). Reported by census validation (MemberID set) and
// by member import (Line set). No PII is included.
type NationalIDWarning struct {
	MemberID string `json:"memberId,omitempty" bson:"memberId,omitempty"`
	// Line is the 1-based position of the member in the submitted import list.
	Line  int    `json:"line,omitempty" bson:"line,omitempty"`
	Class string `json:"class" bson:"class"`
}

// nationalIDWarnings collects the nationalId warnings of a set of members, in the order they are
// added, followed by one duplicate entry for every holder of a canonical value held more than once.
type nationalIDWarnings struct {
	warnings []NationalIDWarning
	// holders lists, per canonical value, the identity (MemberID or Line) of each holder
	holders map[string][]NationalIDWarning
	// order is the canonical values in the order they were first seen, so the output is stable
	order []string
}

func newNationalIDWarnings() *nationalIDWarnings {
	return &nationalIDWarnings{holders: make(map[string][]NationalIDWarning)}
}

// add classifies one member's nationalId. id carries the member's identity (MemberID or Line).
// canonical and derived_letter values register the holder for duplicate grouping but emit no
// warning: both classes can sign in. Other classes (letter_mismatch, invalid,
// unsupported_document, missing) emit a warning.
func (n *nationalIDWarnings) add(id NationalIDWarning, value string) {
	r := internal.NormalizeNationalID(value)
	if r.Canonical != "" {
		if _, seen := n.holders[r.Canonical]; !seen {
			n.order = append(n.order, r.Canonical)
		}
		n.holders[r.Canonical] = append(n.holders[r.Canonical], id)
	}
	switch r.Class {
	case internal.NationalIDCanonical, internal.NationalIDDerivedLetter:
		// these members can sign in; register for duplicate grouping above but emit no warning.
		return
	default:
		w := id
		w.Class = string(r.Class)
		n.warnings = append(n.warnings, w)
	}
}

// result returns the collected warnings, never nil.
func (n *nationalIDWarnings) result() []NationalIDWarning {
	out := append(make([]NationalIDWarning, 0, len(n.warnings)), n.warnings...)
	for _, canonical := range n.order {
		if holders := n.holders[canonical]; len(holders) > 1 {
			for _, id := range holders {
				id.Class = NationalIDWarningDuplicate
				out = append(out, id)
			}
		}
	}
	return out
}

// nationalIDImportWarnings returns the nationalId warnings of a member import, Line being each
// member's 1-based position in members. Duplicates are checked within members only; checking
// them against the members already stored is census validation's job. An empty nationalId is
// not reported: it is only a problem for a census logging in with it, which validation reports.
func nationalIDImportWarnings(members []*OrgMember) []NationalIDWarning {
	n := newNationalIDWarnings()
	for i, m := range members {
		if strings.TrimSpace(m.NationalID) == "" {
			continue
		}
		n.add(NationalIDWarning{Line: i + 1}, m.NationalID)
	}
	return n.result()
}
