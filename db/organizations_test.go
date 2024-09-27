package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestOrganization(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// test not found organization
	address := "childOrgToGet"
	org, _, err := db.Organization(address, false)
	c.Assert(org, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new organization with the address and a not found parent
	parentAddress := "parentOrgToGet"
	c.Assert(db.SetOrganization(&Organization{
		Address:      address,
		Name:         "Child Organization",
		Parent:       parentAddress,
		Subscription: OrganizationSubscription{},
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
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
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
	c.Assert(db.SetOrganization(&Organization{
		Address: newOrgAddress,
		Name:    newOrgName,
		Creator: testUserEmail,
	}), qt.IsNotNil)
	// register the creator and retry to create the organization
	_, err = db.SetUser(&User{
		Email:    testUserEmail,
		Password: testUserPass,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.SetOrganization(&Organization{
		Address: newOrgAddress,
		Name:    newOrgName,
		Creator: testUserEmail,
	}), qt.IsNil)
}

func TestDeleteOrganization(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
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
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// create a new organization with a creator
	address := "orgToReplaceCreator"
	name := "Organization to replace creator"
	_, err := db.SetUser(&User{
		Email:    testUserEmail,
		Password: testUserPass,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    name,
		Creator: testUserEmail,
	}), qt.IsNil)
	org, _, err := db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, name)
	c.Assert(org.Creator, qt.Equals, testUserEmail)
	// replace the creator email
	newCreator := "mySecond@email.test"
	c.Assert(db.ReplaceCreatorEmail(testUserEmail, newCreator), qt.IsNil)
	org, _, err = db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, name)
	c.Assert(org.Creator, qt.Equals, newCreator)
}

func TestOrganizationsMembers(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// create a new organization with a creator
	address := "orgToReplaceCreator"
	name := "Organization to replace creator"
	_, err := db.SetUser(&User{
		Email:    testUserEmail,
		Password: testUserPass,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    name,
		Creator: testUserEmail,
	}), qt.IsNil)
	_, _, err = db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	// get the organization members
	members, err := db.OrganizationsMembers(address)
	c.Assert(err, qt.IsNil)
	c.Assert(members, qt.HasLen, 1)
	singleMember := members[0]
	c.Assert(singleMember.Email, qt.Equals, testUserEmail)
}
func TestAddOrganizationSubscription(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// create a new organization
	address := "orgToAddSubscription"
	orgName := "Organization"
	c.Assert(db.SetOrganization(&Organization{
		Address: address,
		Name:    orgName,
	}), qt.IsNil)
	// add a subscription to the organization
	subscriptionName := "testSubscription"
	startDate := time.Now()
	endDate := startDate.AddDate(1, 0, 0)
	active := true
	stripeID := "stripeID"
	orgSubscription := &OrganizationSubscription{
		StartDate: startDate,
		EndDate:   endDate,
		Active:    true,
	}
	// using a non existing subscription should fail
	c.Assert(db.AddSubscriptionToOrganization(address, orgSubscription), qt.IsNotNil)
	subscriptionID, err := db.SetSubscription(&Subscription{
		Name:     subscriptionName,
		StripeID: stripeID,
	})
	if err != nil {
		t.Error(err)
	}
	orgSubscription.SubscriptionID = subscriptionID
	c.Assert(db.AddSubscriptionToOrganization(address, orgSubscription), qt.IsNil)
	// retrieve the organization and check the subscription details
	org, _, err := db.Organization(address, false)
	c.Assert(err, qt.IsNil)
	c.Assert(org, qt.Not(qt.IsNil))
	c.Assert(org.Address, qt.Equals, address)
	c.Assert(org.Name, qt.Equals, orgName)
	c.Assert(org.Subscription.Active, qt.Equals, active)
}
