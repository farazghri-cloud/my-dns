package matcher

import (
	"strings"
	"sync/atomic"

	"hyperdns/internal/database"
)

type customRulesSnapshot struct {
	exact    map[string]string // normalized domain -> "proxy" | "direct"
	wildcard map[string]string // normalized suffix -> "proxy" | "direct"
	rules    []database.CustomRule
}

// CustomRouter provides lockless, zero-allocation atomic lookups for manual
// custom domain rules supporting exact and leading-wildcard (*.domain) matching.
type CustomRouter struct {
	snapshot atomic.Pointer[customRulesSnapshot]
}

// NewCustomRouter creates an empty CustomRouter.
func NewCustomRouter() *CustomRouter {
	r := &CustomRouter{}
	r.snapshot.Store(&customRulesSnapshot{
		exact:    map[string]string{},
		wildcard: map[string]string{},
	})
	return r
}

// Reload atomically replaces the active rule snapshot without locking readers.
func (r *CustomRouter) Reload(rules []database.CustomRule) {
	if r == nil {
		return
	}
	exact := make(map[string]string, len(rules))
	wildcard := make(map[string]string, len(rules))
	copied := make([]database.CustomRule, 0, len(rules))

	for _, rule := range rules {
		copied = append(copied, rule)
		if !rule.Enabled {
			continue
		}
		d := normalizeDomain(rule.Domain)
		act := strings.ToLower(strings.TrimSpace(rule.Action))
		if act != "proxy" && act != "direct" {
			continue
		}
		if strings.HasPrefix(d, "*.") {
			suffix := strings.TrimPrefix(d, "*.")
			if suffix == "" || strings.Contains(suffix, "*") {
				continue
			}
			wildcard[suffix] = act
		} else {
			if d == "" || strings.Contains(d, "*") {
				continue
			}
			exact[d] = act
		}
	}

	r.snapshot.Store(&customRulesSnapshot{
		exact:    exact,
		wildcard: wildcard,
		rules:    copied,
	})
}

// Match resolves a domain against exact and wildcard custom rules.
// Exact matches take precedence over wildcard matches; longer wildcard suffixes
// take precedence over shorter ones. Hot path is lockless and allocation-free.
func (r *CustomRouter) Match(domain string) (string, bool) {
	if r == nil {
		return "", false
	}
	snap := r.snapshot.Load()
	if snap == nil || (len(snap.exact) == 0 && len(snap.wildcard) == 0) {
		return "", false
	}

	d := normalizeFast(domain)
	if d == "" {
		return "", false
	}

	if act, ok := snap.exact[d]; ok {
		return act, true
	}

	if len(snap.wildcard) == 0 {
		return "", false
	}

	for i := 0; i < len(d); {
		dot := strings.IndexByte(d[i:], '.')
		if dot < 0 {
			break
		}
		i += dot + 1
		if i >= len(d) {
			break
		}
		if act, ok := snap.wildcard[d[i:]]; ok {
			return act, true
		}
	}

	return "", false
}

// Rules returns a copy of the loaded rules slice.
func (r *CustomRouter) Rules() []database.CustomRule {
	if r == nil {
		return nil
	}
	snap := r.snapshot.Load()
	if snap == nil || len(snap.rules) == 0 {
		return []database.CustomRule{}
	}
	out := make([]database.CustomRule, len(snap.rules))
	copy(out, snap.rules)
	return out
}

// normalizeFast avoids allocation when the domain is already trimmed, lowercase,
// and free of a trailing dot (the typical wire-query case).
func normalizeFast(domain string) string {
	n := len(domain)
	if n == 0 {
		return ""
	}
	needsNorm := domain[0] == ' ' || domain[n-1] == ' ' || domain[n-1] == '.'
	if !needsNorm {
		for i := 0; i < n; i++ {
			c := domain[i]
			if c >= 'A' && c <= 'Z' || c == '\t' || c == '\r' || c == '\n' {
				needsNorm = true
				break
			}
		}
	}
	if !needsNorm {
		return domain
	}
	return normalizeDomain(domain)
}
