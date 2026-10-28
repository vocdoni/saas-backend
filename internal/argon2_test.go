package internal

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
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

func TestHashOrgDataVariantsAgree(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x42}
	want := HashOrgData(org, "+34600000000")
	got, err := HashOrgDataContext(context.Background(), org, "+34600000000")
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, want)
	c.Assert(HashOrgDataBackground(org, "+34600000000"), qt.DeepEquals, want)
	c.Assert(HashPasswordBackground("salt", "password"), qt.DeepEquals, HashPassword("salt", "password"))
}

func TestHashOrgDataContextBusy(t *testing.T) {
	c := qt.New(t)
	for range cap(hashSlots) {
		hashSlots <- struct{}{}
	}
	defer func() {
		for range cap(hashSlots) {
			<-hashSlots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := HashOrgDataContext(ctx, common.Address{0x42}, "+34600000000")
	c.Assert(errors.Is(err, ErrHashingBusy), qt.IsTrue)
}

// TestBackgroundHashingHoldsOneSlot pins the background gate: while one background hash holds it,
// another waits for the gate rather than taking a second hashing slot, and a request handler's hash
// still gets a slot meanwhile.
func TestBackgroundHashingHoldsOneSlot(t *testing.T) {
	c := qt.New(t)
	// stand in for a background hash in progress
	backgroundGate <- struct{}{}
	done := make(chan struct{})
	go func() {
		HashPasswordBackground("salt", "password")
		close(done)
	}()

	_, err := HashPasswordContext(context.Background(), "salt", "password")
	c.Assert(err, qt.IsNil)
	select {
	case <-done:
		c.Fatal("a second background hash ran while the gate was held")
	case <-time.After(50 * time.Millisecond):
	}
	c.Assert(hashSlots, qt.HasLen, 0, qt.Commentf("the waiting background hash must not hold a slot"))

	<-backgroundGate
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatal("the background hash did not run once the gate was free")
	}
}
