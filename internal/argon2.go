package internal

import (
	"context"
	"encoding/hex"
	"fmt"
	"runtime"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/argon2"
)

// Argon2 parameters for hashing, if modified, the current hashes will be invalidated.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 4
	argonThreads   = 8
	argonKeyLen    = 32
)

// HashSlotWait is how long HashPasswordContext waits for a free hashing slot before giving up
// with ErrHashingBusy.
const HashSlotWait = 5 * time.Second

// ErrHashingBusy is returned by the context-aware hashing helpers when every hashing slot stays
// taken for longer than HashSlotWait (or the context ends first).
var ErrHashingBusy = fmt.Errorf("password hashing capacity exhausted, retry later")

// hashSlots bounds how many Argon2id derivations run at once. Each one allocates argonMemoryKiB
// of memory and runs argonThreads threads, so letting request concurrency decide how many run in
// parallel lets a burst of login or registration requests exhaust the host's RAM and CPU.
var hashSlots = make(chan struct{}, min(max(runtime.NumCPU(), 2), 4))

// hashWaiters bounds how many bounded-wait callers (request handlers) can queue for a hashing
// slot. Each waiter holds a slot of the API-wide request throttle, so an unbounded queue would let
// a login flood occupy all of them and starve every other endpoint; past this many waiters,
// callers fail at once.
var hashWaiters = make(chan struct{}, 4*cap(hashSlots))

// acquireHashSlot takes a hashing slot, waiting until ctx is done or, when wait > 0, until wait
// elapses; a bounded wait also needs a place in the hashWaiters queue. The caller must release
// the slot with releaseHashSlot.
func acquireHashSlot(ctx context.Context, wait time.Duration) error {
	if wait > 0 {
		select {
		case hashSlots <- struct{}{}:
			return nil
		default:
		}
		select {
		case hashWaiters <- struct{}{}:
			defer func() { <-hashWaiters }()
		default:
			return ErrHashingBusy
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrHashingBusy
	}
}

func releaseHashSlot() {
	<-hashSlots
}

// argon2hashContext derives the Argon2id key of data, holding a hashing slot for the duration.
func argon2hashContext(ctx context.Context, wait time.Duration, data, salt []byte) ([]byte, error) {
	if err := acquireHashSlot(ctx, wait); err != nil {
		return nil, err
	}
	defer releaseHashSlot()
	return argon2.IDKey(data, salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen), nil
}

// argon2hash is the blocking variant of argon2hashContext: it waits as long as needed for a slot.
func argon2hash(data, salt []byte) []byte {
	key, err := argon2hashContext(context.Background(), 0, data, salt)
	if err != nil {
		// unreachable: a background context with no wait limit never ends
		panic(fmt.Sprintf("argon2 hashing failed: %v", err))
	}
	return key
}

// HashPassword helper function allows to hash a password using a salt. It blocks until a hashing
// slot is free; request handlers should use HashPasswordContext so they can fail fast instead.
func HashPassword(salt, password string) []byte {
	return argon2hash([]byte(password), []byte(salt))
}

// HexHashPassword helper function allows to hash a password using a salt and
// return the result as a hex string.
func HexHashPassword(salt, password string) string {
	return hex.EncodeToString(HashPassword(salt, password))
}

// HashPasswordContext hashes a password like HashPassword, but returns ErrHashingBusy when no
// hashing slot frees up within HashSlotWait or before ctx is done.
func HashPasswordContext(ctx context.Context, salt, password string) ([]byte, error) {
	return argon2hashContext(ctx, HashSlotWait, []byte(password), []byte(salt))
}

// HexHashPasswordContext is HashPasswordContext returning the hash as a hex string.
func HexHashPasswordContext(ctx context.Context, salt, password string) (string, error) {
	hash, err := HashPasswordContext(ctx, salt, password)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash), nil
}

// HashOrgData hashes organization data using the organization address as salt.
func HashOrgData(orgAddress common.Address, data string) []byte {
	return argon2hash([]byte(data), orgAddress.Bytes())
}
