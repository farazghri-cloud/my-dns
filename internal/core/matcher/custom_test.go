package matcher

import (
	"slices"
	"testing"
)

// TestNormalizeManualRoutePhonesInTheSameAnswer covers every shape an operator
// realistically pastes into the Manual Routes box. They all have to collapse to
// one spelling, because that spelling is what gets stored, compared for
// duplicates, and indexed by the matcher.
func TestNormalizeManualRoutePhonesInTheSameAnswer(t *testing.T) {
	cases := map[string]string{
		"example.com":                     "example.com",
		"  Example.COM  ":                 "example.com",
		"example.com.":                    "example.com",
		".example.com":                    "example.com",
		"https://example.com":             "example.com",
		"https://example.com/":            "example.com",
		"http://api.example.com/v2/x?q=1": "api.example.com",
		"https://user:pass@example.com/x": "example.com",
		"example.com:443":                 "example.com",
		"example.com:443/path":            "example.com",
		"example.com/path":                "example.com", // a path with no scheme is still just a path
		"*.example.com":                   "*.example.com",
		`"*.example.com"`:                 "*.example.com",
		`"example.com"`:                   "example.com",
		"sub.domain.example.co.uk":        "sub.domain.example.co.uk",
		"*.Sub.Example.com.":              "*.sub.example.com",
		"xn--mgba3a4f16a.example":         "xn--mgba3a4f16a.example",
	}
	for in, want := range cases {
		got, err := NormalizeManualRoute(in)
		if err != nil {
			t.Errorf("NormalizeManualRoute(%q) error = %v, want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("NormalizeManualRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeManualRouteRefusesWhatCannotWork pins the refusals. Each of these
// either matches nothing (an IP, a URL fragment) or matches far too much (a bare
// TLD), and both are silent failures in the resolver: the operator sees a row in
// the list and no traffic on it.
func TestNormalizeManualRouteRefusesWhatCannotWork(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"com",
		"localhost",
		"1.2.3.4",
		"203.0.113.9:443",
		"[2001:db8::1]",
		"::1",
		"ftp:/example.com",   // no "://" to strip, so the slash is not a path
		"exa mple.com",       // interior space
		"foo.*.example.com",  // wildcard not leftmost
		"*",                  // wildcard with nothing after it
		"*.",                 // same
		"a..b.com",           // empty label
		"-example.com",       // label starts with a hyphen
		"example-.com",       // label ends with a hyphen
		"مثال.ایران",         // non-ASCII, needs punycode
		"example.123",        // numeric last label
		"exa!mple.com",       // illegal character
		"a_b",                // single label, even with an underscore
		"*.example.com:8443", // port after a wildcard is still just a port
	} {
		// The wildcard-with-port case is legal input; it is listed here by
		// mistake if it ever returns an error-free value the caller cannot use.
		got, err := NormalizeManualRoute(in)
		if in == "*.example.com:8443" {
			if err != nil || got != "*.example.com" {
				t.Errorf("NormalizeManualRoute(%q) = %q, %v; want *.example.com", in, got, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("NormalizeManualRoute(%q) = %q, want an error", in, got)
		}
	}
}

func TestRouteKind(t *testing.T) {
	cases := map[string]string{
		"example.com":      RouteKindDomain,
		"api.example.com":  RouteKindHost,
		"*.example.com":    RouteKindWildcard,
		"a.b.example.com.": RouteKindHost,
	}
	for in, want := range cases {
		if got := RouteKind(in); got != want {
			t.Errorf("RouteKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeRouteListDropsJunkAndDuplicates keeps a corrupted or hand-edited
// list usable: what can be honoured survives exactly once, and the rest is handed
// back so the dashboard can say which rows are dead.
func TestNormalizeRouteListDropsJunkAndDuplicates(t *testing.T) {
	normalized, dropped := NormalizeRouteList([]string{
		"Example.com", "example.com.", "https://api.example.com/x", "not a domain", "*.foo.com", "*.FOO.com", "1.2.3.4",
	})
	want := []string{"example.com", "api.example.com", "*.foo.com"}
	if !slices.Equal(normalized, want) {
		t.Errorf("normalized = %v, want %v", normalized, want)
	}
	wantDropped := []string{"not a domain", "1.2.3.4"}
	if !slices.Equal(dropped, wantDropped) {
		t.Errorf("dropped = %v, want %v", dropped, wantDropped)
	}
	if n, d := NormalizeRouteList(nil); n != nil || d != nil {
		t.Errorf("NormalizeRouteList(nil) = %v, %v; want nil, nil", n, d)
	}
}

// TestManualRouteIsProxiedForEveryClient is the behaviour the feature exists for:
// a hand-typed route is proxied even though no preset claims it and the client
// holds a policy selection that does not mention it.
func TestManualRouteIsProxiedForEveryClient(t *testing.T) {
	m := NewMatcher()
	manual, err := NormalizeManualRoute("https://Example.com/app")
	if err != nil {
		t.Fatalf("NormalizeManualRoute: %v", err)
	}
	wildcard, err := NormalizeManualRoute("*.newgame.example")
	if err != nil {
		t.Fatalf("NormalizeManualRoute: %v", err)
	}
	m.SetCustomRules([]string{manual, wildcard}, nil, nil, nil)

	// The site itself, and a subdomain of it.
	for _, name := range []string{"example.com", "cdn.example.com", "eu.cdn.example.com"} {
		if action, rule := m.Match(name); action != ActionProxy || rule != RuleCustomProxy {
			t.Errorf("Match(%q) = %v/%q, want PROXY/%q", name, action, rule, RuleCustomProxy)
		}
	}
	// The wildcard covers subdomains only, as documented in the dashboard.
	if action, rule := m.Match("sso.newgame.example"); action != ActionProxy || rule != RuleCustomProxy {
		t.Errorf("wildcard subdomain = %v/%q, want PROXY/%q", action, rule, RuleCustomProxy)
	}
	if action, _ := m.Match("newgame.example"); action != ActionDirect {
		t.Errorf("wildcard apex = %v, want DIRECT (a wildcard never covers its own apex)", action)
	}
	// And a per-client selection that lists nothing relevant does not take it away.
	if action, rule := m.MatchForClient("example.com", []string{"enable_riot"}); action != ActionProxy || rule != RuleCustomProxy {
		t.Errorf("MatchForClient with an unrelated policy = %v/%q, want PROXY/%q", action, rule, RuleCustomProxy)
	}
}
