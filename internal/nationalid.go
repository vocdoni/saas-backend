package internal

import (
	"strings"
	"unicode"
)

// NationalIDType is the kind of Spanish identity document a nationalId holds.
type NationalIDType string

const (
	// NationalIDTypeDNI is a Spanish national identity document: 8 digits and a check letter.
	NationalIDTypeDNI NationalIDType = "DNI"
	// NationalIDTypeNIE is a foreigner identity number: X, Y or Z, 7 digits and a check letter.
	NationalIDTypeNIE NationalIDType = "NIE"
)

// NationalIDClass classifies a raw nationalId value (see NormalizeNationalID).
type NationalIDClass string

const (
	// NationalIDMissing is an empty or whitespace-only value.
	NationalIDMissing NationalIDClass = "missing"
	// NationalIDCanonical is a DNI/NIE whose supplied check letter is right.
	NationalIDCanonical NationalIDClass = "canonical"
	// NationalIDDerivedLetter is a letterless DNI/NIE whose check letter was computed.
	NationalIDDerivedLetter NationalIDClass = "derived_letter"
	// NationalIDLetterMismatch is a DNI/NIE whose supplied check letter is wrong.
	NationalIDLetterMismatch NationalIDClass = "letter_mismatch"
	// NationalIDUnsupportedDocument is a 9-character tax id that is not a DNI/NIE (K/L/M NIFs, CIFs).
	NationalIDUnsupportedDocument NationalIDClass = "unsupported_document"
	// NationalIDInvalid is anything else, passports and arbitrary ids included.
	NationalIDInvalid NationalIDClass = "invalid"
)

// nationalIDControlLetters is the DNI/NIE check-letter table: the letter of a number n is
// nationalIDControlLetters[n%23].
const nationalIDControlLetters = "TRWAGMYFPDXBNJZSQVHLCKE"

// NationalIDResult is the classification of a raw nationalId value and its canonical form.
type NationalIDResult struct {
	Class NationalIDClass
	// Type is set for canonical, derived_letter and letter_mismatch values.
	Type NationalIDType
	// Canonical is the 9-character canonical form, set only for canonical and derived_letter values.
	Canonical string
	// Normalized is what to store and hash: Canonical when set, otherwise the trimmed raw value.
	Normalized string
}

// NormalizeNationalID classifies a raw DNI/NIE and returns its canonical form: 9 characters,
// uppercase, leading zeros kept, exactly what cert-check reports as Document.Number. Whitespace
// (NBSP included), dots and dashes are dropped, ASCII letters uppercased and short numbers
// left-padded; a missing check letter is derived (class derived_letter). A value that is not a
// DNI/NIE (wrong letter, other tax ids, passports, non-ASCII lookalikes) is never rewritten:
// Normalized is its trimmed form, so a census keyed on such values matches exactly as before.
// It never fails and scans by hand, so arbitrary input cannot make it backtrack or panic.
func NormalizeNationalID(raw string) NationalIDResult {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return NationalIDResult{Class: NationalIDMissing}
	}
	compact := make([]byte, 0, len(trimmed))
	for _, r := range trimmed {
		switch {
		case unicode.IsSpace(r), r == '.', r == '-':
			continue
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			compact = append(compact, byte(r))
		case r >= 'a' && r <= 'z':
			compact = append(compact, byte(r-'a'+'A'))
		default:
			// fullwidth digits, accented or Cyrillic lookalikes: warn, never guess
			return NationalIDResult{Class: NationalIDInvalid, Normalized: trimmed}
		}
	}
	return classifyNationalID(string(compact), trimmed)
}

// classifyNationalID applies the DNI/NIE grammar to compact, the separator-free uppercase ASCII
// form of trimmed.
func classifyNationalID(compact, trimmed string) NationalIDResult {
	invalid := NationalIDResult{Class: NationalIDInvalid, Normalized: trimmed}
	if compact == "" {
		return invalid
	}
	switch first := compact[0]; {
	case isASCIIDigit(first):
		digits, letter := splitCheckLetter(compact)
		if len(digits) > 8 || !allASCIIDigits(digits) {
			return invalid
		}
		number := strings.Repeat("0", 8-len(digits)) + digits
		return parsedNationalID{typ: NationalIDTypeDNI, body: number, number: number, letter: letter}.result(trimmed)
	case first == 'X' || first == 'Y' || first == 'Z':
		digits, letter := splitCheckLetter(compact[1:])
		if digits == "" || len(digits) > 7 || !allASCIIDigits(digits) {
			return invalid
		}
		padded := strings.Repeat("0", 7-len(digits)) + digits
		// the NIE prefix reads as a leading digit for the checksum: X=0, Y=1, Z=2
		prefixDigit := string(rune('0' + first - 'X'))
		return parsedNationalID{
			typ: NationalIDTypeNIE, body: string(first) + padded, number: prefixDigit + padded, letter: letter,
		}.result(trimmed)
	case len(compact) == 9 && isASCIIUpper(first) && allASCIIDigits(compact[1:8]):
		// K/L/M personal NIFs and CIF-shaped company ids: tax ids a certificate never carries as a DNI/NIE
		return NationalIDResult{Class: NationalIDUnsupportedDocument, Normalized: trimmed}
	default:
		return invalid
	}
}

// splitCheckLetter splits a trailing check letter off s; letter is 0 when s does not end in one.
func splitCheckLetter(s string) (digits string, letter byte) {
	if s != "" && isASCIIUpper(s[len(s)-1]) {
		return s[:len(s)-1], s[len(s)-1]
	}
	return s, 0
}

// parsedNationalID is a DNI/NIE that passed the grammar: body is its canonical form without the
// check letter, number the 8 digits the letter is computed from, and letter the supplied one (0
// when letterless).
type parsedNationalID struct {
	typ    NationalIDType
	body   string
	number string
	letter byte
}

// result derives or verifies the check letter. trimmed is the raw value, kept on a wrong letter.
func (p parsedNationalID) result(trimmed string) NationalIDResult {
	n := 0
	for i := range len(p.number) {
		n = n*10 + int(p.number[i]-'0')
	}
	expected := nationalIDControlLetters[n%23]
	canonical := p.body + string(rune(expected))
	switch p.letter {
	case 0:
		return NationalIDResult{Class: NationalIDDerivedLetter, Type: p.typ, Canonical: canonical, Normalized: canonical}
	case expected:
		return NationalIDResult{Class: NationalIDCanonical, Type: p.typ, Canonical: canonical, Normalized: canonical}
	default:
		return NationalIDResult{Class: NationalIDLetterMismatch, Type: p.typ, Normalized: trimmed}
	}
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func isASCIIUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

func allASCIIDigits(s string) bool {
	for i := range len(s) {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}
