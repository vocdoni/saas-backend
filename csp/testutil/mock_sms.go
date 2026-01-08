// Package testutil contains utils useful for tests
package testutil

import (
	"context"
	"sync"

	"github.com/vocdoni/saas-backend/notifications"
)

// MockSMS is a mock implementation of the NotificationService interface
type MockSMS struct {
	storage sync.Map // key: string, value: *notifications.Notification
}

var _ notifications.NotificationService = &MockSMS{}

// New does nothing
func (*MockSMS) New(any) error { return nil }

// SendNotification mocks a sending of an SMS notification to the recipient.
func (mock *MockSMS) SendNotification(_ context.Context, n *notifications.Notification) error {
	mock.storage.Store(n.ToNumber, n)
	return nil
}

// FindNotification fetches a stored SMS notification for the recipient.
func (mock *MockSMS) FindNotification(toNumber string) *notifications.Notification {
	if v, ok := mock.storage.Load(toNumber); ok {
		if n, ok := v.(*notifications.Notification); ok {
			return n
		}
	}
	return nil
}
