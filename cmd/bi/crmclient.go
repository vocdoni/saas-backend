package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CRMClient represents a client for the Holded CRM API
type CRMClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// CRMClient creates a new Holded API client
func NewCRMClient(apiKey, baseURL string) *CRMClient {
	return &CRMClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

// Funnel represents a funnel in the Holded CRM
type Funnel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Stages []struct {
		StageID string `json:"stageId"`
		Key     string `json:"key"`
		Name    string `json:"name"`
		Desc    string `json:"desc"`
	} `json:"stages"`
}

type Contact struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
	Type  string `json:"type"`
}

// ContactResponse represents the response from creating a contact
type ContactResponse struct {
	Status int    `json:"status"`
	Info   string `json:"info"`
	ID     string `json:"id"`
}

// LeadResponse represents the response from creating a lead
type LeadResponse struct {
	Status int    `json:"status"`
	Info   string `json:"info"`
	ID     string `json:"id"`
}

// GetLeadsFunnelID retrieves the list of funnels and returns the ID of the funnel named "Leads"
func (c *CRMClient) GetLeadsFunnelStageID() (string, string, error) {
	url := fmt.Sprintf("%s/crm/v1/funnels", c.baseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response body: %w", err)
	}

	var funnels []Funnel
	if err := json.Unmarshal(body, &funnels); err != nil {
		return "", "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Find the funnel named "Leads"
	for _, funnel := range funnels {
		if funnel.Name == "Sales" {
			for _, stage := range funnel.Stages {
				if stage.Name == "Lead" {
					return funnel.ID, stage.StageID, nil
				}
			}
			return funnel.ID, "", nil
		}
	}

	return "", "", fmt.Errorf("funnel named 'Sales' with stage named 'Lead' not found")
}

// CreateContact creates a new contact in Holded and returns the contact ID
func (c *CRMClient) CreateContact(name, email, phone string) (string, error) {
	url := fmt.Sprintf("%s/invoicing/v1/contacts", c.baseURL)

	contactData := &Contact{
		Name:  name,
		Email: email,
		Phone: phone,
		Type:  "lead",
	}

	jsonData, err := json.Marshal(contactData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal contact data: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var contactResp ContactResponse
	if err := json.Unmarshal(body, &contactResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if contactResp.ID == "" {
		return "", fmt.Errorf("contact ID not returned in response")
	}

	return contactResp.ID, nil
}

// CreateLead creates a new lead in Holded for the given contact and funnel
func (c *CRMClient) CreateLead(contactID, funnelID, stageID string) (string, error) {
	url := fmt.Sprintf("%s/crm/v1/leads", c.baseURL)

	leadData := map[string]interface{}{
		"funnelId":  funnelID,
		"stageId":   stageID,
		"contactId": contactID,
	}

	jsonData, err := json.Marshal(leadData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal lead data: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var leadResp LeadResponse
	if err := json.Unmarshal(body, &leadResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if leadResp.ID == "" {
		return "", fmt.Errorf("lead ID not returned in response")
	}

	return leadResp.ID, nil
}

// HandleLead receives lead information and creates the contact and lead in the CRM
func (c *CRMClient) HandleLead(name, email, phone, funnelID, stageID string) (string, error) {
	contactID, err := c.CreateContact(name, email, phone)
	if err != nil {
		return "", fmt.Errorf("failed to create contact: %w", err)
	}

	leadID, err := c.CreateLead(contactID, funnelID, stageID)
	if err != nil {
		return "", fmt.Errorf("failed to create lead: %w", err)
	}

	return leadID, nil
}
