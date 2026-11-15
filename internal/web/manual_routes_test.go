package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"hyperdns/internal/core/matcher"
)

// deleteRoute drives the DELETE verb the dashboard's remove button uses.
func deleteRoute(t *testing.T, h http.Handler, domain, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/rules/manual?domain="+domain, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// routeDomains reads the proxied list back the way the panel does: out of
// /api/config, not out of the endpoint under test.
func routeDomains(t *testing.T, h http.Handler, tok string) []string {
	t.Helper()
	return domainsAt(t, rulesFromConfig(t, h, tok), "custom_proxied")
}

// TestManualRouteRoundTrip is the feature end to end at the HTTP boundary: a route
// added from the panel lands in the rules the dashboard reads back, reaches the live
// matcher, and is removed by the same panel.
func TestManualRouteRoundTrip(t *testing.T) {
	ws, h, tok, cleanup := authedServer(t)
	defer cleanup()

	// A URL pasted out of a browser address bar: the panel promises to strip the
	// scheme, the path and the case, and the stored value has to prove it did.
	w := postJSON(t, h, "/api/rules/manual", `{"domain":"https://Example.COM/some/path?q=1"}`, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("POST route = %d, want 200 — %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if body["domain"] != "example.com" {
		t.Errorf("stored domain = %v, want example.com (canonicalised)", body["domain"])
	}
	if body["kind"] != matcher.RouteKindDomain {
		t.Errorf("kind = %v, want %q", body["kind"], matcher.RouteKindDomain)
	}
	if body["count"] != float64(1) {
		t.Errorf("count = %v, want 1", body["count"])
	}

	// Persisted, and served to the dashboard in the same list the Settings tab reads.
	if got := routeDomains(t, h, tok); !slices.Equal(got, []string{"example.com"}) {
		t.Fatalf("custom_proxied = %v, want [example.com]", got)
	}

	// Live: DNS answers for it with the proxy, without waiting for a restart.
	if action, rule := ws.matcher.Match("cdn.example.com"); action != matcher.ActionProxy || rule != matcher.RuleCustomProxy {
		t.Errorf("Match(cdn.example.com) = %v/%q, want PROXY/%q", action, rule, matcher.RuleCustomProxy)
	}

	// The list endpoint reports the same row.
	var listed struct {
		Routes []manualRoute `json:"routes"`
		Count  int           `json:"count"`
	}
	w = authedGet(t, h, "/api/rules/manual", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("GET routes = %d — %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode routes: %v", err)
	}
	if listed.Count != 1 || len(listed.Routes) != 1 || listed.Routes[0].Domain != "example.com" {
		t.Fatalf("listed = %+v, want one example.com row", listed)
	}

	// Removal is immediate and persistent.
	if w := deleteRoute(t, h, "example.com", tok); w.Code != http.StatusOK {
		t.Fatalf("DELETE route = %d — %s", w.Code, w.Body.String())
	}
	if got := routeDomains(t, h, tok); len(got) != 0 {
		t.Errorf("custom_proxied = %v after removal, want empty", got)
	}
	if action, _ := ws.matcher.Match("example.com"); action != matcher.ActionDirect {
		t.Errorf("Match(example.com) = %v after removal, want DIRECT", action)
	}
}

// TestManualRouteRejectsWhatTheResolverCannotUse pins the contract the panel relies
// on: a refusal is a 400 with a sentence an operator can act on, and nothing is
// written. The old rules-save path accepted any string and stored it.
func TestManualRouteRejectsWhatTheResolverCannotUse(t *testing.T) {
	ws, h, tok, cleanup := authedServer(t)
	defer cleanup()

	for _, body := range []string{
		`{"domain":"com"}`,
		`{"domain":"1.2.3.4"}`,
		`{"domain":"*.foo.*.bar.com"}`,
		`{"domain":"not a domain"}`,
		`{"domain":""}`,
	} {
		w := postJSON(t, h, "/api/rules/manual", body, tok)
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400 — %s", body, w.Code, w.Body.String())
			continue
		}
		if decodeBody(t, w)["error"] == nil {
			t.Errorf("POST %s was refused without an explanation: %s", body, w.Body.String())
		}
	}
	if got := routeDomains(t, h, tok); len(got) != 0 {
		t.Errorf("a refused route was still stored: %v", got)
	}
	if action, _ := ws.matcher.Match("foo.bar.com"); action != matcher.ActionDirect {
		t.Errorf("a refused wildcard reached the matcher: %v", action)
	}
}

// TestManualRouteDuplicateIsRefused: the list is the rule set, so a second copy of
// the same name is noise in the dashboard and a no-op in the index. 409 says exactly
// that, and the count does not move.
func TestManualRouteDuplicateIsRefused(t *testing.T) {
	_, h, tok, cleanup := authedServer(t)
	defer cleanup()

	if w := postJSON(t, h, "/api/rules/manual", `{"domain":"game.example"}`, tok); w.Code != http.StatusOK {
		t.Fatalf("first add = %d — %s", w.Code, w.Body.String())
	}
	// Same name, different presentation: still a duplicate.
	if w := postJSON(t, h, "/api/rules/manual", `{"domain":"HTTPS://Game.Example./"}`, tok); w.Code != http.StatusConflict {
		t.Errorf("duplicate add = %d, want 409 — %s", w.Code, w.Body.String())
	}
	if got := routeDomains(t, h, tok); !slices.Equal(got, []string{"game.example"}) {
		t.Errorf("custom_proxied = %v, want exactly one entry", got)
	}
	// Removing something that is not there is a 404, not a silent success.
	if w := deleteRoute(t, h, "missing.example", tok); w.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown = %d, want 404 — %s", w.Code, w.Body.String())
	}
}

// TestManualRouteSurvivesAPresetToggle is the other half of "in addition to": the
// policies tab saves the whole preset payload, and that save must neither wipe the
// routes nor disturb them.
func TestManualRouteSurvivesAPresetToggle(t *testing.T) {
	_, h, tok, cleanup := authedServer(t)
	defer cleanup()

	if w := postJSON(t, h, "/api/rules/manual", `{"domain":"*.stream.example"}`, tok); w.Code != http.StatusOK {
		t.Fatalf("add wildcard = %d — %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, h, "/api/config/rules", `{"enable_riot":false}`, tok); w.Code != http.StatusOK {
		t.Fatalf("preset save = %d — %s", w.Code, w.Body.String())
	}
	if got := routeDomains(t, h, tok); !slices.Equal(got, []string{"*.stream.example"}) {
		t.Errorf("custom_proxied = %v after a preset toggle, want the wildcard intact", got)
	}
}

// TestManualRoutesRequiresAuth: the endpoint edits the resolver's rule index for
// every client, so an anonymous caller must not reach it.
func TestManualRoutesRequiresAuth(t *testing.T) {
	_, h, _, cleanup := authedServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/rules/manual", strings.NewReader(`{"domain":"evil.example"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST = %d, want 401", w.Code)
	}
}
