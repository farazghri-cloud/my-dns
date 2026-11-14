package matcher

import (
	"testing"

	"hyperdns/internal/database"
)

func TestCustomRouterWildcardAndExact(t *testing.T) {
	r := NewCustomRouter()
	r.Reload([]database.CustomRule{
		{ID: "1", Domain: "*.epicgames.com", Action: "proxy", Enabled: true},
		{ID: "2", Domain: "download.epicgames.com", Action: "direct", Enabled: true},
		{ID: "3", Domain: "exact.example.org", Action: "proxy", Enabled: true},
		{ID: "4", Domain: "disabled.example.org", Action: "proxy", Enabled: false},
	})

	cases := []struct {
		domain string
		want   string
		ok     bool
	}{
		{"store.epicgames.com", "proxy", true},
		{"sub.store.epicgames.com", "proxy", true},
		{"download.epicgames.com", "direct", true}, // exact overrides wildcard
		{"epicgames.com", "", false},               // wildcard *.epicgames.com matches subdomains
		{"exact.example.org", "proxy", true},
		{"sub.exact.example.org", "", false},
		{"disabled.example.org", "", false},
		{"unknown.test", "", false},
	}

	for _, tc := range cases {
		act, ok := r.Match(tc.domain)
		if ok != tc.ok || act != tc.want {
			t.Errorf("Match(%q) = (%q, %v), want (%q, %v)", tc.domain, act, ok, tc.want, tc.ok)
		}
	}
}

func TestCustomRouterZeroAllocHotPath(t *testing.T) {
	r := NewCustomRouter()
	r.Reload([]database.CustomRule{
		{ID: "1", Domain: "*.epicgames.com", Action: "proxy", Enabled: true},
		{ID: "2", Domain: "exact.cdn.net", Action: "direct", Enabled: true},
	})

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = r.Match("store.launcher.epicgames.com")
		_, _ = r.Match("exact.cdn.net")
		_, _ = r.Match("unrelated.domain.org")
	})
	if allocs > 0 {
		t.Errorf("CustomRouter.Match allocated %.1f times per run, want 0", allocs)
	}
}

func TestMatcherCustomRuleMergeKeepsBuiltins(t *testing.T) {
	m := NewMatcher()

	// epicgames.com is a built-in Epic preset domain; before any custom rule
	// exists, the built-in list alone decides the answer.
	if act, rule := m.Match("epicgames.com"); act != ActionProxy {
		t.Fatalf("epicgames.com with no custom rules = (%v, %q), want built-in PROXY", act, rule)
	}

	// An enabled custom rule for a built-in domain wins while it is live — the
	// custom router is consulted ahead of the preset indexes by design.
	m.ReloadCustomRules([]database.CustomRule{
		{ID: "1", Domain: "epicgames.com", Action: "direct", Enabled: true},
	})
	if act, rule := m.Match("epicgames.com"); act != ActionDirect || rule != "Custom Domain (Direct)" {
		t.Fatalf("epicgames.com with custom direct rule = (%v, %q), want (DIRECT, Custom Domain (Direct))", act, rule)
	}

	// Removing the custom rule restores the built-in answer untouched: the
	// merge is additive, never destructive to the preset indexes.
	m.ReloadCustomRules(nil)
	if act, _ := m.Match("epicgames.com"); act != ActionProxy {
		t.Fatalf("epicgames.com after the custom rule was removed = %v, want the built-in PROXY again", act)
	}
}

func TestMatcherScopedClientAndDownloadBypass(t *testing.T) {
	m := NewMatcher()
	m.ReloadCustomRules([]database.CustomRule{
		{ID: "1", Domain: "*.mycustom.local", Action: "proxy", Enabled: true},
	})

	// Scoped to Epic Games only, downloads disabled (bypass CDN to upstream)
	act, _ := m.MatchForScopedClient("epicgames.com", []string{"enable_epic"}, false)
	if act != ActionProxy {
		t.Errorf("epicgames.com with enable_epic scope = %v, want PROXY", act)
	}

	act, _ = m.MatchForScopedClient("playvalorant.com", []string{"enable_epic"}, false)
	if act != ActionDirect {
		t.Errorf("playvalorant.com outside enable_epic scope = %v, want DIRECT", act)
	}

	// Custom manual rule still applies in scoped mode even with empty AllowedPolicies
	act, _ = m.MatchForScopedClient("app.mycustom.local", []string{}, false)
	if act != ActionProxy {
		t.Errorf("custom rule app.mycustom.local = %v, want PROXY", act)
	}

	// Empty AllowedPolicies in strict scoped mode does NOT inherit global presets
	act, _ = m.MatchForScopedClient("epicgames.com", []string{}, false)
	if act != ActionDirect {
		t.Errorf("epicgames.com with empty strict scope = %v, want DIRECT", act)
	}

	// Download CDN bypass: steamcontent.com is in PresetDownloads & Steam preset.
	// Even if client is scoped to enable_steam, allowDownloads=false forces DIRECT.
	act, rule := m.MatchForScopedClient("steamcontent.com", []string{"enable_steam"}, false)
	if act != ActionDirect || rule != RuleDownloads {
		t.Errorf("steamcontent.com with allowDownloads=false = (%v, %q), want (DIRECT, %q)", act, rule, RuleDownloads)
	}

	// When allowDownloads=true and enable_steam is in allowed_policies, CDN is proxied.
	act, _ = m.MatchForScopedClient("steamcontent.com", []string{"enable_steam"}, true)
	if act != ActionProxy {
		t.Errorf("steamcontent.com with allowDownloads=true and enable_steam = %v, want PROXY", act)
	}
}
