package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestOrgMemberPassword covers the member password over HTTP: it travels as the client hash (64
// lowercase hex characters), anything else is refused before a member is written, the hash is
// stored argon2-hashed in `pass`, and reads only report hasPassword. An empty one on an update
// clears it.
func TestOrgMemberPassword(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	membersURL := organizationMembersURL(orgAddress.String())

	t.Run("a password that is not the client hash is refused and nothing is written", func(t *testing.T) {
		c := qt.New(t)
		for _, bad := range []string{"s3cret", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
			requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, token, &apicommon.AddMembersRequest{
				Members: []apicommon.OrgMember{{MemberNumber: "OK-1"}, {MemberNumber: "BAD-1", Password: bad}},
			}, membersURL)
			requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token,
				map[string]any{"memberNumber": "BAD-2", "password": bad}, membersURL)
		}
		c.Assert(getOrgMembers(t, token, orgAddress).Members, qt.HasLen, 0)
	})

	hash := testMemberPassword("s3cret")
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "PW-1", Password: hash},
		apicommon.OrgMember{MemberNumber: "PW-2"},
	)
	c.Assert(members, qt.HasLen, 2)
	ids := make(map[string]string, len(members))
	for _, m := range members {
		ids[m.MemberNumber] = m.ID
	}
	withPassword, withoutPassword := ids["PW-1"], ids["PW-2"]

	// rawMember reads the member back as the JSON a client receives.
	rawMember := func(t *testing.T, id string) map[string]any {
		t.Helper()
		body, code := testRequest(t, http.MethodGet, token, nil, membersURL+"?limit=100")
		qt.Assert(t, code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", body))
		var resp struct {
			Members []map[string]any `json:"members"`
		}
		qt.Assert(t, json.Unmarshal(body, &resp), qt.IsNil)
		for _, m := range resp.Members {
			if m["id"] == id {
				return m
			}
		}
		t.Fatalf("member %s not listed", id)
		return nil
	}
	// hasKey reports whether the member JSON carries the key at all, whatever its value.
	hasKey := func(m map[string]any, key string) bool {
		_, ok := m[key]
		return ok
	}
	// wantPass is the stored hash of the client hash for the member: salted with its ID
	wantPass := func(t *testing.T, id string) []byte {
		t.Helper()
		oid, err := bson.ObjectIDFromHex(id)
		qt.Assert(t, err, qt.IsNil)
		return db.HashMemberPassword(passwordSalt, oid, hash)
	}
	storedPass := func(t *testing.T, id string) []byte {
		t.Helper()
		m, err := testDB.OrgMember(orgAddress, id)
		qt.Assert(t, err, qt.IsNil)
		return m.HashedPass
	}

	t.Run("the client hash is stored argon2-hashed and never returned", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(storedPass(t, withPassword), qt.DeepEquals, wantPass(t, withPassword))
		got := rawMember(t, withPassword)
		c.Assert(got["hasPassword"], qt.DeepEquals, true)
		c.Assert(hasKey(got, "password"), qt.IsFalse)
		c.Assert(hasKey(got, "pass"), qt.IsFalse)
		c.Assert(hasKey(rawMember(t, withoutPassword), "hasPassword"), qt.IsFalse)
	})

	t.Run("an update sets, keeps and clears the password", func(t *testing.T) {
		c := qt.New(t)
		requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token,
			map[string]any{"id": withoutPassword, "password": hash}, membersURL)
		c.Assert(rawMember(t, withoutPassword)["hasPassword"], qt.DeepEquals, true)
		// same password, different member: a different stored hash
		c.Assert(storedPass(t, withoutPassword), qt.DeepEquals, wantPass(t, withoutPassword))
		c.Assert(storedPass(t, withoutPassword), qt.Not(qt.DeepEquals), storedPass(t, withPassword))

		// left out: kept
		requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token,
			map[string]any{"id": withPassword, "name": "Renamed"}, membersURL)
		c.Assert(storedPass(t, withPassword), qt.DeepEquals, wantPass(t, withPassword))

		// sent empty: cleared
		requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token,
			map[string]any{"id": withPassword, "password": ""}, membersURL)
		c.Assert(storedPass(t, withPassword), qt.HasLen, 0)
		c.Assert(hasKey(rawMember(t, withPassword), "hasPassword"), qt.IsFalse)
	})
}
