#!/usr/bin/env bash
# =====================================================================
# IICPC PLATFORM · DigitalOcean droplet provisioner (doctl)
# ---------------------------------------------------------------------
# Creates a droplet with the cloud-init in this directory, waits for it
# to boot, prints the public IP and the live URL.
#
# Prerequisites:
#   - doctl installed:        brew install doctl
#   - authenticated:          doctl auth init
#   - SSH key in your DO account (note its fingerprint)
#
# Usage:
#   SSH_KEY=ab:cd:... ./deploy.sh
#   SSH_KEY=ab:cd:... REGION=blr1 SIZE=s-2vcpu-4gb ./deploy.sh
# =====================================================================
set -euo pipefail

NAME="${NAME:-iicpc-platform}"
REGION="${REGION:-blr1}"          # Bangalore. sgp1=Singapore, nyc3=NYC.
SIZE="${SIZE:-s-2vcpu-4gb}"       # ~$24/mo. Bump to s-4vcpu-8gb if needed.
IMAGE="${IMAGE:-ubuntu-22-04-x64}"
SSH_KEY="${SSH_KEY:?set SSH_KEY=<fingerprint or key-id>}"

CLOUD_INIT="$(dirname "$0")/cloud-init.yaml"
[ -f "$CLOUD_INIT" ] || { echo "missing $CLOUD_INIT"; exit 1; }

echo "▶ creating droplet $NAME in $REGION ($SIZE)..."
ID=$(doctl compute droplet create "$NAME" \
       --region "$REGION" \
       --size "$SIZE" \
       --image "$IMAGE" \
       --ssh-keys "$SSH_KEY" \
       --user-data-file "$CLOUD_INIT" \
       --enable-monitoring \
       --tag-names iicpc,platform \
       --format ID --no-header --wait)

echo "  → droplet id: $ID"

IP=$(doctl compute droplet get "$ID" --format PublicIPv4 --no-header)
echo "  → public ip:  $IP"

echo "▶ waiting for cloud-init to finish (5–8 min)..."
echo "  tip: ssh root@$IP 'tail -f /var/log/cloud-init-output.log'"
echo
echo "Once it's done:"
echo "  • Browse:  http://$IP/"
echo "  • SSH:     ssh root@$IP"
echo "  • Update:  ssh root@$IP 'cd /opt/iicpc && git pull && docker compose up -d --build'"
echo "  • Destroy: doctl compute droplet delete $ID"
