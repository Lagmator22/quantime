#!/usr/bin/env bash
# =====================================================================
# IICPC PLATFORM · end-to-end demo
# ---------------------------------------------------------------------
# Brings the stack up, submits the sample engine, runs a 30s stress
# test, and prints the leaderboard. Use this as the demo-day script.
# =====================================================================
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
TEAM_ID="${TEAM_ID:-t_demo}"

echo "▶ checking stack..."
until curl -sf "$BASE/api/health" >/dev/null; do
  echo "  …waiting for gateway"
  sleep 2
done
echo "✓ gateway up"

echo "▶ packaging sample engine..."
TMP=$(mktemp -d)
tar -czf "$TMP/sample.tar.gz" -C examples/sample-engine-go .

echo "▶ submitting..."
RESP=$(curl -sf -F "teamId=$TEAM_ID" -F "name=sample-engine" -F "lang=go" \
            -F "source=@$TMP/sample.tar.gz" "$BASE/api/submissions")
SUB_ID=$(echo "$RESP" | jq -r .id)
echo "  → submission id: $SUB_ID"

echo "▶ waiting for deploy..."
for i in {1..60}; do
  STATUS=$(curl -sf "$BASE/api/submissions/$SUB_ID" | jq -r .Status)
  echo "  status=$STATUS"
  if [ "$STATUS" = "deployed" ]; then break; fi
  if [ "$STATUS" = "failed" ];   then echo "✗ build failed"; exit 1; fi
  sleep 2
done

echo "▶ launching 30s stress run..."
RUN=$(curl -sf -H "Content-Type: application/json" -X POST \
       -d "{\"submissionId\":\"$SUB_ID\",\"profile\":\"sustained\",\"seed\":42,\"durationSec\":30,\"botsPerFleet\":50}" \
       "$BASE/api/runs")
RUN_ID=$(echo "$RUN" | jq -r .id)
echo "  → run id: $RUN_ID"

echo "▶ waiting for run to finish..."
sleep 35

echo "▶ leaderboard:"
curl -sf "$BASE/api/leaderboard" | jq '.[:10]'

echo "✓ demo complete"
