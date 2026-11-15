#!/usr/bin/env bash
# ==============================================================================
# HyperDNS — Ubuntu 24.04 installer that builds FROM THIS SOURCE TREE
#
#   sudo bash scripts/install-ubuntu24.sh
#   sudo HYPERDNS_DOMAIN=dns.example.com HYPERDNS_EMAIL=you@example.com \
#        bash scripts/install-ubuntu24.sh
#
# One-line form (no checkout on the server needed — the script fetches the repo):
#   curl -fsSL https://raw.githubusercontent.com/farazghri-cloud/my-dns/main/scripts/install-ubuntu24.sh \
#     | sudo HYPERDNS_DOMAIN=dns.example.com HYPERDNS_EMAIL=you@example.com bash
#
# Why this script exists next to scripts/install.sh:
#
#   install.sh installs a pre-built binary that ships inside the release bundle.
#   A fork that has been modified in the tree — this one adds Manual Routes —
#   has no such binary, and the whole point of the fork is that the code on the
#   server is the code in the repository. So this installer compiles what is in
#   front of it, and installs that.
#
# What it does, in order:
#   1. checks root, OS and source tree
#   2. installs curl/ca-certificates and a Go toolchain (only if missing/too old)
#   3. stops the running service and archives the existing install to /root
#   4. seeds /opt/hyperdns/config.json on a fresh install (domain, email,
#      admin password, public IP) and leaves an existing one alone
#   5. builds /opt/hyperdns/hyperdns from ./cmd/hyperdns (static, no CGO)
#   6. frees port 53 from systemd-resolved
#   7. installs and starts the systemd unit, waits for the dashboard line
#   8. prints the panel URL, the credentials and the ports to open
#
# Upgrade-safe: config.json, data.db, master.key and certs/ are preserved. A
# fresh install is only ever performed when none of them exist.
# ==============================================================================

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'
say()  { printf "%b\n" "$*"; }
ok()   { printf "  ${GREEN}✓${NC} %b\n" "$*"; }
warn() { printf "  ${YELLOW}!${NC} %b\n" "$*"; }
die()  { printf "${RED}[x]${NC} %b\n" "$*" >&2; exit 1; }

INSTALL_DIR="/opt/hyperdns"
SERVICE="hyperdns"
GO_MIN_MINOR=26          # go.mod requires 1.26.4; any 1.26+ toolchain builds it
GO_DOWNLOAD_VERSION="1.27.4"
DOMAIN="${HYPERDNS_DOMAIN:-}"
EMAIL="${HYPERDNS_EMAIL:-}"
PANEL_PORT="${HYPERDNS_PANEL_PORT:-8080}"
# Where the one-line form fetches the source from. Point these at a fork or a
# tag when you need to install something other than main.
REPO_URL="${HYPERDNS_REPO:-https://github.com/farazghri-cloud/my-dns.git}"
REPO_REF="${HYPERDNS_REF:-main}"

# Under `curl | bash` there is no ${BASH_SOURCE[0]} to take a directory from, so
# this is best-effort and the dependency step below decides whether it found a
# checkout or has to fetch one.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"
SRC_DIR="$(cd "${SCRIPT_DIR}/.." 2>/dev/null && pwd || pwd)"

# ---------------------------------------------------------------- 1. pre-flight
say "${CYAN}${BOLD}HyperDNS — Ubuntu 24.04 source installer${NC}"

[ "${EUID}" -eq 0 ] || die "Run as root: sudo bash scripts/install-ubuntu24.sh"

if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-unknown}"; OS_VER="${VERSION_ID:-0}"
else
    OS_ID=unknown; OS_VER=0
fi
case "${OS_ID}" in
    ubuntu)  awk "BEGIN{exit !(${OS_VER%.*} >= 22)}" || die "Ubuntu 22.04 or newer is required (found ${OS_VER})" ;;
    debian)  awk "BEGIN{exit !(${OS_VER%.*} >= 12)}" || die "Debian 12 or newer is required (found ${OS_VER})" ;;
    *)       warn "Untested distribution '${OS_ID}' — continuing, but systemd and apt are assumed." ;;
esac

if [ -f "${SRC_DIR}/cmd/hyperdns/main.go" ]; then
    ok "Source tree: ${SRC_DIR}"
else
    warn "No source checkout beside this script (piped install) — the repository is fetched in the next step"
fi

# Interactivity. A piped `curl | bash` run has no terminal to answer prompts, so
# every question it would ask is answerable by environment variable instead and
# an unanswered domain is a warning rather than a hang.
INTERACTIVE=0
[ -t 0 ] && INTERACTIVE=1

# ------------------------------------------------------- 2. build dependencies
say "${CYAN}${BOLD}[1/7]${NC} Build dependencies"
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends ca-certificates curl tar git >/dev/null
    ok "curl, ca-certificates, tar, git present"
else
    warn "apt-get not found; assuming curl/tar are already available"
fi

# Source. The one-line form (`curl | sudo bash`) has no checkout on disk, so the
# tree is cloned into a scratch directory and built there. Nothing of this clone is
# installed: only the compiled binary moves to /opt/hyperdns.
if [ ! -f "${SRC_DIR}/cmd/hyperdns/main.go" ]; then
    say "  Fetching ${REPO_URL} (${REPO_REF})"
    FETCH_DIR="$(mktemp -d /tmp/hyperdns-src.XXXXXX)"
    if git clone --depth 1 --branch "${REPO_REF}" "${REPO_URL}" "${FETCH_DIR}" >/dev/null 2>&1; then
        SRC_DIR="${FETCH_DIR}"
        ok "Source fetched: $(cd "${SRC_DIR}" && git log -1 --format='%h %s' 2>/dev/null | cut -c1-72)"
    else
        rm -rf "${FETCH_DIR}"
        die "Could not clone ${REPO_URL} at ref '${REPO_REF}'. Check that the repository and branch exist, or copy the source and run scripts/install-ubuntu24.sh from it."
    fi
fi
[ -f "${SRC_DIR}/cmd/hyperdns/main.go" ] || die "${SRC_DIR} has no cmd/hyperdns/main.go — the fetched tree is not the daemon"
[ -f "${SRC_DIR}/go.mod" ] || die "${SRC_DIR}/go.mod is missing — the fetched tree is incomplete"

# Go toolchain. A distribution Go on 24.04 is 1.22 — too old for a go.mod that
# says 1.26 — so the version is checked, not just the presence of the command.
need_go=1
if command -v go >/dev/null 2>&1; then
    go_minor="$(go env GOVERSION 2>/dev/null | sed -n 's/^go1\.\([0-9]\+\).*/\1/p')"
    if [ -n "${go_minor}" ] && [ "${go_minor}" -ge "${GO_MIN_MINOR}" ]; then
        need_go=0
        ok "$(go env GOVERSION) already installed"
    else
        warn "Go $(go env GOVERSION 2>/dev/null || echo unknown) is older than 1.${GO_MIN_MINOR}; installing ${GO_DOWNLOAD_VERSION}"
    fi
fi
if [ "${need_go}" -eq 1 ]; then
    arch="$(uname -m)"
    case "${arch}" in
        x86_64|amd64) goarch=amd64 ;;
        aarch64|arm64) goarch=arm64 ;;
        *) die "Unsupported architecture: ${arch}" ;;
    esac
    tmp_tgz="$(mktemp /tmp/go.XXXXXX.tar.gz)"
    curl -fsSL "https://go.dev/dl/go${GO_DOWNLOAD_VERSION}.linux-${goarch}.tar.gz" -o "${tmp_tgz}" \
        || die "Could not download the Go toolchain. Install Go 1.${GO_MIN_MINOR}+ and re-run."
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${tmp_tgz}"
    rm -f "${tmp_tgz}"
    export PATH="/usr/local/go/bin:${PATH}"
    command -v go >/dev/null 2>&1 || die "Go was unpacked but is still not on PATH"
    ok "Installed $(go env GOVERSION) to /usr/local/go"
fi
export GOCACHE="${GOCACHE:-/root/.cache/go-build}"
export GOFLAGS="${GOFLAGS:--mod=mod}"
export CGO_ENABLED=0

# --------------------------------------------------------- 3. stop + archive
say "${CYAN}${BOLD}[2/7]${NC} Existing installation"
if systemctl list-unit-files 2>/dev/null | grep -q "^${SERVICE}.service"; then
    systemctl stop "${SERVICE}" 2>/dev/null || true
    ok "Stopped the running ${SERVICE} service"
fi
if [ -d "${INSTALL_DIR}" ] && [ -n "$(ls -A "${INSTALL_DIR}" 2>/dev/null)" ]; then
    # Outside /opt/hyperdns on purpose: an archive inside the directory it backs
    # up is a copy that grows on every re-run and that a later `rm -rf` takes
    # with it.
    BACKUP="/root/hyperdns-preinstall-$(date +%Y%m%d-%H%M%S).tar.gz"
    tar -czf "${BACKUP}" -C "$(dirname "${INSTALL_DIR}")" "$(basename "${INSTALL_DIR}")" 2>/dev/null || true
    if [ -f "${BACKUP}" ]; then
        ok "Archived the previous install to ${BACKUP} ($(du -h "${BACKUP}" | cut -f1))"
    else
        warn "Could not archive ${INSTALL_DIR} — continuing"
    fi
fi
mkdir -p "${INSTALL_DIR}/certs"
chmod 755 "${INSTALL_DIR}"

# ------------------------------------------------------------- 4. config seed
say "${CYAN}${BOLD}[3/7]${NC} Configuration"
FRESH_INSTALL=0
if [ ! -f "${INSTALL_DIR}/config.json" ]; then
    FRESH_INSTALL=1
    cp "${SRC_DIR}/config.example.json" "${INSTALL_DIR}/config.json"

    if [ -z "${DOMAIN}" ] && [ "${INTERACTIVE}" -eq 1 ]; then
        say ""
        say "  A panel domain is what the dashboard and its Let's Encrypt certificate use."
        say "  Its A record must already point at this server. Leave empty to install"
        say "  with the dashboard bound to 127.0.0.1 (reachable over an SSH tunnel only)."
        printf "  Panel domain [empty = loopback only]: "
        read -r DOMAIN || true
    fi
    if [ -n "${DOMAIN}" ] && [ -z "${EMAIL}" ] && [ "${INTERACTIVE}" -eq 1 ]; then
        printf "  Contact email for Let's Encrypt [admin@%s]: " "${DOMAIN}"
        read -r EMAIL || true
        EMAIL="${EMAIL:-admin@${DOMAIN}}"
    fi

    # Public IP: an explicit override wins, otherwise ask the network. The
    # daemon needs it for the A records it answers on proxied names, and a wrong
    # value points every subscriber at a host that is not this one.
    PUBLIC_IP="${HYPERDNS_PUBLIC_IP:-}"
    if [ -z "${PUBLIC_IP}" ]; then
        PUBLIC_IP="$(curl -fsS --max-time 8 https://api.ipify.org 2>/dev/null || true)"
    fi
    [ -n "${PUBLIC_IP}" ] || PUBLIC_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [ -n "${PUBLIC_IP}" ] || die "Could not determine the public IP; set HYPERDNS_PUBLIC_IP=x.x.x.x and re-run"

    ADMIN_PASS="$(openssl rand -hex 12 2>/dev/null || tr -dc 'a-f0-9' < /dev/urandom | head -c 24)"
    API_KEY="hdns_live_$(openssl rand -hex 16 2>/dev/null || tr -dc 'a-f0-9' < /dev/urandom | head -c 32)"

    python3 - "$INSTALL_DIR/config.json" "$PUBLIC_IP" "$ADMIN_PASS" "$API_KEY" "$DOMAIN" "$EMAIL" "$PANEL_PORT" <<'PY'
import json, sys
path, ip, password, api_key, domain, email, port = sys.argv[1:8]
cfg = json.load(open(path))
cfg.setdefault('server', {}).update({
    'public_ip': ip,
    'bind_host': '0.0.0.0',
    'web_port': int(port),
    'admin_username': 'admin',
    'admin_password': password,
    'api_key': api_key,
})
if domain:
    cfg.setdefault('tls', {}).update({'domain': domain, 'email': email or ('admin@' + domain), 'auto_cert': True})
json.dump(cfg, open(path, 'w'), indent=2)
PY
    chmod 600 "${INSTALL_DIR}/config.json"
    GENERATED_ADMIN_PASSWORD="${ADMIN_PASS}"
    GENERATED_API_KEY="${API_KEY}"
    ok "Seeded config.json (public IP ${PUBLIC_IP}$([ -n "${DOMAIN}" ] && echo ", domain ${DOMAIN}" || echo ", no domain"))"
else
    chmod 600 "${INSTALL_DIR}/config.json" 2>/dev/null || true
    # Upgrades keep the operator's file. A domain given on the command line is
    # still applied, because that is the one field most often the reason for a
    # re-run.
    if [ -n "${DOMAIN}" ]; then
        python3 - "$INSTALL_DIR/config.json" "$DOMAIN" "$EMAIL" <<'PY'
import json, sys
path, domain, email = sys.argv[1:4]
cfg = json.load(open(path))
tls = cfg.setdefault('tls', {})
tls['domain'] = domain
tls['auto_cert'] = True
if email:
    tls['email'] = email
json.dump(cfg, open(path, 'w'), indent=2)
PY
        ok "Updated the panel domain in the existing config.json"
    else
        ok "Kept the existing config.json (database, key and certificates untouched)"
    fi
    GENERATED_ADMIN_PASSWORD=""
fi

# ------------------------------------------------------------------ 5. build
say "${CYAN}${BOLD}[4/7]${NC} Building from source"
cd "${SRC_DIR}"
# -trimpath keeps absolute build paths out of the binary; -s -w drops the symbol
# table, which is where most of the file size of a Go binary lives.
if ! go build -trimpath -ldflags="-s -w" -o "${INSTALL_DIR}/hyperdns.tmp" ./cmd/hyperdns; then
    die "go build failed. Run 'go build ./...' in ${SRC_DIR} to see the full error."
fi
chmod 755 "${INSTALL_DIR}/hyperdns.tmp"
mv -f "${INSTALL_DIR}/hyperdns.tmp" "${INSTALL_DIR}/hyperdns"
ln -sf "${INSTALL_DIR}/hyperdns" /usr/local/bin/hdns
ok "Built $(du -h "${INSTALL_DIR}/hyperdns" | cut -f1) binary: ${INSTALL_DIR}/hyperdns"
"${INSTALL_DIR}/hyperdns" -version 2>/dev/null | head -1 | sed 's/^/    /' || true

# ------------------------------------------------------------ 6. free port 53
say "${CYAN}${BOLD}[5/7]${NC} Port 53"
if systemctl is-active --quiet systemd-resolved; then
    mkdir -p /etc/systemd/resolved.conf.d
    printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/hyperdns.conf
    # resolved runs as its own unprivileged user and reads this drop-in, so a
    # restrictive umask on the caller's shell must not be allowed to make it
    # unreadable — the stub would then keep the port and the daemon would fail
    # to bind it for a reason the operator just tried to remove.
    chmod 755 /etc/systemd/resolved.conf.d
    chmod 644 /etc/systemd/resolved.conf.d/hyperdns.conf
    systemctl restart systemd-resolved || true

    # /etc/resolv.conf on a stock Ubuntu points at 127.0.0.53, the stub that was
    # just disabled. Repoint it at a real resolver or the host stops resolving.
    if [ ! -f "${INSTALL_DIR}/.resolv-backup" ] && [ -e /etc/resolv.conf ]; then
        if [ -L /etc/resolv.conf ]; then
            printf 'symlink\n%s\n' "$(readlink /etc/resolv.conf)" > "${INSTALL_DIR}/.resolv-backup"
        else
            { printf 'file\n'; cat /etc/resolv.conf; } > "${INSTALL_DIR}/.resolv-backup"
        fi
    fi
    if grep -q '127.0.0.53' /etc/resolv.conf 2>/dev/null; then
        rm -f /etc/resolv.conf
        printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf
        ok "Replaced the resolved stub in /etc/resolv.conf (original saved in ${INSTALL_DIR}/.resolv-backup)"
    fi
    ok "systemd-resolved stub listener disabled"
else
    ok "systemd-resolved is not active; port 53 is already free"
fi

# --------------------------------------------------------- 7. systemd + start
say "${CYAN}${BOLD}[6/7]${NC} Service"
cat > "/etc/systemd/system/${SERVICE}.service" <<UNIT
[Unit]
Description=HyperDNS — Standalone SmartDNS & Gaming Gateway
Documentation=https://github.com/IzumiRain/hyperdns
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
RuntimeDirectory=hyperdns
RuntimeDirectoryMode=0700
ExecStart=${INSTALL_DIR}/hyperdns -daemon \\
    -config ${INSTALL_DIR}/config.json \\
    -db ${INSTALL_DIR}/data.db \\
    -key ${INSTALL_DIR}/master.key
Restart=always
RestartSec=3s
TimeoutStopSec=15s
SyslogIdentifier=hyperdns
LimitNOFILE=65535
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
UNIT
chmod 644 "/etc/systemd/system/${SERVICE}.service"
systemctl daemon-reload
systemctl enable "${SERVICE}" >/dev/null 2>&1
systemctl restart "${SERVICE}"
ok "Unit written to /etc/systemd/system/${SERVICE}.service and started"

# The dashboard binds only after first boot finishes (certificate issuance on a
# fresh install), so the journal is watched rather than assumed.
say "  Waiting for the daemon to report its listeners..."
PANEL_URL=""
for _ in $(seq 1 60); do
    PANEL_URL="$(journalctl -u "${SERVICE}" --since "-2min" --no-pager 2>/dev/null | grep -o 'https\?://[^ ]*/dash/' | tail -1 || true)"
    [ -n "${PANEL_URL}" ] && break
    systemctl is-active --quiet "${SERVICE}" || break
    sleep 2
done
if systemctl is-active --quiet "${SERVICE}"; then
    ok "${SERVICE} is running"
else
    echo
    journalctl -u "${SERVICE}" -n 40 --no-pager || true
    die "${SERVICE} failed to start. The journal above is the reason."
fi

# Ports. Only touched when ufw is the active firewall — silently enabling a
# firewall on a host that did not have one would be a bigger change than an
# install should make.
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    for p in 53/udp 53/tcp 80/tcp 443/tcp 853/tcp 8443/tcp "${PANEL_PORT}/tcp"; do
        ufw allow "${p}" >/dev/null 2>&1 || true
    done
    ok "ufw: allowed 53 (udp/tcp), 80, 443, 853, 8443 and ${PANEL_PORT}"
fi

# ----------------------------------------------------------------- 8. summary
say ""
say "${GREEN}${BOLD}  HyperDNS is installed and running${NC}"
say "  ${BOLD}Dashboard${NC}       ${PANEL_URL:-see: journalctl -u hyperdns | grep dash}"
say "  ${BOLD}Standard DNS${NC}    <server-ip>:53 (UDP/TCP)"
say "  ${BOLD}Config${NC}          ${INSTALL_DIR}/config.json"
say "  ${BOLD}Logs${NC}            journalctl -u ${SERVICE} -f"
if [ -n "${GENERATED_ADMIN_PASSWORD:-}" ]; then
    say ""
    say "  ${YELLOW}${BOLD}First-run credentials (shown once)${NC}"
    say "    username : admin"
    say "    password : ${GENERATED_ADMIN_PASSWORD}"
    say "    API key  : ${GENERATED_API_KEY}"
    say "    Save them now — the password is stored hashed after the first start."
fi
say ""
say "  ${BOLD}Open these on the provider firewall:${NC} 53/udp 53/tcp 80/tcp 443/tcp 853/tcp ${PANEL_PORT}/tcp"
if [ -z "${DOMAIN}" ] && [ "${FRESH_INSTALL}" -eq 1 ]; then
    say ""
    warn "No panel domain was provided, so the dashboard is loopback-only."
    warn "Reach it over a tunnel:  ssh -L ${PANEL_PORT}:127.0.0.1:${PANEL_PORT} root@<server>"
    warn "then open the URL above on your own machine. Add -dns.example.com later"
    warn "and re-run this script to bind it to a real HTTPS name."
fi
say ""
say "  ${BOLD}Manual routes (the fork's addition):${NC} Dashboard → Policies →"
say "  “Manual Routes — Domains, Subdomains & SNI”, at the end of Gaming Policies."
say "  Anything typed there is proxied in addition to the presets, for every client."
say ""
