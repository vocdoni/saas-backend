package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

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
