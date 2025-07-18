// Package main provides a command-line tool for census workflow operations.
// It handles the creation and management of census data for voting processes
// and demonstrates the API usage for the saas-backend service.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// Config holds the script configuration
type Config struct {
	APIEndpoint string
	Email       string
	Password    string
}

// LoginRequest matches the expected login request format
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Client handles HTTP requests with authentication
type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

func newClient(baseURL string) *Client {
	return &Client{
		http:    &http.Client{},
		baseURL: baseURL,
	}
}

func (c *Client) makeRequest(method, path string, body any, target any) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("error marshaling request body: %v", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("error making request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Printf("Error closing response body: %v", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if target != nil {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			return fmt.Errorf("error decoding response: %v", err)
		}
	}

	return nil
}

func generateMembers(n int) []apicommon.OrgMember {
	// members := make([]apicommon.OrgMember, n+1)
	members := make([]apicommon.OrgMember, n)
	for i := 0; i < n; i++ {
		members[i] = apicommon.OrgMember{
			Email:        fmt.Sprintf("user%d@example.com", i+1),
			MemberNumber: fmt.Sprintf("member_%d", i+1),
			Name:         fmt.Sprintf("User %d", i+1),
		}
	}
	// Add a member with an empty value for email
	// members = append(members, apicommon.OrgMember{
	// 	Email:        "",
	// 	MemberNumber: fmt.Sprintf("member_%d", n),
	// 	Name:         fmt.Sprintf("User %d", n),
	// })
	// // Add a member with a duplicate value for email
	// members = append(members, apicommon.OrgMember{
	// 	Email:        fmt.Sprintf("user%d@example.com", n-1),
	// 	MemberNumber: fmt.Sprintf("member_%d", n-1),
	// 	Name:         fmt.Sprintf("User %d", n-1),
	// })

	return members
}

// func generateElectionMetadata() []byte {
// 	metadata := map[string]any{
// 		"title":       "Test Voting Process",
// 		"description": "This is a test voting process created by the workflow script",
// 		"startDate":   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
// 		"endDate":     time.Now().Add(72 * time.Hour).Format(time.RFC3339),
// 		"votingType":  "single-choice",
// 		"questions": []map[string]any{
// 			{
// 				"title":   "Test Question",
// 				"choices": []string{"Option A", "Option B", "Option C"},
// 			},
// 		},
// 	}

// 	metadataBytes, _ := json.Marshal(metadata)
// 	return metadataBytes
// }

func main() {
	config := Config{}
	flag.StringVar(&config.APIEndpoint, "api", "http://localhost:8080", "API endpoint URL")
	flag.StringVar(&config.Email, "email", "", "User email")
	flag.StringVar(&config.Password, "password", "", "User password")
	flag.Parse()

	if config.Email == "" || config.Password == "" {
		fmt.Println("Error: email, password are required")
		flag.Usage()
		return
	}

	client := newClient(config.APIEndpoint)

	// 1. Login
	fmt.Println("1. Logging in...")
	var loginResp apicommon.LoginResponse
	err := client.makeRequest("POST", "/auth/login", LoginRequest{
		Email:    config.Email,
		Password: config.Password,
	}, &loginResp)
	if err != nil {
		fmt.Printf("Error logging in: %v", err)
		return
	}
	client.token = loginResp.Token
	fmt.Println("✓ Login successful")

	// 2. Create organization and add members
	fmt.Println("\n2. Creating organization and adding members...")
	orgMembers := generateMembers(5) // Generate 5 test members
	var orgResp apicommon.OrganizationInfo
	err = client.makeRequest("POST", "/organizations", apicommon.OrganizationInfo{
		Size:    fmt.Sprintf("%d", len(orgMembers)),
		Type:    string(db.AssociationType),
		Country: "ES",
	}, &orgResp)
	if err != nil {
		fmt.Printf("Error creating organization: %v", err)
		return
	}
	fmt.Printf("✓ Organization created with address: %s\n", orgResp.Address)
	var addMembersResp apicommon.AddMembersResponse
	err = client.makeRequest(
		"POST",
		fmt.Sprintf("/organizations/%s/members", orgResp.Address),
		apicommon.AddMembersRequest{
			Members: orgMembers,
		},
		&addMembersResp,
	)
	if err != nil {
		fmt.Printf("Error adding members: %v", err)
		return
	}
	// wait for members to be added
	if len(addMembersResp.JobID) > 0 {
		fmt.Printf("Members are being added asynchronously, job ID: %s\n", addMembersResp.JobID)
		for {
			var jobResp db.BulkOrgMembersJob
			err = client.makeRequest("GET", fmt.Sprintf("/jobs/%s", addMembersResp.JobID), nil, &jobResp)
			if err != nil {
				fmt.Printf("Error checking job status: %v\n", err)
				return
			}
			if jobResp.Progress == 100 {
				fmt.Printf("Members added asynchronously, %d members added\n", jobResp.Added)
				break
			}
			fmt.Printf("Waiting for members to be added... Progress: %d%%\n", jobResp.Progress)
			// Sleep for a while before checking again
			time.Sleep(5 * time.Second)
		}
	} else {
		fmt.Printf("Members added synchronously, %d members added\n", addMembersResp.Added)
	}

	// get orgmembers to verify
	var orgMembersResp apicommon.OrganizationMembersResponse
	err = client.makeRequest("GET", fmt.Sprintf("/organizations/%s/members", orgResp.Address), nil, &orgMembersResp)
	if err != nil {
		fmt.Printf("Error getting organization members: %v", err)
		return
	}
	if len(orgMembersResp.Members) != len(orgMembers) {
		fmt.Printf("Error: expected %d members, got %d\n", len(orgMembers), len(orgMembersResp.Members))
		return
	}
	fmt.Printf("✓ Added %d members to organization %s\n", len(orgMembersResp.Members), orgResp.Address)

	// 4 Create a group of members
	fmt.Println("\n4. Creating group of members...")
	groupName := "Test Group"
	groupMembersIDs := make([]string, 0, len(orgMembersResp.Members))
	for _, member := range orgMembersResp.Members {
		groupMembersIDs = append(groupMembersIDs, member.ID)
	}
	var groupResp apicommon.OrganizationMemberGroupInfo
	// Create group with all members
	err = client.makeRequest(
		"POST",
		fmt.Sprintf("/organizations/%s/groups", orgResp.Address),
		apicommon.CreateOrganizationMemberGroupRequest{
			Title:     groupName,
			MemberIDs: groupMembersIDs,
		},
		&groupResp,
	)
	if err != nil {
		fmt.Printf("Error creating group: %v", err)
		return
	}

	// 4. Create a census for the group
	fmt.Printf("\n4. Creating census for the group %s and org %s\n", groupResp.ID, orgResp.Address)
	var createCensusResp apicommon.CreateCensusResponse
	err = client.makeRequest(
		"POST",
		"/census",
		apicommon.CreateCensusRequest{
			OrgAddress: orgResp.Address,
			GroupID:    groupResp.ID,
			AuthFields: db.OrgMemberAuthFields{
				"email",
				"memberNumber",
			},
		}, &createCensusResp,
	)
	// Print the census ID and member warnings
	if err != nil {
		fmt.Printf("Error creating census: %v", err)
		return
	}

	fmt.Printf("✓ Census created with ID: %s\n", createCensusResp.ID)

	// 3. Get census info

	// 5. Publish census

	// 6. Create process

	// 7. Get process info
}
