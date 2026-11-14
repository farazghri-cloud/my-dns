package database

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrCustomRuleNotFound = errors.New("custom rule not found")

func generateRuleID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "rule_" + hex.EncodeToString(b)
}

// maxRuleHostnameLen is the presentation-form limit for a fully qualified
// domain name (255 octets on the wire; 253 in the dotted form that is stored).
const maxRuleHostnameLen = 253

// normalizeCustomRule validates and canonicalizes one manual rule entry.
func normalizeCustomRule(r *CustomRule) error {
	d, err := normalizeRuleDomain(r.Domain)
	if err != nil {
		return err
	}
	r.Domain = d

	act := strings.TrimSpace(strings.ToLower(r.Action))
	if act != "proxy" && act != "direct" {
		return fmt.Errorf("invalid action %q (must be 'proxy' or 'direct')", r.Action)
	}
	r.Action = act
	return nil
}

// normalizeRuleDomain canonicalizes a user-supplied routing entry into a bare
// lowercase hostname or a leading-wildcard pattern. It accepts the forms a
// person actually pastes — a bare domain, a full URL
// ("https://host:8443/store"), a "host:port" pair — and reduces each to its
// host part, so an entry that names one host always stores one host.
//
// IP literals and CIDR ranges are rejected with a specific error instead of
// being stored as hostnames they can never match: the policy matcher routes
// DNS query names, and neither the built-in presets nor this router ever
// matches on an address. (Pinning a domain to a fixed address is what the
// Custom DNS Records list is for.)
func normalizeRuleDomain(raw string) (string, error) {
	d := strings.TrimSpace(strings.ToLower(raw))
	d = strings.TrimSuffix(d, ".")
	if d == "" || d == "." {
		return "", errors.New("domain cannot be empty")
	}
	if i := strings.Index(d, "://"); i >= 0 {
		d = d[i+3:]
	}
	// A slash means a CIDR range when the whole string parses as one, and a URL
	// path otherwise. The CIDR test runs first: cutting the path first would
	// turn "10.0.0.0/8" into the bare address "10.0.0.0" and report the wrong
	// reason for the rejection.
	if _, _, err := net.ParseCIDR(d); err == nil {
		return "", addressNotRoutable(raw, "CIDR ranges")
	}
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndex(d, "@"); i >= 0 {
		d = d[i+1:]
	}
	// Bare IP literals ("1.2.3.4", "::1") are addresses, not names.
	if net.ParseIP(d) != nil {
		return "", addressNotRoutable(raw, "IP addresses")
	}
	// A bracketed form ("[::1]" or "[::1]:443") exists for address literals
	// only, so it is either an address or a mistake — never a hostname.
	if strings.HasPrefix(d, "[") {
		end := strings.Index(d, "]")
		if end < 0 || net.ParseIP(d[1:end]) == nil {
			return "", fmt.Errorf("invalid address literal %q", raw)
		}
		return "", addressNotRoutable(raw, "IP addresses")
	}
	// A remaining colon is a port suffix ("host:8443") when the tail is numeric
	// and the host part carries no colon of its own — a colon inside the host
	// part is a malformed IPv6 literal, and the ParseIP probe above already
	// caught the valid ones. Strip the port and re-test, because
	// "1.2.3.4:8080" is an address with a port, not a name.
	if strings.Contains(d, ":") {
		i := strings.LastIndex(d, ":")
		if !isAllDigits(d[i+1:]) || strings.Contains(d[:i], ":") {
			return "", fmt.Errorf("invalid entry %q: a colon is only allowed as a numeric :port suffix", raw)
		}
		d = d[:i]
		if net.ParseIP(d) != nil {
			return "", addressNotRoutable(raw, "IP addresses")
		}
	}
	// Wildcard form: only a leading "*.domain" is supported, matching what the
	// rule index and the reload path accept.
	if strings.HasPrefix(d, "*.") {
		bare, err := validateHostname(d[2:], raw)
		if err != nil {
			return "", err
		}
		return "*." + bare, nil
	}
	return validateHostname(d, raw)
}

// validateHostname enforces RFC 1123 hostname syntax on an already-canonical
// host: letters, digits and hyphens per label, no empty or hyphen-edge labels,
// 63 characters per label and 253 overall. Underscores are rejected on
// purpose: the SNI relay reads the ClientHello's server name, which cannot
// carry one, and no built-in preset lists one.
func validateHostname(d, raw string) (string, error) {
	if len(d) > maxRuleHostnameLen {
		return "", fmt.Errorf("domain %q is longer than %d characters", raw, maxRuleHostnameLen)
	}
	if strings.Contains(d, "*") {
		return "", fmt.Errorf("invalid domain wildcard %q (only a leading *. is supported)", raw)
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" {
			return "", fmt.Errorf("invalid domain %q: empty label (double dot?)", raw)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("invalid domain %q: label %q is longer than 63 characters", raw, label)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
				continue
			}
			return "", fmt.Errorf("invalid domain %q: character %q is not allowed (label %q)", raw, string(c), label)
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid domain %q: label %q must not start or end with a hyphen", raw, label)
		}
	}
	return d, nil
}

// addressNotRoutable is the rejection message for IP and CIDR entries. It names
// the replacement instead of only the failure: the operator who pasted an
// address usually wants a name to answer with that address, which is the
// Custom DNS Records list, not a routing rule.
func addressNotRoutable(raw, kind string) error {
	return fmt.Errorf("%s cannot be routed (%q) — the matcher matches DNS query names, not addresses; use a Custom DNS Record to pin a domain to an address", kind, raw)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SaveCustomRule validates and persists a custom manual domain routing rule.
func (db *DB) SaveCustomRule(rule CustomRule) (CustomRule, error) {
	if err := normalizeCustomRule(&rule); err != nil {
		return CustomRule{}, err
	}
	now := time.Now().UTC()
	if strings.TrimSpace(rule.ID) == "" {
		rule.ID = generateRuleID()
		if rule.CreatedAt.IsZero() {
			rule.CreatedAt = now
		}
	} else {
		// Preserve CreatedAt if updating an existing rule
		if existing, err := db.GetCustomRule(rule.ID); err == nil && existing != nil && !existing.CreatedAt.IsZero() {
			rule.CreatedAt = existing.CreatedAt
		} else if rule.CreatedAt.IsZero() {
			rule.CreatedAt = now
		}
	}
	rule.UpdatedAt = now

	data, err := json.Marshal(rule)
	if err != nil {
		return CustomRule{}, err
	}

	err = db.bolt.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketCustomRules)
		if err != nil {
			return err
		}
		// Duplicate guard, inside the same write transaction as the put so two
		// concurrent saves serialize: the second sees the first's record.
		if rule.Enabled {
			if other := enabledRuleForDomain(b, rule.ID, rule.Domain); other != "" {
				return fmt.Errorf("an enabled rule for domain %q already exists (%s); delete or disable it first", rule.Domain, other)
			}
		}
		return b.Put([]byte(rule.ID), data)
	})
	if err != nil {
		return CustomRule{}, err
	}
	return rule, nil
}

// enabledRuleForDomain scans the bucket for an enabled rule claiming the same
// normalized domain, excluding excludeID (an edit re-saving its own domain).
// It exists because two enabled rules for one domain cannot both win: the
// reload path keys a map by domain, so the later record silently overwrote the
// earlier one and the losing rule kept looking active in the UI. The invariant
// is "at most one enabled rule per normalized domain"; a disabled rule may
// shadow a live one freely (it matches nothing until re-enabled, and
// re-enabling is a save that passes through this same check).
//
// Rows that fail normalization are skipped rather than fatal: this scan runs
// against whatever older releases stored, and a legacy row must never block a
// new save.
func enabledRuleForDomain(b *bolt.Bucket, excludeID, domain string) string {
	conflict := ""
	_ = b.ForEach(func(_ []byte, v []byte) error {
		if conflict != "" || v == nil {
			return nil
		}
		var existing CustomRule
		if err := json.Unmarshal(v, &existing); err != nil {
			return nil
		}
		if existing.ID == excludeID || !existing.Enabled {
			return nil
		}
		if d, err := normalizeRuleDomain(existing.Domain); err == nil && d == domain {
			conflict = existing.ID
		}
		return nil
	})
	return conflict
}

// GetCustomRule fetches a custom rule by ID.
func (db *DB) GetCustomRule(id string) (*CustomRule, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrCustomRuleNotFound
	}
	var rule CustomRule
	var found bool
	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCustomRules)
		if b == nil {
			return nil
		}
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, &rule)
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrCustomRuleNotFound
	}
	return &rule, nil
}

// ListCustomRules returns all persisted custom manual domain rules sorted deterministically.
func (db *DB) ListCustomRules() ([]CustomRule, error) {
	rules := []CustomRule{}
	err := db.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCustomRules)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var r CustomRule
			if err := json.Unmarshal(v, &r); err == nil {
				rules = append(rules, r)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rules, func(i, j int) bool {
		if !rules[i].CreatedAt.Equal(rules[j].CreatedAt) {
			return rules[i].CreatedAt.Before(rules[j].CreatedAt)
		}
		return rules[i].ID < rules[j].ID
	})
	return rules, nil
}

// DeleteCustomRule deletes a custom manual rule by ID.
func (db *DB) DeleteCustomRule(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrCustomRuleNotFound
	}
	return db.bolt.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCustomRules)
		if b == nil {
			return ErrCustomRuleNotFound
		}
		if b.Get([]byte(id)) == nil {
			return ErrCustomRuleNotFound
		}
		return b.Delete([]byte(id))
	})
}
