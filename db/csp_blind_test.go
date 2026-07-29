package db

import (
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	testBlindUserID     = internal.HexBytes([]byte("blindUserID"))
	testBlindElectionID = internal.HexBytes([]byte("blindElectionID"))
	testBlindSecretK    = []byte("0123456789abcdef0123456789abcdef")
)

func TestCSPBlindSessionRoundTrip(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Run("bad inputs", func(c *qt.C) {
		c.Assert(testDB.SetCSPBlindSession(nil, testBlindElectionID, testBlindSecretK, 0), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, nil, testBlindSecretK, 0), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, nil, 0), qt.ErrorIs, ErrBadInputs)

		_, err := testDB.ConsumeCSPBlindSession(nil, testBlindElectionID)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.ConsumeCSPBlindSession(testBlindUserID, nil)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})

	c.Run("store and consume", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, testBlindSecretK, 0), qt.IsNil)

		session, err := testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.IsNil)
		c.Assert(session.SecretK, qt.DeepEquals, testBlindSecretK)
		c.Assert(session.UserID, qt.DeepEquals, testBlindUserID)
		c.Assert(session.ElectionID, qt.DeepEquals, testBlindElectionID)
	})

	c.Run("consuming twice fails", func(c *qt.C) {
		// This is the property that keeps the salted private key secret: signing
		// two different blinded messages with one k reveals d.
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, testBlindSecretK, 0), qt.IsNil)

		_, err := testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.IsNil)
		_, err = testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.ErrorIs, ErrCSPBlindSessionNotFound)
	})

	c.Run("unknown session", func(c *qt.C) {
		_, err := testDB.ConsumeCSPBlindSession(internal.HexBytes([]byte("nobody")), testBlindElectionID)
		c.Assert(err, qt.ErrorIs, ErrCSPBlindSessionNotFound)
	})

	c.Run("expired session is not returned", func(c *qt.C) {
		// Mongo's TTL monitor only sweeps about once a minute, so an expired
		// document is usually still present; the read must reject it anyway.
		// A short positive TTL is used because SetCSPBlindSession treats a
		// non-positive one as "apply the default".
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, testBlindSecretK, time.Millisecond), qt.IsNil)
		time.Sleep(20 * time.Millisecond)

		_, err := testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.ErrorIs, ErrCSPBlindSessionNotFound)
	})

	c.Run("preparing again replaces the previous session", func(c *qt.C) {
		// The client gets a fresh R point, so the previous k must stop being usable.
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, testBlindSecretK, 0), qt.IsNil)
		newK := []byte("fedcba9876543210fedcba9876543210")
		c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, newK, 0), qt.IsNil)

		session, err := testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.IsNil)
		c.Assert(session.SecretK, qt.DeepEquals, newK)
		// and only one session ever existed for this key
		_, err = testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
		c.Assert(err, qt.ErrorIs, ErrCSPBlindSessionNotFound)
	})
}

// TestCSPBlindSessionConcurrentConsume asserts that concurrent consumers of one
// session produce exactly one winner. Without an atomic fetch-and-delete both
// callers would receive the same k, and blind-signing two different messages
// with it would leak the salted private key.
func TestCSPBlindSessionConcurrentConsume(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const racers = 8
	c.Assert(testDB.SetCSPBlindSession(testBlindUserID, testBlindElectionID, testBlindSecretK, 0), qt.IsNil)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		notFound  int
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := testDB.ConsumeCSPBlindSession(testBlindUserID, testBlindElectionID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case err == ErrCSPBlindSessionNotFound:
				notFound++
			}
		}()
	}
	wg.Wait()

	c.Assert(succeeded, qt.Equals, 1)
	c.Assert(notFound, qt.Equals, racers-1)
}

// TestCSPBlindSessionIDNoAliasing guards the session key derivation against
// append(userID, electionID...), which can write into userID's spare capacity
// and corrupt the caller's slice.
func TestCSPBlindSessionIDNoAliasing(t *testing.T) {
	c := qt.New(t)

	// a slice with spare capacity is exactly the case where append would alias
	userID := make(internal.HexBytes, 4, 64)
	copy(userID, []byte("user"))
	original := make([]byte, len(userID))
	copy(original, userID)

	electionID := internal.HexBytes([]byte("election"))
	id1 := cspBlindSessionID(userID, electionID)

	c.Assert([]byte(userID), qt.DeepEquals, original)
	// and the derivation is stable and distinguishes the two inputs
	c.Assert(cspBlindSessionID(userID, electionID), qt.DeepEquals, id1)
	c.Assert(cspBlindSessionID(electionID, userID), qt.Not(qt.DeepEquals), id1)
}
