package db

import (
	"strings"

	"github.com/vocdoni/saas-backend/internal"
)

// NationalIDWarningDuplicate is the NationalIDWarning class of members (or import lines) whose
// nationalIds normalize to the same canonical DNI/NIE. Every holder of the value gets one.
const NationalIDWarningDuplicate = "duplicate_after_normalization"

// NationalIDWarning is a non-blocking problem with a member's nationalId, reported by census
// validation (MemberID set) and by member import (Line set). Class is an internal.NationalIDClass
// other than canonical, or NationalIDWarningDuplicate. Original and Derived are set only for
// derived_letter: the value as imported and the canonical value with the derived check letter.
// They carry DNI values, so they only travel in Manager/Admin responses, never in logs.
type NationalIDWarning struct {
	MemberID string `json:"memberId,omitempty" bson:"memberId,omitempty"`
	// Line is the 1-based position of the member in the submitted import list.
	Line     int    `json:"line,omitempty" bson:"line,omitempty"`
	Class    string `json:"class" bson:"class"`
	Original string `json:"original,omitempty" bson:"original,omitempty"`
	Derived  string `json:"derived,omitempty" bson:"derived,omitempty"`
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

// add classifies one member's nationalId. id carries the member's identity (MemberID or Line);
// original is its NationalIDOriginal, empty on the import path. A canonical value with a marker
// is reported as derived_letter, like the letterless value it was derived from.
func (n *nationalIDWarnings) add(id NationalIDWarning, value, original string) {
	r := internal.NormalizeNationalID(value)
	if r.Canonical != "" {
		if _, seen := n.holders[r.Canonical]; !seen {
			n.order = append(n.order, r.Canonical)
		}
		n.holders[r.Canonical] = append(n.holders[r.Canonical], id)
	}
	w := id
	switch r.Class {
	case internal.NationalIDCanonical:
		if original == "" {
			return
		}
		w.Class, w.Original, w.Derived = string(internal.NationalIDDerivedLetter), original, r.Canonical
	case internal.NationalIDDerivedLetter:
		w.Class, w.Original, w.Derived = string(r.Class), strings.TrimSpace(value), r.Canonical
	default:
		w.Class = string(r.Class)
	}
	n.warnings = append(n.warnings, w)
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
