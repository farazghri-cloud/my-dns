package database

import (
	"path/filepath"
	"strings"
	"testing"

	"hyperdns/internal/crypto"
)

func newCustomRulesTestDB(t *testing.T) *DB {
	t.Helper()
	// Same pattern as TestDatabase_ClientEncryptionAndCRUD: a fresh file in
	// t.TempDir(), because bbolt's exclusive lock turns a leftover file into a
	// hang rather than an error.
	tmpDB := filepath.Join(t.TempDir(), "test_hyperdns.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}
	db, err := Open(tmpDB, cipher)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNormalizeCustomRule_AcceptedForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain domain", "example.com", "example.com"},
		{"subdomain", "play.example.com", "play.example.com"},
		{"wildcard", "*.example.com", "*.example.com"},
		{"uppercase and trailing dot", "Play.Example.COM.", "play.example.com"},
		{"surrounding whitespace", "  example.com\t", "example.com"},
		{"https URL with port and path", "https://play.example.com:8443/store/list", "play.example.com"},
		{"http URL with query", "http://example.com/login?next=/x", "example.com"},
		{"scheme without path", "https://example.com", "example.com"},
		{"userinfo", "user@example.com", "example.com"},
		{"port suffix", "example.com:8443", "example.com"},
		{"URL userinfo port and path together", "https://user@www.example.com:8080/a/b#c", "www.example.com"},
		{"wildcard uppercase", "*.EPICGames.com.", "*.epicgames.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := CustomRule{Domain: tc.in, Action: "proxy", Enabled: true}
			if err := normalizeCustomRule(&r); err != nil {
				t.Fatalf("normalizeCustomRule(%q) returned error %v, want %q", tc.in, err, tc.want)
			}
			if r.Domain != tc.want {
				t.Fatalf("normalizeCustomRule(%q) = %q, want %q", tc.in, r.Domain, tc.want)
			}
		})
	}
}

func TestNormalizeCustomRule_RejectedForms(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantIn string // substring expected in the error message
	}{
		{"empty", "", "cannot be empty"},
		{"bare dot", ".", "cannot be empty"},
		{"bare star", "*", "wildcard"},
		{"bare star dot", "*.", "wildcard"},
		{"internal wildcard", "a.*.b.com", "wildcard"},
		{"wildcard inside suffix", "*.a.*.com", "wildcard"},
		{"double dot", "ab..cd.com", "empty label"},
		{"leading hyphen", "-abc.com", "hyphen"},
		{"trailing hyphen", "abc-.com", "hyphen"},
		{"space", "exa mple.com", "not allowed"},
		{"punctuation", "!!!", "not allowed"},
		{"underscore", "_dmarc.example.com", "not allowed"},
		{"colon with non-numeric tail", "exa:mple.com", ":port"},
		{"trailing colon", "example.com:", ":port"},
		{"IPv4", "1.2.3.4", "IP addresses"},
		{"IPv4 with port", "1.2.3.4:8080", "IP addresses"},
		{"IPv4 URL", "http://1.2.3.4/login", "IP addresses"},
		{"IPv6", "::1", "IP addresses"},
		{"bracketed IPv6 with port", "[::1]:443", "IP addresses"},
		{"bracketed non-address", "[bad]:443", "address literal"},
		{"CIDR", "10.0.0.0/8", "CIDR ranges"},
		{"CIDR in URL form", "https://192.168.0.0/16", "CIDR ranges"},
		{"valid IPv6 literal", "a::b", "IP addresses"},
		{"malformed IPv6 colon run", "a:::b", ":port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := CustomRule{Domain: tc.in, Action: "proxy", Enabled: true}
			err := normalizeCustomRule(&r)
			if err == nil {
				t.Fatalf("normalizeCustomRule(%q) accepted the entry (stored %q), want error", tc.in, r.Domain)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("normalizeCustomRule(%q) error %q does not mention %q", tc.in, err.Error(), tc.wantIn)
			}
		})
	}
}

func TestNormalizeCustomRule_ActionValidation(t *testing.T) {
	r := CustomRule{Domain: "example.com", Action: "  PROXY ", Enabled: true}
	if err := normalizeCustomRule(&r); err != nil {
		t.Fatalf("normalizeCustomRule rejected a valid action: %v", err)
	}
	if r.Action != "proxy" {
		t.Fatalf("action = %q, want \"proxy\"", r.Action)
	}
	r.Action = "drop"
	if err := normalizeCustomRule(&r); err == nil {
		t.Fatal("normalizeCustomRule accepted an unknown action")
	}
}

func TestSaveCustomRule_RoundTripNormalization(t *testing.T) {
	db := newCustomRulesTestDB(t)

	saved, err := db.SaveCustomRule(CustomRule{
		Domain:  "HTTPS://Play.Example.COM:8443/store",
		Action:  "proxy",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveCustomRule rejected a URL-pasted entry: %v", err)
	}
	if saved.Domain != "play.example.com" {
		t.Fatalf("saved domain = %q, want %q", saved.Domain, "play.example.com")
	}

	got, err := db.GetCustomRule(saved.ID)
	if err != nil {
		t.Fatalf("GetCustomRule: %v", err)
	}
	if got.Domain != "play.example.com" || got.Action != "proxy" || !got.Enabled {
		t.Fatalf("round-tripped rule = %+v, want domain play.example.com, action proxy, enabled", got)
	}

	rules, err := db.ListCustomRules()
	if err != nil {
		t.Fatalf("ListCustomRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Domain != "play.example.com" {
		t.Fatalf("ListCustomRules = %+v, want exactly the one normalized rule", rules)
	}
}

func TestSaveCustomRule_DuplicateDomains(t *testing.T) {
	db := newCustomRulesTestDB(t)

	first, err := db.SaveCustomRule(CustomRule{Domain: "example.com", Action: "proxy", Enabled: true})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	// An enabled duplicate is rejected regardless of action or input spelling.
	dup := []CustomRule{
		{Domain: "example.com", Action: "direct", Enabled: true},
		{Domain: "EXAMPLE.com.", Action: "proxy", Enabled: true},
		{Domain: "https://example.com/x", Action: "proxy", Enabled: true},
	}
	for _, d := range dup {
		if _, err := db.SaveCustomRule(d); err == nil {
			t.Fatalf("SaveCustomRule(%q) accepted a duplicate of rule %s", d.Domain, first.ID)
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("SaveCustomRule(%q) error %q does not name the conflict", d.Domain, err.Error())
		}
	}

	// A disabled second rule is allowed: it matches nothing until re-enabled.
	shadow, err := db.SaveCustomRule(CustomRule{Domain: "example.com", Action: "direct", Enabled: false})
	if err != nil {
		t.Fatalf("disabled shadow save: %v", err)
	}

	// Re-enabling the shadow while the first is live hits the same guard.
	shadow.Enabled = true
	if _, err := db.SaveCustomRule(shadow); err == nil {
		t.Fatal("re-enabling a shadow onto a live rule was accepted")
	}

	// Editing a rule to its own domain is not a conflict with itself.
	first.Action = "direct"
	if _, err := db.SaveCustomRule(first); err != nil {
		t.Fatalf("self-edit save: %v", err)
	}

	// Disabling the first frees the domain for the shadow.
	first.Enabled = false
	if _, err := db.SaveCustomRule(first); err != nil {
		t.Fatalf("disabling save: %v", err)
	}
	shadow.Enabled = true
	if _, err := db.SaveCustomRule(shadow); err != nil {
		t.Fatalf("re-enable after the live rule was disabled: %v", err)
	}
}

func TestSaveCustomRule_ExactAndWildcardCoexist(t *testing.T) {
	db := newCustomRulesTestDB(t)

	if _, err := db.SaveCustomRule(CustomRule{Domain: "example.com", Action: "proxy", Enabled: true}); err != nil {
		t.Fatalf("exact save: %v", err)
	}
	// "example.com" and "*.example.com" are different entries by design: the
	// exact form matches only itself, the wildcard only strict subdomains.
	if _, err := db.SaveCustomRule(CustomRule{Domain: "*.example.com", Action: "proxy", Enabled: true}); err != nil {
		t.Fatalf("wildcard save alongside its exact form: %v", err)
	}
	if _, err := db.SaveCustomRule(CustomRule{Domain: "*.EXAMPLE.com", Action: "direct", Enabled: true}); err == nil {
		t.Fatal("second wildcard with the same normalized pattern was accepted")
	}
}
