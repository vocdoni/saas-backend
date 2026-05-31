package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestMemberMatchesIdentifier(t *testing.T) {
	c := qt.New(t)

	member := apicommon.OrgMember{
		Email:        "person@example.com",
		NationalID:   "1234",
		MemberNumber: "M-99",
	}

	cases := []struct {
		name       string
		member     apicommon.OrgMember
		idField    string
		identifier string
		want       bool
	}{
		{"email match", member, identifierFieldEmail, "person@example.com", true},
		{
			"email trims surrounding space",
			apicommon.OrgMember{Email: "  person@example.com  "},
			identifierFieldEmail, "person@example.com", true,
		},
		{"email case-sensitive mismatch", member, identifierFieldEmail, "Person@example.com", false},
		{"email no match", member, identifierFieldEmail, "other@example.com", false},
		{"nationalId match", member, identifierFieldNationalID, "1234", true},
		{"nationalId mismatch", member, identifierFieldNationalID, "5678", false},
		{"memberNumber match", member, identifierFieldMemberNumber, "M-99", true},
		{"unknown field never matches", member, "unknown", "person@example.com", false},
	}

	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			c.Assert(memberMatchesIdentifier(tc.member, tc.idField, tc.identifier), qt.Equals, tc.want)
		})
	}
}

func TestFindMemberByIdentifierEscapesSearchTerm(t *testing.T) {
	c := qt.New(t)

	orgAddress := common.HexToAddress("0x3333333333333333333333333333333333333333")
	identifier := "a.b+x@example.com"

	var gotSearch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.Method, qt.Equals, http.MethodGet)
		c.Assert(r.URL.Path, qt.Equals, "/organizations/"+orgAddress.Hex()+"/members")
		gotSearch = r.URL.Query().Get("search")

		w.Header().Set("Content-Type", "application/json")
		c.Assert(json.NewEncoder(w).Encode(apicommon.OrganizationMembersResponse{
			Members: []apicommon.OrgMember{{ID: "member-id-1", Email: identifier}},
		}), qt.IsNil)
	}))
	defer server.Close()

	client := newClient(server.URL)
	found, err := findMemberByIdentifier(client, orgAddress, identifierFieldEmail, identifier)

	c.Assert(err, qt.IsNil)
	c.Assert(found, qt.IsNotNil)
	c.Assert(found.ID, qt.Equals, "member-id-1")
	// The server-side `search` param is a regex; metacharacters must be escaped
	// so the email is matched literally instead of as a pattern.
	c.Assert(gotSearch, qt.Equals, regexp.QuoteMeta(identifier))
}

func TestUpdateExistingMemberFromRowSkipReturnsPersistedMember(t *testing.T) {
	c := qt.New(t)

	existing := &apicommon.OrgMember{
		ID:         "member-id-1",
		Email:      "old@example.com",
		Phone:      "abcdef***",
		Name:       "Old",
		NationalID: "1234",
	}

	row := csvRow{
		Line:       2,
		Identifier: existing.NationalID,
		Values: map[string]string{
			"email": "new@example.com",
			"name":  "New",
		},
	}

	stdin := bufio.NewReader(strings.NewReader("n\n"))
	result, err := updateExistingMemberFromRow(
		nil,
		&organizationContext{},
		Config{IDField: identifierFieldNationalID},
		row,
		existing,
		stdin,
	)

	c.Assert(err, qt.IsNil)
	c.Assert(result.Skipped, qt.IsTrue)
	c.Assert(result.Updated, qt.IsFalse)
	c.Assert(result.Member, qt.DeepEquals, *existing)
}

func TestUpdateExistingMemberFromRowAcceptNoPhoneChanges(t *testing.T) {
	c := qt.New(t)

	orgAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	org := &organizationContext{Address: orgAddress, Country: "ES"}
	existing := &apicommon.OrgMember{
		ID:         "member-id-1",
		Email:      "old@example.com",
		Phone:      "abcdef***",
		Name:       "Old",
		NationalID: "1234",
	}

	row := csvRow{
		Line:       2,
		Identifier: existing.NationalID,
		Values: map[string]string{
			"email": "new@example.com",
			"name":  "New",
		},
	}

	var received apicommon.OrgMember
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.Method, qt.Equals, http.MethodPut)
		c.Assert(r.URL.Path, qt.Equals, "/organizations/"+orgAddress.Hex()+"/members")

		body, err := io.ReadAll(r.Body)
		c.Assert(err, qt.IsNil)
		c.Assert(json.Unmarshal(body, &received), qt.IsNil)

		w.Header().Set("Content-Type", "application/json")
		c.Assert(json.NewEncoder(w).Encode(apicommon.OrgMember{ID: existing.ID}), qt.IsNil)
	}))
	defer server.Close()

	client := newClient(server.URL)
	result, err := updateExistingMemberFromRow(
		client,
		org,
		Config{IDField: identifierFieldNationalID, Yes: true},
		row,
		existing,
		bufio.NewReader(strings.NewReader("")),
	)

	c.Assert(err, qt.IsNil)
	c.Assert(result.Updated, qt.IsTrue)
	c.Assert(result.Skipped, qt.IsFalse)
	c.Assert(result.Member.Email, qt.Equals, "new@example.com")
	c.Assert(result.Member.Name, qt.Equals, "New")
	c.Assert(result.Member.Phone, qt.Equals, existing.Phone)

	// With no phone change, request payload clears phone to preserve persisted masked phone.
	c.Assert(received.Phone, qt.Equals, "")
	c.Assert(received.Email, qt.Equals, "new@example.com")
}

func TestUpdateExistingMemberFromRowAcceptPhoneChanges(t *testing.T) {
	c := qt.New(t)

	orgAddress := common.HexToAddress("0x2222222222222222222222222222222222222222")
	org := &organizationContext{Address: orgAddress, Country: "ES"}

	oldPhone := "+34600000001"
	maskedOldPhone, err := maskedPhone(oldPhone, org.Address, org.Country)
	c.Assert(err, qt.IsNil)

	existing := &apicommon.OrgMember{
		ID:         "member-id-2",
		Email:      "old@example.com",
		Phone:      maskedOldPhone,
		Name:       "Old",
		NationalID: "5678",
	}

	newPhone := "+34600000002"
	row := csvRow{
		Line:       3,
		Identifier: existing.NationalID,
		Values: map[string]string{
			"phone": newPhone,
			"email": "new@example.com",
		},
	}

	var received apicommon.OrgMember
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		c.Assert(readErr, qt.IsNil)
		c.Assert(json.Unmarshal(body, &received), qt.IsNil)

		w.Header().Set("Content-Type", "application/json")
		c.Assert(json.NewEncoder(w).Encode(apicommon.OrgMember{ID: existing.ID}), qt.IsNil)
	}))
	defer server.Close()

	client := newClient(server.URL)
	result, err := updateExistingMemberFromRow(
		client,
		org,
		Config{IDField: identifierFieldNationalID, Yes: true},
		row,
		existing,
		bufio.NewReader(strings.NewReader("")),
	)

	c.Assert(err, qt.IsNil)
	c.Assert(result.Updated, qt.IsTrue)
	c.Assert(result.Skipped, qt.IsFalse)
	c.Assert(result.Member.Phone, qt.Equals, newPhone)
	c.Assert(received.Phone, qt.Equals, newPhone)
	c.Assert(received.Email, qt.Equals, "new@example.com")
}
