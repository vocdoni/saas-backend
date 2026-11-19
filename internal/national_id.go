package internal

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// nationalIDCheckLetters are the DNI/NIE check letters, indexed by the number modulo 23.
const nationalIDCheckLetters = "TRWAGMYFPDXBNJZSQVHLCKE"

var (
	dniPattern = regexp.MustCompile(`^0*([0-9]{1,8})([A-Z])$`)
	niePattern = regexp.MustCompile(`^([XYZ])0*([0-9]{1,7})([A-Z])$`)
)

// CanonicalNationalID returns the canonical form of a Spanish DNI or NIE: uppercase, without
// separators, the DNI number left-padded to 8 digits and the NIE number to 7, as cert-check reports
// the document number of a certificate. It ignores case, whitespace (unicode.IsSpace), '.', '-', '_'
// and '/', and missing or extra leading zeros. ok is false, and the input is returned trimmed but
// otherwise unchanged, when the value is not a DNI or NIE with a valid check letter.
func CanonicalNationalID(s string) (canonical string, ok bool) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune(".-_/", r) {
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if m := dniPattern.FindStringSubmatch(cleaned); m != nil {
		if checkLetterMatches(m[1], m[2]) {
			return leftPad(m[1], 8) + m[2], true
		}
	} else if m := niePattern.FindStringSubmatch(cleaned); m != nil {
		if checkLetterMatches(strconv.Itoa(strings.IndexByte("XYZ", m[1][0]))+leftPad(m[2], 7), m[3]) {
			return m[1] + leftPad(m[2], 7) + m[3], true
		}
	}
	return strings.TrimSpace(s), false
}

// checkLetterMatches reports whether letter is the check letter of the decimal number digits.
func checkLetterMatches(digits, letter string) bool {
	n, err := strconv.Atoi(digits)
	return err == nil && nationalIDCheckLetters[n%23] == letter[0]
}

// leftPad pads digits with zeros on the left up to width.
func leftPad(digits string, width int) string {
	return strings.Repeat("0", width-len(digits)) + digits
}
