// Package main
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// Client wraps authenticated HTTP calls to the SaaS API.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

func newClient(baseURL string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// doJSON sends an HTTP request and decodes a JSON response into target when provided.
func (c *Client) doJSON(method, path string, query url.Values, body, target any) error {
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL = fullURL + "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", method, fullURL, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request %s %s: %w", method, fullURL, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			fmt.Printf("warning: close response body: %v\n", closeErr)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, _ := io.ReadAll(resp.Body)
		trimmed := strings.TrimSpace(string(respBody))
		if trimmed == "" {
			trimmed = "no response body"
		}
		return fmt.Errorf("request %s %s failed with status %d: %s", method, fullURL, resp.StatusCode, trimmed)
	}

	if target == nil {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return fmt.Errorf("drain response body: %w", err)
		}
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, fullURL, err)
	}
	return nil
}

func (c *Client) login(email, password string) error {
	var loginResp apicommon.LoginResponse
	req := apicommon.UserInfo{
		Email:    email,
		Password: password,
	}
	if err := c.doJSON(
		http.MethodPost,
		"/auth/login",
		nil,
		req,
		&loginResp,
	); err != nil {
		return fmt.Errorf("POST /auth/login: %w", err)
	}
	if loginResp.Token == "" {
		return fmt.Errorf("POST /auth/login: empty token in response")
	}
	c.token = loginResp.Token
	return nil
}

func (c *Client) organization(address common.Address) (*apicommon.OrganizationInfo, error) {
	var resp apicommon.OrganizationInfo
	path := fmt.Sprintf("/organizations/%s", url.PathEscape(address.Hex()))
	if err := c.doJSON(http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return &resp, nil
}

func (c *Client) organizationMembers(
	address common.Address,
	search string,
	page int,
	limit int,
) (*apicommon.OrganizationMembersResponse, error) {
	query := url.Values{}
	if search != "" {
		query.Set("search", search)
	}
	if page > 0 {
		query.Set("page", strconv.Itoa(page))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var resp apicommon.OrganizationMembersResponse
	path := fmt.Sprintf("/organizations/%s/members", url.PathEscape(address.Hex()))
	if err := c.doJSON(http.MethodGet, path, query, nil, &resp); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return &resp, nil
}

func (c *Client) upsertMember(address common.Address, member apicommon.OrgMember) (string, error) {
	var resp apicommon.OrgMember
	path := fmt.Sprintf("/organizations/%s/members", url.PathEscape(address.Hex()))
	if err := c.doJSON(http.MethodPut, path, nil, member, &resp); err != nil {
		return "", fmt.Errorf("PUT %s: %w", path, err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("PUT %s: empty member ID in response", path)
	}
	return resp.ID, nil
}

func (c *Client) addMembersToCensus(censusID string, memberIDs []string) (*apicommon.AddMembersResponse, error) {
	var resp apicommon.AddMembersResponse
	path := fmt.Sprintf("/census/%s", url.PathEscape(censusID))
	req := apicommon.AddCensusParticipantsRequest{MemberIDs: memberIDs}
	if err := c.doJSON(http.MethodPost, path, nil, req, &resp); err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	return &resp, nil
}

func (c *Client) censusParticipants(censusID string) (*apicommon.CensusParticipantsResponse, error) {
	var resp apicommon.CensusParticipantsResponse
	path := fmt.Sprintf("/census/%s/participants", url.PathEscape(censusID))
	if err := c.doJSON(http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return &resp, nil
}

func (c *Client) processBundle(bundleID string) (map[string]any, error) {
	var resp map[string]any
	path := fmt.Sprintf("/process/bundle/%s", url.PathEscape(bundleID))
	if err := c.doJSON(http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return resp, nil
}

// censusIDByBundle fetches a process bundle and extracts its census identifier.
// It supports both `census.id` and `census._id` payload shapes.
func (c *Client) censusIDByBundle(bundleID string) (string, error) {
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return "", fmt.Errorf("bundle ID is empty")
	}

	bundle, err := c.processBundle(bundleID)
	if err != nil {
		return "", fmt.Errorf("retrieve bundle %s: %w", bundleID, err)
	}

	censusRaw, ok := bundle["census"]
	if !ok || censusRaw == nil {
		return "", fmt.Errorf("bundle %s missing census field", bundleID)
	}

	census, ok := censusRaw.(map[string]any)
	if !ok {
		return "", fmt.Errorf(
			"bundle %s census has unexpected type %T",
			bundleID,
			censusRaw,
		)
	}

	censusID := stringField(census, "_id")
	if censusID == "" {
		censusID = stringField(census, "id")
	}
	if censusID == "" {
		return "", fmt.Errorf(
			"bundle %s census ID not found in census._id or census.id",
			bundleID,
		)
	}
	return censusID, nil
}

func stringField(obj map[string]any, key string) string {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return ""
	}

	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		// Mongo Extended JSON representation, e.g. {"$oid":"..."}.
		if oid, ok := value["$oid"].(string); ok {
			return strings.TrimSpace(oid)
		}
	}
	return ""
}
