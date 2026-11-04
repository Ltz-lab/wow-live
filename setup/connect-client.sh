#!/usr/bin/env bash
# One-time machine setup for tools/capture-live (macOS).
#
#   sudo setup/connect-client.sh                  set up for both clients
#   sudo setup/connect-client.sh --game retail    only the retail client's portal file
#   sudo setup/connect-client.sh undo             put everything back
#
# What it does:
#   1. makes a local CA and a certificate for the portal name in certs/
#      (bnetrelay shows it to the client; certs/ is git-ignored, keep it private)
#   2. trusts that CA in the System keychain (the client checks its system roots)
#   3. maps the portal name to 127.0.0.1 in /etc/hosts, so the client logs in
#      through bnetrelay on this Mac
#   4. Forever: writes ForeverLauncher.wtf, a copy of Config.wtf whose portal is
#      that name (Config.wtf itself is untouched). Retail's RelayPortal.wtf is
#      written by capture-live on its first run.
#
# Paths: WOW_FOREVER_DIR (default /Applications/World of Warcraft/_forever_),
# WOW_LIVE_CERTS (default certs/ in this repo).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CERTS="${WOW_LIVE_CERTS:-$REPO/certs}"
FOREVER="${WOW_FOREVER_DIR:-/Applications/World of Warcraft/_forever_}"
PORTAL=trinity.actual.battle.net
CA_NAME="wow-live local CA"
HOSTS_LINE="127.0.0.1 $PORTAL"
MARK="# wow-live capture relay"
GAME=all

[[ $EUID -eq 0 ]] || { echo "run with sudo: sudo $0 $*"; exit 1; }
OWNER="${SUDO_USER:-$(id -un)}"

case "${1:-}" in
  undo)
    sed -i '' "/$MARK/d" /etc/hosts
    dscacheutil -flushcache
    [[ -f "$CERTS/ca.cert.pem" ]] && security remove-trusted-cert -d "$CERTS/ca.cert.pem" 2>/dev/null || true
    security delete-certificate -c "$CA_NAME" /Library/Keychains/System.keychain 2>/dev/null || true
    rm -f "$FOREVER/WTF/ForeverLauncher.wtf"
    echo "undone (certs/ kept; delete it yourself if you like)"
    exit 0 ;;
  --game) GAME="${2:?--game forever|retail}" ;;
  "") ;;
  *) echo "usage: sudo $0 [--game forever|retail | undo]"; exit 2 ;;
esac

# 1. certificates
if [[ ! -f "$CERTS/server.chain.pem" ]]; then
  mkdir -p "$CERTS" && chmod 700 "$CERTS"
  cd "$CERTS"
  openssl req -x509 -new -nodes -newkey rsa:2048 -days 3650 -sha256 \
    -subj "/CN=$CA_NAME" -keyout ca.key.pem -out ca.cert.pem 2>/dev/null
  openssl req -new -nodes -newkey rsa:2048 -subj "/CN=$PORTAL" \
    -keyout server.key.pem -out server.csr 2>/dev/null
  printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' "$PORTAL" > ext.cnf
  openssl x509 -req -in server.csr -CA ca.cert.pem -CAkey ca.key.pem -CAcreateserial \
    -days 825 -sha256 -extfile ext.cnf -out server.cert.pem 2>/dev/null
  cat server.cert.pem ca.cert.pem > server.chain.pem
  rm -f server.csr ext.cnf
  chown -R "$OWNER" "$CERTS"
  chmod 600 "$CERTS"/*.pem
  echo "certs: made $CERTS (CA + $PORTAL)"
  cd - >/dev/null
fi

# 2. trust the CA
if ! security find-certificate -c "$CA_NAME" /Library/Keychains/System.keychain >/dev/null 2>&1; then
  security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$CERTS/ca.cert.pem"
  echo "keychain: trusted $CA_NAME"
fi

# 3. the portal name resolves here
if ! grep -qF "$HOSTS_LINE" /etc/hosts; then
  printf '%s %s\n' "$HOSTS_LINE" "$MARK" >> /etc/hosts
  echo "hosts: added $HOSTS_LINE"
fi
dscacheutil -flushcache

# 4. Forever's portal file
if [[ "$GAME" != retail && -d "$FOREVER" ]]; then
  mkdir -p "$FOREVER/WTF"
  W="$FOREVER/WTF/ForeverLauncher.wtf"
  { [[ -f "$FOREVER/WTF/Config.wtf" ]] && grep -v '^SET portal ' "$FOREVER/WTF/Config.wtf"; echo "SET portal \"$PORTAL\""; } > "$W"
  chown "$OWNER" "$W"
  echo "forever: wrote $W"
fi

echo "set up. Next: tools/capture-live --dry-run"
