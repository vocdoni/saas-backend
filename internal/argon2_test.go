package internal

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"golang.org/x/crypto/argon2"
)

func TestHashPasswordUnchanged(t *testing.T) {
	c := qt.New(t)
	// stored password hashes must keep verifying, so the parameters cannot drift
	want := hex.EncodeToString(argon2.IDKey([]byte("password"), []byte("salt"), 4, 64*1024, 8, 32))
	c.Assert(HexHashPassword("salt", "password"), qt.Equals, want)

	got, err := HexHashPasswordContext(context.Background(), "salt", "password")
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, want)
}

func TestHashPasswordContextBusy(t *testing.T) {
	c := qt.New(t)
	// take every hashing slot, as a flood of concurrent logins would
	for range cap(hashSlots) {
		hashSlots <- struct{}{}
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		for range cap(hashSlots) {
			<-hashSlots
		}
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := HashPasswordContext(ctx, "salt", "password")
	c.Assert(errors.Is(err, ErrHashingBusy), qt.IsTrue)
	// it gives up instead of waiting for a slot
	c.Assert(time.Since(start) < HashSlotWait, qt.IsTrue)

	// with the waiting queue full too, callers are refused without waiting at all
	for range cap(hashWaiters) {
		hashWaiters <- struct{}{}
	}
	start = time.Now()
	_, err = HashPasswordContext(context.Background(), "salt", "password")
	c.Assert(errors.Is(err, ErrHashingBusy), qt.IsTrue)
	c.Assert(time.Since(start) < 50*time.Millisecond, qt.IsTrue)
	for range cap(hashWaiters) {
		<-hashWaiters
	}

	release()
	_, err = HashPasswordContext(context.Background(), "salt", "password")
	c.Assert(err, qt.IsNil)
	c.Assert(hashSlots, qt.HasLen, 0)
}
