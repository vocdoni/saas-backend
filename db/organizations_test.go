package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestOrganization(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// test not found organization
	address := "childOrgToGet"
	org, _, err := db.Organization(address, false)
	c.Assert(org, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new organization with the address and a not found parent
	parentAddress := "parentOrgToGet"
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    "Child Organization",
		Parent:  parentAddress,
	}), qt.IsNil)
	// test not found parent organization
	_, parentOrg, err := db.Organization(address, true)
	c.Assert(parentOrg, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new parent organization
	c.Assert(db.SetOrganization(&Organization{
		Address: parentAddress,
		Name:    "Parent Organization",
	}), qt.IsNil)
	// test found organization and parent organization
	org, parentOrg, err = db.Organization(address, true)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(parentOrg, qt.Not(qt.IsNil))
	c.Assert(parentOrg.Address, qt.Equals, parentAddress)
}

func TestSetOrganization(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// create a new organization
	address := "orgToSet"
	orgName := "Organization"
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    orgName,
	}), qt.IsNil)
	org, _, err := db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, orgName)
	// update the organization
	orgName = "New Organization"
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    orgName,
	}), qt.IsNil)
	org, _, err = db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, orgName)
	// try to create a new organization with the same name
	newOrgAddress := "newOrgToSet"
	c.Assert(db.SetOrganization(&Organization{
		Address: newOrgAddress,
		Name:    orgName,
	}), qt.IsNotNil)
	// try to create a new organization with a not found creator
	newOrgName := "New Organization 2"
	newOrgCreator := "creator@my.org"
	newOrgCreatorPassword := "password"
	c.Assert(db.SetOrganization(&Organization{
		Address: newOrgAddress,
		Name:    newOrgName,
		Creator: newOrgCreator,
	}), qt.IsNotNil)
	// register the creator and retry to create the organization
	c.Assert(db.SetUser(&User{
		Email:    newOrgCreator,
		Password: newOrgCreatorPassword,
	}), qt.IsNil)
	c.Assert(db.SetOrganization(&Organization{
		Address: newOrgAddress,
		Name:    newOrgName,
		Creator: newOrgCreator,
	}), qt.IsNil)
}

func TestDeleteOrganization(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// create a new organization and delete it
	address := "orgToDelete"
	name := "Organization to delete"
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    name,
	}), qt.IsNil)
	org, _, err := db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, name)
	// delete the organization
	c.Assert(db.DelOrganization(org), qt.IsNil)
	// check the organization doesn't exist
	org, _, err = db.Organization(address, false)
	c.Assert(err, qt.Equals, ErrNotFound)
	c.Assert(org, qt.IsNil)
}

func TestReplaceCreatorEmail(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// create a new organization with a creator
	address := "orgToReplaceCreator"
	name := "Organization to replace creator"
	creator := "my@email.test"
	creatorPassword := "password"
	c.Assert(db.SetUser(&User{
		Email:    creator,
		Password: creatorPassword,
	}), qt.IsNil)
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    name,
		Creator: creator,
	}), qt.IsNil)
	org, _, err := db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, name)
	c.Assert(org.Creator, qt.Equals, creator)
	// replace the creator email
	newCreator := "mySecond@email.test"
	c.Assert(db.ReplaceCreatorEmail(creator, newCreator), qt.IsNil)
	org, _, err = db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, name)
	c.Assert(org.Creator, qt.Equals, newCreator)
}
