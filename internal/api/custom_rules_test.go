package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hyperdns/internal/database"
)

func TestV2CustomRulesCRUD(t *testing.T) {
	a, _, _, cleanup := setupTestAPI(t)
	mux := v2Mux(a)
	defer cleanup()

	// 1. Initially empty
	w := v2Get(t, mux, "/api/v2/custom-rules")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/custom-rules = %d", w.Code)
	}

	// 2. Create custom rule (POST)
	body := `{"domain":"*.epicgames.com","action":"proxy","enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/custom-rules", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-API-Key", v2TestKey)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v2/custom-rules = %d: %s", w.Code, w.Body.String())
	}
	var created database.CustomRule
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created rule: %v", err)
	}
	if created.ID == "" || created.Domain != "*.epicgames.com" || created.Action != "proxy" {
		t.Fatalf("unexpected created rule: %+v", created)
	}

	// 3. Get rule by ID
	w = v2Get(t, mux, "/api/v2/custom-rules/"+created.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/custom-rules/%s = %d", created.ID, w.Code)
	}

	// 4. Update rule (PUT)
	updateBody := `{"action":"direct"}`
	req = httptest.NewRequest(http.MethodPut, "/api/v2/custom-rules/"+created.ID, strings.NewReader(updateBody))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-API-Key", v2TestKey)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/v2/custom-rules/%s = %d: %s", created.ID, w.Code, w.Body.String())
	}
	var updated database.CustomRule
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil || updated.Action != "direct" {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	// 5. Delete rule
	req = httptest.NewRequest(http.MethodDelete, "/api/v2/custom-rules/"+created.ID, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-API-Key", v2TestKey)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v2/custom-rules/%s = %d", created.ID, w.Code)
	}

	// 6. Verify 404 after delete
	w = v2Get(t, mux, "/api/v2/custom-rules/"+created.ID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET deleted rule = %d, want 404", w.Code)
	}
}

func TestV2ClientPoliciesEndpoint(t *testing.T) {
	a, _, _, cleanup := setupTestAPI(t)
	mux := v2Mux(a)
	defer cleanup()

	c, err := a.clients.CreateClient("Scoped Client", 30, "")
	if err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	// PUT policies
	body := `{"allowed_policies":["enable_epic"],"allow_downloads":false,"use_custom_scope":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/v2/clients/"+c.ID+"/policies", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-API-Key", v2TestKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/v2/clients/%s/policies = %d: %s", c.ID, w.Code, w.Body.String())
	}

	// GET policies
	w = v2Get(t, mux, "/api/v2/clients/"+c.ID+"/policies")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/clients/%s/policies = %d: %s", c.ID, w.Code, w.Body.String())
	}
	var res struct {
		AllowedPolicies []string `json:"allowed_policies"`
		AllowDownloads  bool     `json:"allow_downloads"`
		UseCustomScope  bool     `json:"use_custom_scope"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal policies: %v", err)
	}
	if !res.UseCustomScope || res.AllowDownloads || len(res.AllowedPolicies) != 1 || res.AllowedPolicies[0] != "enable_epic" {
		t.Fatalf("unexpected policies payload: %+v", res)
	}
}
