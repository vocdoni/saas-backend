package migrations

import "context"

// lockRenewerKey is the context key of the function renewing the cross-instance migration lock.
type lockRenewerKey struct{}

// WithLockRenewer returns a copy of ctx carrying renew, which extends the cross-instance migration
// lock held by the run applying the migration. db.RunMigrationsUp sets it on the context it hands
// to every Up function.
func WithLockRenewer(ctx context.Context, renew func(context.Context) error) context.Context {
	return context.WithValue(ctx, lockRenewerKey{}, renew)
}

// RenewLock extends the cross-instance migration lock through the renewer ctx carries, failing
// when the lock was lost (it expired and another instance took it over). A migration that may run
// longer than the lock TTL calls it before each unit of work and stops on error, so two instances
// never migrate at once. Without a renewer (a test calling Up directly, RunMigrationsDown) it does
// nothing.
func RenewLock(ctx context.Context) error {
	renew, ok := ctx.Value(lockRenewerKey{}).(func(context.Context) error)
	if !ok || renew == nil {
		return nil
	}
	return renew(ctx)
}
