package api

import (
	"encoding/json"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

func TestOrganizationEmbeddedMeta(t *testing.T) {
	token := testCreateUser(t, "password123")

	t.Run("string name is normalised to default locale", func(t *testing.T) {
		c := qt.New(t)
		body := &apicommon.OrganizationInfo{
			Type: string(db.CompanyType),
			Name: &apicommon.MultilingualText{"default": "Acme Corp"},
		}
		resp, code := testRequest(t, http.MethodPost, token, body, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var org apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &org), qt.IsNil)

		c.Assert(org.Name, qt.IsNotNil)
		c.Assert((*org.Name)["default"], qt.Equals, "Acme Corp")
		// also present in meta
		c.Assert(org.Meta["name"], qt.IsNotNil)
	})

	t.Run("multilingual object is stored as-is", func(t *testing.T) {
		c := qt.New(t)
		body := &apicommon.OrganizationInfo{
			Type: string(db.CompanyType),
			Name: &apicommon.MultilingualText{"default": "Acme", "es": "Acme ES"},
		}
		resp, code := testRequest(t, http.MethodPost, token, body, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var org apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &org), qt.IsNil)

		c.Assert(org.Name, qt.IsNotNil)
		c.Assert((*org.Name)["default"], qt.Equals, "Acme")
		c.Assert((*org.Name)["es"], qt.Equals, "Acme ES")
	})

	t.Run("logo and description are stored and returned", func(t *testing.T) {
		c := qt.New(t)
		body := &apicommon.OrganizationInfo{
			Type:        string(db.CompanyType),
			Logo:        &apicommon.MultilingualText{"default": "https://acme.com/logo.png"},
			Description: &apicommon.MultilingualText{"default": "We make things"},
		}
		resp, code := testRequest(t, http.MethodPost, token, body, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var org apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &org), qt.IsNil)

		c.Assert(org.Logo, qt.IsNotNil)
		c.Assert((*org.Logo)["default"], qt.Equals, "https://acme.com/logo.png")
		c.Assert(org.Description, qt.IsNotNil)
		c.Assert((*org.Description)["default"], qt.Equals, "We make things")
	})

	t.Run("explicit meta.name takes precedence over name field", func(t *testing.T) {
		c := qt.New(t)
		body := map[string]any{
			"type": string(db.CompanyType),
			"name": map[string]any{"default": "Shorthand Name"},
			"meta": map[string]any{
				"name": map[string]any{"default": "Meta Name"},
			},
		}
		resp, code := testRequest(t, http.MethodPost, token, body, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var org apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &org), qt.IsNil)

		c.Assert(org.Name, qt.IsNotNil)
		c.Assert((*org.Name)["default"], qt.Equals, "Meta Name")
	})

	t.Run("object without default key is rejected", func(t *testing.T) {
		c := qt.New(t)
		body := map[string]any{
			"type": string(db.CompanyType),
			"name": map[string]any{"en": "No Default"},
		}
		_, code := testRequest(t, http.MethodPost, token, body, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusBadRequest)
	})
}
