package notifications

import (
	"context"
	"errors"
	"fmt"

	"go.vocdoni.io/dvote/log"
)

// FailoverService is a NotificationService that delivers a notification by
// trying an ordered list of underlying services. It returns as soon as one
// succeeds, falling through to the next only when the current one fails. It is
// used to fail over from a primary provider to one or more backup providers
// that share the same sender identity (same From address/domain, different SMTP
// relay).
//
// Because the CSP OTP challenges are idempotent, a failover that ends up
// delivering the same message through more than one provider (for example when
// the primary actually delivered but reported an error) is harmless.
type FailoverService struct {
	services []NotificationService
}

var _ NotificationService = (*FailoverService)(nil)

// NewFailoverService builds a FailoverService from the given services in
// priority order (primary first). nil services are skipped, so callers may pass
// optional backups directly. The services must already be initialized.
func NewFailoverService(services ...NotificationService) *FailoverService {
	return &FailoverService{services: services}
}

// New implements NotificationService. The wrapped services are configured by
// the caller before being passed to NewFailoverService, so this is a no-op.
func (*FailoverService) New(any) error { return nil }

// SendNotification tries each configured service in order and returns nil on the
// first success. If every service fails it returns the joined error; if no
// services are configured it returns an error.
func (f *FailoverService) SendNotification(ctx context.Context, notification *Notification) error {
	var joined error
	attempted := 0
	for i, service := range f.services {
		if service == nil {
			continue
		}
		attempted++
		if err := service.SendNotification(ctx, notification); err != nil {
			log.Warnw("notification provider failed, trying next", "provider", i, "error", err)
			joined = errors.Join(joined, fmt.Errorf("provider %d: %w", i, err))
			continue
		}
		if i > 0 {
			log.Infow("notification delivered via backup provider", "provider", i)
		}
		return nil
	}
	if attempted == 0 {
		return fmt.Errorf("no notification providers configured")
	}
	return joined
}
