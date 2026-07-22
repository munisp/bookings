#!/usr/bin/env bash
# Provision LiveKit SIP inbound telephony (Wave 5 #1, docs/telephony.md).
#
# Creates the inbound trunk (numbers list) and the dispatch rule that routes
# inbound PSTN calls into `call-{dialed number}` rooms where the voice-agent
# worker's receptionist picks up.
#
# Prereqs: livekit-cli (`lk`) on PATH — https://github.com/livekit/livekit-cli
#
# Env:
#   LK_URL     LiveKit server URL      (default ws://localhost:7880)
#   LK_KEY     LiveKit API key         (default devkey — dev only!)
#   LK_SECRET  LiveKit API secret      (default secret — dev only!)
#   TRUNK_CONFIG    trunk YAML         (default deploy/livekit-sip/trunk-config.example.yaml)
#   DISPATCH_CONFIG dispatch-rule YAML (default deploy/livekit-sip/dispatch-rule.yaml)
set -euo pipefail

LK_URL="${LK_URL:-ws://localhost:7880}"
LK_KEY="${LK_KEY:-devkey}"
LK_SECRET="${LK_SECRET:-secret}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TRUNK_CONFIG="${TRUNK_CONFIG:-$HERE/trunk-config.example.yaml}"
DISPATCH_CONFIG="${DISPATCH_CONFIG:-$HERE/dispatch-rule.yaml}"

command -v lk >/dev/null 2>&1 || {
  echo "error: livekit-cli ('lk') not found on PATH." >&2
  echo "install: curl -sSL https://get.livekit.io/cli | bash" >&2
  exit 1
}

LK=(lk --url "$LK_URL" --api-key "$LK_KEY" --api-secret "$LK_SECRET")

echo "==> creating SIP inbound trunk from $TRUNK_CONFIG"
"${LK[@]}" sip inbound create "$TRUNK_CONFIG"

echo "==> creating SIP dispatch rule from $DISPATCH_CONFIG"
"${LK[@]}" sip dispatch create "$DISPATCH_CONFIG"

echo "==> verifying"
"${LK[@]}" sip inbound list
"${LK[@]}" sip dispatch list

cat <<'EOF'

Done. Next steps:
  1. Point the carrier's SIP trunk at this LiveKit server's SIP port
     (5060/udp+tcp, TLS 5061) for the numbers in the trunk config.
  2. Map each dialed number to a tenant on the voice worker:
       TENANT_PHONE_MAP='{"+15551234567":"acme"}'  (see docs/telephony.md)
  3. Call the number — the room `call-{number}` is dispatched to the
     receptionist agent.
EOF
