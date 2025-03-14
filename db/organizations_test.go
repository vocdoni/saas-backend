package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestOrganizations(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
	t.Run("GetOrganization", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// test not found organization
		address := "childOrgToGet"
		org, _, err := testDB.Organization(address, false)
		c.Assert(org, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new organization with the address and a not found parent
		parentAddress := "parentOrgToGet"
		c.Assert(testDB.SetOrganization(&Organization{
			Address:      address,
			Parent:       parentAddress,
			Subscription: OrganizationSubscription{},
		}), qt.IsNil)
		// test not found parent organization
		_, parentOrg, err := testDB.Organization(address, true)
		c.Assert(parentOrg, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new parent organization
		c.Assert(testDB.SetOrganization(&Organization{
			Address: parentAddress,
		}), qt.IsNil)
		// test found organization and parent organization
		org, parentOrg, err = testDB.Organization(address, true)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		c.Assert(parentOrg, qt.Not(qt.IsNil))
		c.Assert(parentOrg.Address, qt.Equals, parentAddress)
	})

	t.Run("SetOrganization", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// create a new organization
		address := "orgToSet"
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, _, err := testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		// update the organization
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, _, err = testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		// try to create a new organization with a not found creator
		newOrgAddress := "newOrgToSet"
		c.Assert(testDB.SetOrganization(&Organization{
			Address: newOrgAddress,
			Creator: testUserEmail,
		}), qt.IsNotNil)
		// register the creator and retry to create the organization
		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{
			Address: newOrgAddress,
			Creator: testUserEmail,
		}), qt.IsNil)
	})

	t.Run("DeleteOrganization", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// create a new organization and delete it
		address := "orgToDelete"
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, _, err := testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		// delete the organization
		c.Assert(testDB.DelOrganization(org), qt.IsNil)
		// check the organization doesn't exist
		org, _, err = testDB.Organization(address, false)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(org, qt.IsNil)
	})

	t.Run("ReplaceCreatorEmail", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// create a new organization with a creator
		address := "orgToReplaceCreator"
		_, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
			Creator: testUserEmail,
		}), qt.IsNil)
		org, _, err := testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		c.Assert(org.Creator, qt.Equals, testUserEmail)
		// replace the creator email
		newCreator := "mySecond@email.test"
		c.Assert(testDB.ReplaceCreatorEmail(testUserEmail, newCreator), qt.IsNil)
		org, _, err = testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		c.Assert(org.Creator, qt.Equals, newCreator)
	})

	t.Run("OrganizationsMembers", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// create a new organization with a creator
		address := "orgToReplaceCreator"
		_, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
			Creator: testUserEmail,
		}), qt.IsNil)
		_, _, err = testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		// get the organization members
		members, err := testDB.OrganizationsMembers(address)
		c.Assert(err, qt.IsNil)
		c.Assert(members, qt.HasLen, 1)
		singleMember := members[0]
		c.Assert(singleMember.Email, qt.Equals, testUserEmail)
	})

	t.Run("AddOrganizationPlan", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// create a new organization
		address := "orgToAddPlan"
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		// add a subscription to the organization
		subscriptionName := "testPlan"
		startDate := time.Now()
		active := true
		stripeID := "stripeID"
		orgSubscription := &OrganizationSubscription{
			PlanID:    100,
			StartDate: startDate,
			Active:    true,
		}
		// using a non existing subscription should fail
		c.Assert(testDB.SetOrganizationSubscription(address, orgSubscription), qt.IsNotNil)
		subscriptionID, err := testDB.SetPlan(&Plan{
			Name:     subscriptionName,
			StripeID: stripeID,
		})
		if err != nil {
			t.Error(err)
		}
		orgSubscription.PlanID = subscriptionID
		c.Assert(testDB.SetOrganizationSubscription(address, orgSubscription), qt.IsNil)
		// retrieve the organization and check the subscription details
		org, _, err := testDB.Organization(address, false)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.Equals, address)
		c.Assert(org.Subscription.Active, qt.Equals, active)
	})
}
