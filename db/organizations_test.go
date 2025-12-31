package db

import (
	"encoding/json"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	vocapi "go.vocdoni.io/dvote/api"
)

func TestOrganizations(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	t.Run("GetOrganization", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// test not found organization
		address := testAnotherOrgAddress
		org, err := testDB.Organization(address)
		c.Assert(org, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new organization with the address and a not found parent
		parentAddress := testNonExistentOrg
		c.Assert(testDB.SetOrganization(&Organization{
			Address:      address,
			Parent:       parentAddress,
			Subscription: OrganizationSubscription{},
		}), qt.IsNil)
		// test not found parent organization
		_, parentOrg, err := testDB.OrganizationWithParent(address)
		c.Assert(parentOrg, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new parent organization
		c.Assert(testDB.SetOrganization(&Organization{
			Address: parentAddress,
		}), qt.IsNil)
		// test found organization and parent organization
		org, parentOrg, err = testDB.OrganizationWithParent(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		c.Assert(parentOrg, qt.Not(qt.IsNil))
		c.Assert(parentOrg.Address, qt.DeepEquals, parentAddress)
	})

	t.Run("SetOrganization", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new organization
		address := testOrgAddress
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, err := testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		// update the organization
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, err = testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		// try to create a new organization with a not found creator
		newOrgAddress := testAnotherOrgAddress
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
		validMetadataJSON := `{
			"version": "1.0",  
			"name": {  
				"default": "TestCSPORg"
			},  
			"description": {  
				"default": ""
			},  
			"newsFeed": {  
				"default": ""
			},  
			"media": {
				"avatar": "https://example.com/logo.png"
			},  
			"meta": {} 
		}`
		var validMetadata vocapi.AccountMetadata
		err = json.Unmarshal([]byte(validMetadataJSON), &validMetadata)
		c.Assert(err, qt.IsNil)
		org.Meta["name"], org.Meta["logo"] = ParseVochainOrganizationMeta(&validMetadata)
		c.Assert(testDB.SetOrganization(org), qt.IsNil)
		org, err = testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		c.Assert(org.Meta["name"], qt.Equals, "TestCSPORg")
		c.Assert(org.Meta["logo"], qt.Equals, "https://example.com/logo.png")
	})

	t.Run("DeleteOrganization", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new organization and delete it
		address := testOrgAddress
		c.Assert(testDB.SetOrganization(&Organization{
			Address: address,
		}), qt.IsNil)
		org, err := testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		// delete the organization
		c.Assert(testDB.DelOrganization(org), qt.IsNil)
		// check the organization doesn't exist
		org, err = testDB.Organization(address)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(org, qt.IsNil)
	})

	t.Run("ReplaceCreatorEmail", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new organization with a creator
		address := testOrgAddress
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
		org, err := testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		c.Assert(org.Creator, qt.Equals, testUserEmail)
		// replace the creator email
		newCreator := "mySecond@email.test"
		c.Assert(testDB.ReplaceCreatorEmail(testUserEmail, newCreator), qt.IsNil)
		org, err = testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		c.Assert(org.Creator, qt.Equals, newCreator)
	})

	t.Run("OrganizationsUsers", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new organization with a creator
		address := testOrgAddress
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
		_, err = testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		// get the organization users
		users, err := testDB.OrganizationUsers(address)
		c.Assert(err, qt.IsNil)
		c.Assert(users, qt.HasLen, 1)
		singleUser := users[0]
		c.Assert(singleUser.Email, qt.Equals, testUserEmail)
	})

	t.Run("AddOrganizationPlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new organization
		address := testOrgAddress
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
		org, err := testDB.Organization(address)
		c.Assert(err, qt.IsNil)
		c.Assert(org, qt.Not(qt.IsNil))
		c.Assert(org.Address, qt.DeepEquals, address)
		c.Assert(org.Subscription.Active, qt.Equals, active)
	})

	t.Run("UpdateOrganizationUserRole", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Create a test user
		userEmail := "roletest@example.com"
		userID, err := testDB.SetUser(&User{
			Email:     userEmail,
			Password:  testDBUserPass,
			FirstName: testDBFirstName,
			LastName:  testDBLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(userID, qt.Not(qt.Equals), uint64(0))

		// Create a test organization
		orgAddress := testOrgAddress
		c.Assert(testDB.SetOrganization(&Organization{
			Address: orgAddress,
			Creator: userEmail, // This will add the user as admin
		}), qt.IsNil)

		// Verify the user is initially an admin
		user, err := testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.Organizations[0].Address, qt.DeepEquals, orgAddress)
		c.Assert(user.Organizations[0].Role, qt.Equals, AdminRole)

		// Update the role from Admin to Manager
		c.Assert(testDB.UpdateOrganizationUserRole(orgAddress, userID, ManagerRole), qt.IsNil)

		// Verify the role was updated
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.Organizations[0].Address, qt.DeepEquals, orgAddress)
		c.Assert(user.Organizations[0].Role, qt.Equals, ManagerRole)

		// Update the role from Manager to Viewer
		c.Assert(testDB.UpdateOrganizationUserRole(orgAddress, userID, ViewerRole), qt.IsNil)

		// Verify the role was updated
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.Organizations[0].Address, qt.DeepEquals, orgAddress)
		c.Assert(user.Organizations[0].Role, qt.Equals, ViewerRole)

		// Test updating a non-existent user
		nonExistentUserID := uint64(9999)
		err = testDB.UpdateOrganizationUserRole(orgAddress, nonExistentUserID, AdminRole)
		// The function doesn't return an error for non-existent users, it just doesn't update anything
		c.Assert(err, qt.IsNil)

		// Test updating a user for a non-existent organization
		err = testDB.UpdateOrganizationUserRole(testNonExistentOrg, userID, AdminRole)
		// The function doesn't return an error for non-existent organizations, it just doesn't update anything
		c.Assert(err, qt.IsNil)

		// Verify the user's role hasn't changed after the non-existent org update
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.Organizations[0].Address, qt.DeepEquals, orgAddress)
		c.Assert(user.Organizations[0].Role, qt.Equals, ViewerRole)
	})

	t.Run("RemoveOrganizationUser", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Create a test user
		userEmail := "usertest@example.com"
		userID, err := testDB.SetUser(&User{
			Email:     userEmail,
			Password:  testDBUserPass,
			FirstName: testDBFirstName,
			LastName:  testDBLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(userID, qt.Not(qt.Equals), uint64(0))

		// Create a test organization
		orgAddress := testOrgAddress
		c.Assert(testDB.SetOrganization(&Organization{
			Address: orgAddress,
			Creator: userEmail, // This will add the user as admin
		}), qt.IsNil)

		// Verify the user is initially a user
		user, err := testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.Organizations[0].Address, qt.DeepEquals, orgAddress)

		// Remove the user from the organization
		c.Assert(testDB.RemoveOrganizationUser(orgAddress, userID), qt.IsNil)

		// Verify the user is not tied to any organization now
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Organizations, qt.HasLen, 0)

		// Test removing a non-existent user
		nonExistentUserID := uint64(9999)
		err = testDB.RemoveOrganizationUser(orgAddress, nonExistentUserID)
		// The function doesn't return an error for non-existent users, it just doesn't remove anything
		c.Assert(err, qt.IsNil)

		// Create another user and add them to multiple organizations
		secondUserEmail := "seconduser@example.com"
		secondUserID, err := testDB.SetUser(&User{
			Email:     secondUserEmail,
			Password:  testDBUserPass,
			FirstName: testDBFirstName,
			LastName:  testDBLastName,
		})
		c.Assert(err, qt.IsNil)

		// Create two organizations for the second user
		firstOrgAddress := testOrgAddress
		secondOrgAddress := testAnotherOrgAddress

		c.Assert(testDB.SetOrganization(&Organization{
			Address: firstOrgAddress,
			Creator: secondUserEmail,
		}), qt.IsNil)

		c.Assert(testDB.SetOrganization(&Organization{
			Address: secondOrgAddress,
			Creator: secondUserEmail,
		}), qt.IsNil)

		// Verify the second user is a user of both organizations
		secondUser, err := testDB.User(secondUserID)
		c.Assert(err, qt.IsNil)
		c.Assert(secondUser.Organizations, qt.HasLen, 2)

		// Remove the second user from the first organization
		c.Assert(testDB.RemoveOrganizationUser(firstOrgAddress, secondUserID), qt.IsNil)

		// Verify the second user is now only a user of the second organization
		secondUser, err = testDB.User(secondUserID)
		c.Assert(err, qt.IsNil)
		c.Assert(secondUser.Organizations, qt.HasLen, 1)
		c.Assert(secondUser.Organizations[0].Address, qt.DeepEquals, secondOrgAddress)
	})
}
