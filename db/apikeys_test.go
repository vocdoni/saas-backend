package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

func TestAPIKeys(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	org := common.HexToAddress("0x1111111111111111111111111111111111111111")

	// unknown hash → not found
	_, err := testDB.APIKeyByHash("missing")
	c.Assert(err, qt.Equals, ErrNotFound)

	// a key without ID/Hash is rejected
	c.Assert(testDB.SetAPIKey(&APIKey{}), qt.Equals, ErrInvalidData)

	key := &APIKey{
		ID:         "key-1",
		OrgAddress: org,
		Label:      "first",
		Prefix:     "vsk_aaaabbbb",
		Hash:       "hash-1",
		Scopes:     []string{"quota:read", "managed:read"},
		CreatedBy:  "admin@example.com",
		CreatedAt:  time.Now(),
	}
	c.Assert(testDB.SetAPIKey(key), qt.IsNil)

	// lookup by hash
	got, err := testDB.APIKeyByHash("hash-1")
	c.Assert(err, qt.IsNil)
	c.Assert(got.ID, qt.Equals, "key-1")
	c.Assert(got.Scopes, qt.DeepEquals, []string{"quota:read", "managed:read"})

	// lookup by id (org-scoped)
	got, err = testDB.APIKeyByID(org, "key-1")
	c.Assert(err, qt.IsNil)
	c.Assert(got.Label, qt.Equals, "first")

	// list by org
	list, err := testDB.APIKeysByOrg(org)
	c.Assert(err, qt.IsNil)
	c.Assert(list, qt.HasLen, 1)

	// last-used tracking
	c.Assert(testDB.TouchAPIKey("key-1", time.Now()), qt.IsNil)
	got, err = testDB.APIKeyByHash("hash-1")
	c.Assert(err, qt.IsNil)
	c.Assert(got.LastUsedAt, qt.Not(qt.IsNil))

	// an expired key is treated as not found
	past := time.Now().Add(-time.Hour)
	expired := &APIKey{ID: "key-exp", OrgAddress: org, Hash: "hash-exp", ExpiresAt: &past, CreatedAt: time.Now()}
	c.Assert(testDB.SetAPIKey(expired), qt.IsNil)
	_, err = testDB.APIKeyByHash("hash-exp")
	c.Assert(err, qt.Equals, ErrNotFound)

	// revoking makes the key unauthenticable but still listable
	c.Assert(testDB.RevokeAPIKey(org, "key-1"), qt.IsNil)
	_, err = testDB.APIKeyByHash("hash-1")
	c.Assert(err, qt.Equals, ErrNotFound)
	list, err = testDB.APIKeysByOrg(org)
	c.Assert(err, qt.IsNil)
	c.Assert(list, qt.HasLen, 2) // key-1 (revoked) + key-exp

	// revoking a missing key → not found
	c.Assert(testDB.RevokeAPIKey(org, "nope"), qt.Equals, ErrNotFound)
}
