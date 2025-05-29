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
	"math/rand"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

// Config holds the script configuration
type Config struct {
	APIEndpoint string
	Email       string
	Password    string
	OrgAddress  string
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
	members := make([]apicommon.OrgMember, n)
	for i := 0; i < n; i++ {
		members[i] = apicommon.OrgMember{
			Email:    fmt.Sprintf("user%d@example.com", i+1),
			Phone:    fmt.Sprintf("+%010d", rand.Int63n(10000000000)),
			MemberNo: fmt.Sprintf("member_%d", i+1),
			Name:     fmt.Sprintf("User %d", i+1),
		}
	}
	return members
}

func generateMetadata() []byte {
	metadata := map[string]any{
		"title":       "Test Voting Process",
		"description": "This is a test voting process created by the workflow script",
		"startDate":   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		"endDate":     time.Now().Add(72 * time.Hour).Format(time.RFC3339),
		"votingType":  "single-choice",
		"questions": []map[string]any{
			{
				"title":   "Test Question",
				"choices": []string{"Option A", "Option B", "Option C"},
			},
		},
	}

	metadataBytes, _ := json.Marshal(metadata)
	return metadataBytes
}

func main() {
	config := Config{}
	flag.StringVar(&config.APIEndpoint, "api", "http://localhost:8080", "API endpoint URL")
	flag.StringVar(&config.Email, "email", "", "User email")
	flag.StringVar(&config.Password, "password", "", "User password")
	flag.StringVar(&config.OrgAddress, "org", "", "Organization address")
	flag.Parse()

	if config.Email == "" || config.Password == "" || config.OrgAddress == "" {
		fmt.Println("Error: email, password, and organization address are required")
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

	// 2. Create census
	fmt.Println("\n2. Creating census...")
	var censusID string
	err = client.makeRequest("POST", "/census", apicommon.OrganizationCensus{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: config.OrgAddress,
	}, &censusID)
	if err != nil {
		fmt.Printf("Error creating census: %v", err)
		return
	}
	fmt.Printf("✓ Census created with ID: %s\n", censusID)

	// 3. Get census info
	fmt.Println("\n3. Getting census info...")
	var census db.Census
	err = client.makeRequest("GET", fmt.Sprintf("/census/%s", censusID), nil, &census)
	if err != nil {
		fmt.Printf("Error getting census info: %v", err)
		return
	}
	fmt.Printf("✓ Census info retrieved: %+v\n", census)

	// 4. Add members
	fmt.Println("\n4. Adding members...")
	members := generateMembers(10)
	err = client.makeRequest("POST", fmt.Sprintf("/census/%s", censusID), apicommon.AddMembersRequest{
		Members: members,
	}, nil)
	if err != nil {
		fmt.Printf("Error adding members: %v", err)
		return
	}
	fmt.Println("✓ Members added successfully")

	// 5. Publish census
	fmt.Println("\n5. Publishing census...")
	var publishedCensus db.PublishedCensus
	err = client.makeRequest("POST", fmt.Sprintf("/census/%s/publish", censusID), nil, &publishedCensus)
	if err != nil {
		fmt.Printf("Error publishing census: %v", err)
		return
	}
	fmt.Printf("✓ Census published with URI: %s\n", publishedCensus.URI)

	// 6. Create process
	fmt.Println("\n6. Creating process...")
	processID := fmt.Sprintf("test_process_%d", time.Now().Unix())
	metadata := generateMetadata()
	root := new(internal.HexBytes).SetString(publishedCensus.Root)
	err = client.makeRequest("POST", fmt.Sprintf("/process/%s", processID), apicommon.CreateProcessRequest{
		PublishedCensusRoot: *root,
		Metadata:            metadata,
	}, nil)
	if err != nil {
		fmt.Printf("Error creating process: %v", err)
		return
	}
	fmt.Printf("✓ Process created with ID: %s\n", processID)

	// 7. Get process info
	fmt.Println("\n7. Getting process info...")
	var process db.Process
	err = client.makeRequest("GET", fmt.Sprintf("/process/%s", processID), nil, &process)
	if err != nil {
		fmt.Printf("Error getting process info: %v", err)
		return
	}
	fmt.Printf("✓ Process info retrieved: %+v\n", process)

	fmt.Println("\n✨ Workflow completed successfully!")
}
