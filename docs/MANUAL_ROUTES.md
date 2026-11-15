# Manual Routes — domains, subdomains, wildcards and SNI hosts

Manual Routes is the operator's own addition to the preset catalog. A name typed
into it is proxied **in addition to** the built-in policies, never instead of them:
the presets stay exactly as they are, and the manual list is layered on top.

- **Where:** Dashboard → **Policies** tab → the last panel of the *Gaming
  Policies* section, under the policy cards.
- **Storage:** the `custom_proxied` policy row (`data.db`), the same list the
  *Custom Proxied Domains* box on the Settings tab edits. Two views, one list.
- **Scope:** every client, including subscribers whose own policy selection does
  not mention the name. Manual routes are admin-wide, like the operator's block
  and direct lists.
- **Precedence:** a manual route outranks a preset that claims the same name, and
  it overrides the real-time forced-direct guard and the bulk-download veto for
  that name.

## What a route does

Adding a name has two effects on the same query path:

1. **DNS.** An `A` query for a routed name is answered with this server's public
   IP (`server.public_ip`), so the client connects here instead of to the real
   host. `AAAA` is answered with an empty `NOERROR` (the IPv6 sink), so a client
   that prefers IPv6 cannot bypass the route.
2. **SNI / Host relay.** The connection arrives at the SNI proxy, which reads the
   TLS `ClientHello` SNI (or the HTTP `Host` header on port 80) and relays the
   stream to the real host on the same port. Nothing is decrypted and no
   certificate is presented to the client, so the destination host's own TLS
   identity is what the client verifies.

That is why the panel labels each row `DNS + SNI`: one entry covers both planes.

## What you can type

| You type | Stored as | Covers |
|---|---|---|
| `example.com` | `example.com` | the site **and every subdomain** (`cdn.example.com`, `eu.cdn.example.com`) |
| `api.example.com` | `api.example.com` | that host and anything under it |
| `*.example.com` | `*.example.com` | subdomains only — **not** the apex `example.com` |
| `https://Example.COM/path?x=1` | `example.com` | scheme, path, query, case and trailing dot are stripped |
| `example.com:443` | `example.com` | the port is stripped |
| `"example.com"` | `example.com` | surrounding quotes are stripped |

Refused, with the reason shown in the panel: an IP address (`1.2.3.4` — add the
hostname instead), a bare single label (`com`, `localhost`), a wildcard anywhere
but the leftmost label (`foo.*.example.com`), an empty label (`a..b.com`), a
label starting or ending with a hyphen, a numeric last label (`example.123`), and
non-ASCII names — use the punycode (`xn--…`) form.

Rules are the standard DNS ones: 253 characters total, 63 per label, letters,
digits, hyphen and underscore.

## API

The panel talks to `POST`/`DELETE /api/rules/manual` (dashboard session auth, same
as every other admin route), under the install's admin path prefix.

```bash
# list
curl -s -H "Authorization: Bearer $TOKEN" https://dns.example.com/<admin-path>/api/rules/manual

# add (returns the canonical spelling)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"domain":"https://cdn.mygame.example/launcher"}' \
     https://dns.example.com/<admin-path>/api/rules/manual
# {"success":true,"domain":"cdn.mygame.example","kind":"host","count":1,...}

# remove
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
     'https://dns.example.com/<admin-path>/api/rules/manual?domain=cdn.mygame.example'
```

Responses: `200` on success, `400` with a human-readable `error` for input that
cannot be routed, `409` for a name already in the list, `404` when removing a name
that is not there, `401` without a session.

The same list can still be written as part of a full rules save
(`POST /api/config/rules` with `custom_proxied: [...]`); both paths canonicalise
and persist identically.

## Verifying a route

1. Add the name in the panel — it appears with a `DOMAIN` / `SUBDOMAIN` /
   `WILDCARD` badge.
2. From any client pointed at this resolver:
   ```bash
   dig +short cdn.mygame.example @<server-ip>
   ```
   The answer is `server.public_ip`.
3. The Live query stream shows the query with the rule **Custom Proxy** and the
   action `PROXY`. A name that is routed but resolved `DIRECT` there means a
   forced-direct guard owns it (real-time media / unproxyable port), not that the
   route was ignored.
4. Removing the row takes effect on the next query — the rule index is rebuilt and
   the cache flushed on every change, no restart needed.

## Limits worth knowing

- The relay substitutes the **address**, not the port. A client that reaches
  `foo.example` on 443 is relayed to the real host's 443; traffic on other ports
  cannot be carried by this design.
- Bulk traffic still counts against the client's quota, and against the VPS's
  egress: manual routes are not exempt from the meter.
- A route on a name that fronts a CDN will relay that CDN's whole edge for every
  subscriber on the install. Prefer the specific host over a wildcard on a
  shared domain.
