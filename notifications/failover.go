package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.vocdoni.io/dvote/log"
)

// DefaultProviderTimeout is the per-provider send timeout used by a
// FailoverService when none is configured.
const DefaultProviderTimeout = 10 * time.Second

// FailoverService is a NotificationService that delivers a notification by
// trying an ordered list of underlying services. It returns as soon as one
// succeeds, falling through to the next only when the current one fails. It is
// used to fail over from a primary provider to one or more backup providers
// that share the same sender identity (same From address/domain, different SMTP
// relay).
//
// Each provider attempt gets its own independent timeout (providerTimeout)
// rather than sharing the caller's deadline. This is essential: a primary that
// is slow to fail (hanging connection, timeout) would otherwise consume the
// whole deadline and leave the backup with no time, so the backup would fail
// immediately and never actually deliver. The caller's cancellation (e.g. on
// shutdown) is still honored.
//
// Because the CSP OTP challenges are idempotent, a failover that ends up
// delivering the same message through more than one provider (for example when
// the primary actually delivered but reported an error) is harmless.
type FailoverService struct {
	services        []NotificationService
	providerTimeout time.Duration
}

var _ NotificationService = (*FailoverService)(nil)

// NewFailoverService builds a FailoverService from the given services in
// priority order (primary first). nil services are skipped, so callers may pass
// optional backups directly. The services must already be initialized.
func NewFailoverService(services ...NotificationService) *FailoverService {
	return &FailoverService{
		services:        services,
		providerTimeout: DefaultProviderTimeout,
	}
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
		if err := f.sendVia(ctx, service, notification); err != nil {
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

// sendVia delivers through a single service with a fresh, independent timeout so
// that a slow provider cannot consume the time budget of the next one.
//
// The caller's deadline is intentionally detached (context.WithoutCancel): if it
// were kept, a primary that exhausted the caller's deadline would leave the
// backup with an already-expired context, so the backup would fail immediately
// and never deliver — the very failure this guards against. A context that hit
// its deadline is indistinguishable from a cancelled one to AfterFunc, so we
// cannot selectively propagate cancellation either; instead each attempt is
// bounded solely by providerTimeout. On shutdown the process exits promptly
// regardless, so an in-flight attempt is abandoned then anyway.
func (f *FailoverService) sendVia(ctx context.Context, service NotificationService, notification *Notification) error {
	attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), f.providerTimeout)
	defer cancel()
	return service.SendNotification(attemptCtx, notification)
}
