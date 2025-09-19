package handlers

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
)

var (
	testOrgAddress = common.Address{0x01, 0x23, 0x45, 0x67, 0x89}
	spanishOrg     = &db.Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
		Country:   "ES",
	}

	frenchOrg = &db.Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
		Country:   "FR",
	}
)

func TestHandlePhoneContact(t *testing.T) {
	c := qt.New(t)
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		org     *db.Organization
		phone   string
		want    string
		wantErr bool
	}{
		{name: "spanishOrg", org: spanishOrg, phone: "612345601", want: "+34612345601"},
		{name: "frenchOrg", org: frenchOrg, phone: "612345601", want: "+33612345601"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(*testing.T) {
			hashedPhone, err := db.NewHashedPhone(tt.phone, tt.org)
			c.Assert(err, qt.IsNil)

			phone, challengeType, err := handlePhoneContact(tt.org, tt.phone, hashedPhone)
			c.Assert(err, qt.IsNil)
			c.Assert(phone, qt.Equals, tt.want)
			c.Assert(challengeType, qt.Equals, notifications.SMSChallenge)
		})
	}
}
