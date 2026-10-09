package main

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

func TestValidateSetIntegratorArgs(t *testing.T) {
	c := qt.New(t)
	const addr = "0x00000000000000000000000000000000000000aa"

	got, err := validateSetIntegratorArgs(addr, 5)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, common.HexToAddress(addr))

	for _, tc := range []struct {
		name    string
		address string
		maxOrgs int
		errMsg  string
	}{
		{"missing address", "", 5, "orgAddress is required"},
		{"invalid address", "nothex", 5, "invalid orgAddress.*"},
		{"maxManagedOrgs omitted", addr, 0, "maxManagedOrgs must be greater than 0.*"},
		{"negative maxManagedOrgs", addr, -1, "maxManagedOrgs must be greater than 0.*"},
	} {
		c.Run(tc.name, func(c *qt.C) {
			_, err := validateSetIntegratorArgs(tc.address, tc.maxOrgs)
			c.Assert(err, qt.ErrorMatches, tc.errMsg)
		})
	}
}
