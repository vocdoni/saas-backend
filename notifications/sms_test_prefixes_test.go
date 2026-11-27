package notifications_test

import (
	"context"
	"errors"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications"
)

func TestParseSMSTestPrefixes(t *testing.T) {
	c := qt.New(t)

	tests := []struct {
		name    string
		input   string
		want    notifications.SMSTestPrefixes
		wantErr bool
	}{
		{
			name:  "empty string returns nil",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace-only returns nil",
			input: "   ",
			want:  nil,
		},
		{
			name:  "single valid prefix",
			input: "+442079460",
			want:  notifications.SMSTestPrefixes{"+442079460"},
		},
		{
			name:  "multiple valid prefixes",
			input: "+442079460,+441134960",
			want:  notifications.SMSTestPrefixes{"+442079460", "+441134960"},
		},
		{
			name:  "spaces around entries are trimmed",
			input: " +442079460 , +441134960 ",
			want:  notifications.SMSTestPrefixes{"+442079460", "+441134960"},
		},
		{
			name:  "full number is a valid prefix",
			input: "+442079460123",
			want:  notifications.SMSTestPrefixes{"+442079460123"},
		},
		{
			name:    "missing leading plus",
			input:   "442079460",
			wantErr: true,
		},
		{
			name:    "non-digit after plus",
			input:   "+44abc",
			wantErr: true,
		},
		{
			name:    "duplicate prefix",
			input:   "+442079460,+442079460",
			wantErr: true,
		},
		{
			name:    "too many digits (>15)",
			input:   "+1234567890123456",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *qt.C) {
			got, err := notifications.ParseSMSTestPrefixes(tt.input)
			if tt.wantErr {
				c.Assert(err, qt.IsNotNil)
				return
			}
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.DeepEquals, tt.want)
		})
	}
}

func TestSMSTestPrefixesCode(t *testing.T) {
	c := qt.New(t)

	prefixes := notifications.SMSTestPrefixes{"+442079460"}

	c.Run("matched number returns last 6 digits", func(c *qt.C) {
		code, ok := prefixes.Code("+442079460123")
		c.Assert(ok, qt.IsTrue)
		c.Assert(code, qt.Equals, "460123")
	})

	c.Run("unmatched number returns false", func(c *qt.C) {
		_, ok := prefixes.Code("+34612345678")
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("number too short to yield 6-digit code", func(c *qt.C) {
		_, ok := prefixes.Code("+44207")
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("empty prefixes never match", func(c *qt.C) {
		var empty notifications.SMSTestPrefixes
		_, ok := empty.Code("+442079460123")
		c.Assert(ok, qt.IsFalse)
	})
}

// mockSMS is a simple SendNotification recorder for wrapper tests.
type mockSMS struct {
	calls []string
}

func (*mockSMS) New(any) error { return nil }
func (m *mockSMS) SendNotification(_ context.Context, n *notifications.Notification) error {
	m.calls = append(m.calls, n.ToNumber)
	return nil
}

func TestSMSTestPrefixServiceWrapper(t *testing.T) {
	c := qt.New(t)

	prefixes := notifications.SMSTestPrefixes{"+442079460"}

	c.Run("test number is suppressed and next not called", func(c *qt.C) {
		next := &mockSMS{}
		svc := &notifications.SMSTestPrefixService{Prefixes: prefixes, Next: next}
		err := svc.SendNotification(context.Background(), &notifications.Notification{ToNumber: "+442079460123"})
		c.Assert(err, qt.IsNil)
		c.Assert(next.calls, qt.HasLen, 0)
	})

	c.Run("non-test number is forwarded to next", func(c *qt.C) {
		next := &mockSMS{}
		svc := &notifications.SMSTestPrefixService{Prefixes: prefixes, Next: next}
		err := svc.SendNotification(context.Background(), &notifications.Notification{ToNumber: "+34612345678"})
		c.Assert(err, qt.IsNil)
		c.Assert(next.calls, qt.DeepEquals, []string{"+34612345678"})
	})

	c.Run("non-test number with nil next returns ErrNoSMSProvider", func(c *qt.C) {
		svc := &notifications.SMSTestPrefixService{Prefixes: prefixes, Next: nil}
		err := svc.SendNotification(context.Background(), &notifications.Notification{ToNumber: "+34612345678"})
		c.Assert(errors.Is(err, notifications.ErrNoSMSProvider), qt.IsTrue)
	})

	c.Run("test number with nil next still suppressed", func(c *qt.C) {
		svc := &notifications.SMSTestPrefixService{Prefixes: prefixes, Next: nil}
		err := svc.SendNotification(context.Background(), &notifications.Notification{ToNumber: "+442079460123"})
		c.Assert(err, qt.IsNil)
	})
}
