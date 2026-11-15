package matcher

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// The operator's hand-typed routes ("Manual Routes" in the dashboard, shown at
// the end of the Gaming Policies tab). They are not a preset: a route typed here
// is proxied in addition to whatever the presets already cover, applies to every
// client regardless of its own policy selection (see policyFilter.accept), and
// outranks presets that claim the same name.
//
// The matcher itself only needs a hostname. What this file adds is the layer in
// front of it: a single canonical form for anything a human types into the box.
// Operators paste in every shape there is — "https://Example.COM/path?x=1",
// "example.com:443", "*.example.com", ".example.com", "example.com." — and
// before this, each of those was stored verbatim, so "https://foo.com" sat in the
// list looking healthy and matched nothing, while "Foo.com" and "foo.com" were
// two separate rows that had to be deleted twice.

// Route kinds. Cosmetic: the dashboard uses them for the badge on each row, and
// they describe what the operator's entry covers. A plain hostname covers itself
// and every subdomain of it (that is how ruleSet.index encodes a non-wildcard
// entry), a wildcard covers subdomains only, and a name with three or more labels
// is simply a host inside a domain.
const (
	RouteKindDomain    = "domain"
	RouteKindHost      = "host"
	RouteKindWildcard  = "wildcard"
	routeMaxNameLength = 253
)

// RouteKind classifies an already-stored entry for display. Unknown shapes fall
// back to RouteKindDomain, the broadest label, so a row never renders without a
// badge.
func RouteKind(entry string) string {
	d := normalizeDomain(entry)
	if strings.HasPrefix(d, "*.") {
		return RouteKindWildcard
	}
	if strings.Count(d, ".") >= 2 {
		return RouteKindHost
	}
	return RouteKindDomain
}

// NormalizeManualRoute canonicalises one route the operator typed and reports why
// it cannot be used when it cannot. The returned form is what gets stored, so the
// saved list, the dashboard and the matcher index all agree on one spelling.
//
// Accepted input: hostname, subdomain, wildcard ("*.example.com"), a URL with a
// scheme/path/query, and any of those with a trailing dot, a leading dot, a port,
// userinfo or stray whitespace. Rejected: empty input, bare IP addresses, a
// single label ("localhost", "com" — a route that matches every TLD is never what
// the operator meant), a wildcard anywhere but the leftmost label, non-ASCII
// (the caller should add the punycode form), and anything with a space or an
// illegal character.
func NormalizeManualRoute(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	// Quotes come along when an operator copies a value out of a config file or a
	// shell command line.
	s = strings.Trim(s, "`'\"")
	if s == "" {
		return "", errors.New("enter a domain, subdomain or wildcard (e.g. example.com, api.example.com, *.example.com)")
	}

	// A pasted URL: drop scheme, then path/query/fragment, then userinfo.
	if i := strings.Index(s, "://"); i > 0 && !strings.ContainsAny(s[:i], "./?") {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}

	// An address is never a route: the resolver answers with this server's own IP,
	// and the SNI proxy relays by name, so an IP entry would silently do nothing.
	if ip := net.ParseIP(strings.Trim(s, "[]")); ip != nil {
		return "", fmt.Errorf("%s is an IP address — add the hostname that should be routed instead", s)
	}

	// Port. Bracketed IPv6 is already gone above; a bare "host:port" keeps the host.
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}

	// ".example.com" (the DNS presentation form for "any subdomain") and
	// "example.com." (fully-qualified) both mean the same name.
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	s = strings.TrimPrefix(s, ".")
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", errors.New("that is not a domain — add at least one dot, e.g. example.com")
	}

	wildcard := false
	if strings.HasPrefix(s, "*.") {
		wildcard = true
		s = s[2:]
	}
	if strings.Contains(s, "*") {
		return "", errors.New("a wildcard is only supported as the leftmost label, e.g. *.example.com")
	}
	if s == "" {
		return "", errors.New("that is not a domain — add at least one dot, e.g. example.com")
	}

	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%q is not a domain — add at least one dot, e.g. example.com", strings.TrimSuffix(s, "."))
	}
	if len(s) > routeMaxNameLength {
		return "", fmt.Errorf("the name is longer than %d characters", routeMaxNameLength)
	}
	for _, label := range labels {
		switch {
		case label == "":
			return "", errors.New("the domain has an empty label (two dots in a row)")
		case len(label) > 63:
			return "", fmt.Errorf("the label %q is longer than 63 characters", label)
		case label[0] == '-' || label[len(label)-1] == '-':
			return "", fmt.Errorf("the label %q starts or ends with a hyphen", label)
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
				continue
			}
			if ch >= 0x80 {
				return "", errors.New("non-ASCII characters are not supported — add the punycode (xn--) form of the name")
			}
			return "", fmt.Errorf("invalid character %q in the domain", string(ch))
		}
	}

	// A trailing numeric label is what an address looks like when it slipped
	// through as text ("192.168.1" with the last octet typed wrong). Requiring a
	// letter in the last label keeps those out without rejecting legitimate
	// numeric hosts such as "123.example.com".
	if !strings.ContainsAny(labels[len(labels)-1], "abcdefghijklmnopqrstuvwxyz") {
		return "", errors.New("the last part of the domain must contain a letter")
	}

	if wildcard {
		return "*." + s, nil
	}
	return s, nil
}

// NormalizeRouteList canonicalises a stored list, dropping entries that cannot be
// honoured and duplicates that differ only in case or presentation. The dropped
// values are returned rather than silently discarded so a caller can tell the
// operator which typed rows did nothing.
func NormalizeRouteList(entries []string) (normalized, dropped []string) {
	if len(entries) == 0 {
		return nil, nil
	}
	normalized = make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name, err := NormalizeManualRoute(entry)
		if err != nil {
			dropped = append(dropped, entry)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		normalized = append(normalized, name)
	}
	return normalized, dropped
}
