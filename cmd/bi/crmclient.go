package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.vocdoni.io/dvote/log"
)

const (
	// httpClientTimeout is the default timeout for HTTP requests to CRM API
	httpClientTimeout = 30 * time.Second
)

// CRMClient represents a client for the Holded CRM API
type CRMClient struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewCRMClient creates a new Holded CRM API client with configured timeout
func NewCRMClient(apiKey, baseURL string) *CRMClient {
	return &CRMClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: httpClientTimeout,
		},
	}
}

type stage struct {
	StageID string `json:"stageId"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Desc    string `json:"desc"`
}

// Funnel represents a funnel in the Holded CRM
type funnel struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Stages []stage `json:"stages"`
}

type contact struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Type        string `json:"type"`
	CountryCode string `json:"countryCode,omitempty"`
}

// ContactResponse represents the response from creating a contact
type contactResponse struct {
	Status int    `json:"status"`
	Info   string `json:"info"`
	ID     string `json:"id"`
}

// GetContactResponse represents the response from getting contacts
type getContactResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Lead represents a lead in Holded CRM
type Lead struct {
	FunnelID     string            `json:"funnelId"`
	StageID      string            `json:"stageId"`
	ContactID    string            `json:"contactId"`
	Name         string            `json:"name"`
	CustomFields map[string]string `json:"customFields,omitempty"`
}

// LeadResponse represents the response from creating a lead
type LeadResponse struct {
	Status int    `json:"status"`
	Info   string `json:"info"`
	ID     string `json:"id"`
}

// GetLeadsFunnelID retrieves the list of funnels and returns the ID of the funnel named "Leads"
func (c *CRMClient) GetLeadsFunnelStageID() (funelID string, stageID string, err error) {
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

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response body: %w", err)
	}

	var funnels []funnel
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
func (c *CRMClient) CreateContact(name, email, countryCode string) (string, error) {
	if email == "" {
		return "", fmt.Errorf("email is required")
	}
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	url := fmt.Sprintf("%s/invoicing/v1/contacts", c.baseURL)

	contactData := &contact{
		Name:        name,
		Email:       email,
		CountryCode: countryCode,
		Type:        "lead",
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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var contactResp contactResponse
	if err := json.Unmarshal(body, &contactResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if contactResp.ID == "" {
		return "", fmt.Errorf("contact ID not returned in response")
	}

	return contactResp.ID, nil
}

// FindContactByEmail searches for a contact by email and returns the contact ID if found
func (c *CRMClient) FindContactByEmail(email string) (string, error) {
	if email == "" {
		return "", fmt.Errorf("email is required")
	}

	escapedEmail := url.QueryEscape(email)
	url := fmt.Sprintf("%s/invoicing/v1/contacts?email=%s", c.baseURL, escapedEmail)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("key", c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var contacts []getContactResponse
	if err := json.Unmarshal(body, &contacts); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(contacts) == 0 {
		return "", nil // Contact not found
	}

	return contacts[0].ID, nil
}

// CreateLead creates a new lead in Holded for the given contact and funnel
func (c *CRMClient) CreateLead(lead Lead) (string, error) {
	if lead.ContactID == "" {
		return "", fmt.Errorf("contact ID is required")
	}
	if lead.FunnelID == "" {
		return "", fmt.Errorf("funnel ID is required")
	}
	if lead.StageID == "" {
		return "", fmt.Errorf("stage ID is required")
	}

	url := fmt.Sprintf("%s/crm/v1/leads", c.baseURL)

	leadData := map[string]string{
		"funnelId":  lead.FunnelID,
		"stageId":   lead.StageID,
		"contactId": lead.ContactID,
		"name":      lead.Name,
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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

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

	// update the Lead custom fields if any
	if len(lead.CustomFields) > 0 {
		// convert custom fields to a JSON string
		customFields, err := json.Marshal(lead.CustomFields)
		if err != nil {
			return "", fmt.Errorf("failed to marshal custom fields: %w", err)
		}
		updateData := map[string]string{
			"title": "saas info",
			"desc":  string(customFields),
		}
		updateDataBytes, err := json.Marshal(updateData)
		if err != nil {
			return "", fmt.Errorf("failed to marshal update data: %w", err)
		}

		updateURL := fmt.Sprintf("%s/crm/v1/leads/%s/notes", c.baseURL, leadResp.ID)
		updateReq, err := http.NewRequest("POST", updateURL, bytes.NewBuffer(updateDataBytes))
		if err != nil {
			return "", fmt.Errorf("failed to create update request: %w", err)
		}

		updateReq.Header.Set("accept", "application/json")
		updateReq.Header.Set("content-type", "application/json")
		updateReq.Header.Set("key", c.apiKey)

		updateResp, err := c.client.Do(updateReq)
		if err != nil {
			return "", fmt.Errorf("failed to execute update request: %w", err)
		}
		defer func() {
			if err := updateResp.Body.Close(); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		if updateResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(updateResp.Body)
			return "", fmt.Errorf("API returned status %d on lead update: %s", updateResp.StatusCode, string(body))
		}
	}

	return leadResp.ID, nil
}

func (c *CRMClient) HandleContact(email, name, countryCode string) (string, bool, error) {
	contactID, err := c.FindContactByEmail(email)
	if err != nil {
		return "", false, fmt.Errorf("failed to search for contact: %w", err)
	}

	if contactID != "" {
		log.Infow("Contact found in Holded CRM", "email", email, "contactID", contactID)
		return contactID, true, nil
	}

	contactID, err = c.CreateContact(name, email, countryCode)
	if err != nil {
		return "", false, fmt.Errorf("failed to create contact: %w", err)
	}
	if contactID == "" {
		return "", false, fmt.Errorf("contact ID is empty after creation")
	}

	return contactID, false, nil
}

// HandleLead receives lead information and creates the contact and lead in the CRM
func (c *CRMClient) HandleLead(
	contactID, funnelID, stageID, name string,
	customFields map[string]string,
) (string, error) {
	lead := Lead{
		FunnelID:     funnelID,
		StageID:      stageID,
		ContactID:    contactID,
		Name:         name,
		CustomFields: customFields,
	}
	leadID, err := c.CreateLead(lead)
	if err != nil {
		return "", fmt.Errorf("failed to create lead: %w", err)
	}

	return leadID, nil
}
