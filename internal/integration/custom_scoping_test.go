package integration

import (
	"testing"

	"github.com/miekg/dns"
	"hyperdns/internal/database"
	"hyperdns/internal/service"
)

func TestPerClientScopedPoliciesAndDownloadBypass(t *testing.T) {
	f := newFixture(t, false)

	// Client with custom scoping enabled: only enable_epic allowed, downloads disabled (bypass relay)
	c := f.provision(t, service.CreateClientRequest{
		Name:            "Epic Gamer Scoped",
		Days:            30,
		IP:              subscriberIP,
		AllowedPolicies: []string{"enable_epic"},
		AllowDownloads:  false,
		UseCustomScope:  true,
	})
	if !c.UseCustomScope {
		t.Fatal("UseCustomScope is false")
	}

	// 1. Epic Games apex and store domains are proxied
	resp := f.resolveName(t, subscriberIP, "epicgames.com.")
	if len(resp.Answer) == 0 {
		t.Fatalf("epicgames.com came back with no answer")
	}
	if a, ok := resp.Answer[0].(*dns.A); !ok || a.A.String() != testPublicIP {
		t.Fatalf("epicgames.com answer = %v, want relay %s", resp.Answer[0], testPublicIP)
	}

	// 2. Steam domains (different preset) resolve DIRECT (not proxied to relay)
	respSteam := f.resolveName(t, subscriberIP, "store.steampowered.com.")
	for _, rr := range respSteam.Answer {
		if a, ok := rr.(*dns.A); ok && a.A.String() == testPublicIP {
			t.Errorf("store.steampowered.com was proxied for an account scoped only to Epic Games")
		}
	}

	// 3. Steam download CDN (steamcontent.com) resolves DIRECT because AllowDownloads=false
	respCDN := f.resolveName(t, subscriberIP, "steamcontent.com.")
	for _, rr := range respCDN.Answer {
		if a, ok := rr.(*dns.A); ok && a.A.String() == testPublicIP {
			t.Errorf("steamcontent.com was proxied when AllowDownloads=false")
		}
	}

	// 4. Add dynamic custom manual rule for a domain, reload into matcher
	f.dns.Matcher().ReloadCustomRules([]database.CustomRule{
		{ID: "rule1", Domain: "*.special-target.internal", Action: "proxy", Enabled: true},
	})
	respCustom := f.resolveName(t, subscriberIP, "api.special-target.internal.")
	if len(respCustom.Answer) == 0 {
		t.Fatalf("api.special-target.internal returned no answer")
	}
	if a, ok := respCustom.Answer[0].(*dns.A); !ok || a.A.String() != testPublicIP {
		t.Fatalf("api.special-target.internal = %v, want %s", respCustom.Answer[0], testPublicIP)
	}
}
