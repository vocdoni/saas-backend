package notifications

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.vocdoni.io/dvote/log"
)

// ErrNoSMSProvider is returned by the SMS test-prefix wrapper when a non-test number is submitted
// but no real SMS provider is configured.
var ErrNoSMSProvider = errors.New("no SMS provider configured")

// prefixRe matches "+" followed by 1-15 digits (the maximum E.164 subscriber+country length).
var prefixRe = regexp.MustCompile(`^\+[0-9]{1,15}$`)

// SMSTestPrefixes is a list of E.164 number prefixes for dev/staging OTP bypass. Any normalized
// E.164 number whose "+digits" form starts with one of the prefixes is a test number.
type SMSTestPrefixes []string

// ParseSMSTestPrefixes parses a comma-separated list of E.164 prefixes into SMSTestPrefixes.
// Each entry must be "+" followed by 1-15 digits; empty entries and duplicates are rejected.
// Empty input returns nil.
func ParseSMSTestPrefixes(raw string) (SMSTestPrefixes, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var out SMSTestPrefixes
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !prefixRe.MatchString(entry) {
			return nil, fmt.Errorf("invalid prefix %q: must be '+' followed by 1-15 digits", entry)
		}
		if seen[entry] {
			return nil, fmt.Errorf("duplicate prefix %q", entry)
		}
		seen[entry] = true
		out = append(out, entry)
	}
	return out, nil
}

// Code returns whether number is a test number and, when it is, its fixed 6-digit OTP code
// (the last 6 digits of the E.164 number after "+"). A valid E.164 number is always at least 7
// characters ("+N NNNNNN"), so a number shorter than 6 digits after "+" never matches.
func (p SMSTestPrefixes) Code(number string) (string, bool) {
	digits := strings.TrimPrefix(number, "+")
	if len(digits) < 6 {
		return "", false
	}
	for _, prefix := range p {
		if strings.HasPrefix(number, prefix) {
			return digits[len(digits)-6:], true
		}
	}
	return "", false
}

// SMSTestPrefixService is a NotificationService wrapper for dev/staging test numbers. It suppresses
// the real SMS send for numbers matched by Prefixes and delegates non-test numbers to Next. If Next
// is nil and the number is not a test number, it returns ErrNoSMSProvider.
type SMSTestPrefixService struct {
	Prefixes SMSTestPrefixes
	Next     NotificationService
}

var _ NotificationService = (*SMSTestPrefixService)(nil)

// New is a no-op; SMSTestPrefixService is configured via struct fields.
func (*SMSTestPrefixService) New(any) error { return nil }

// SendNotification suppresses the SMS for test numbers (logging the number only) and delegates to
// Next for real ones. Returns ErrNoSMSProvider when Next is nil and the number is not a test number.
func (s *SMSTestPrefixService) SendNotification(ctx context.Context, n *Notification) error {
	if _, ok := s.Prefixes.Code(n.ToNumber); ok {
		log.Infow("SMS test-prefix: suppressing real send", "toNumber", n.ToNumber)
		return nil
	}
	if s.Next == nil {
		return ErrNoSMSProvider
	}
	return s.Next.SendNotification(ctx, n)
}
