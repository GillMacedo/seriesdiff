#!/bin/sh
# Builds each version of examples/orders, runs it, sends the same fixed set of
# requests, and saves its /metrics output to OUT/orders-VERSION.prom, where OUT
# is the first argument or examples/scrapes. Needs Go, curl, and the network to
# download client_golang the first time. ADDR sets the listen address.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
out=${1:-$root/examples/scrapes}
addr=${ADDR:-127.0.0.1:18080}
for v in before after comments; do
  (cd "$root/examples/orders/$v" && go build -o "$tmp/orders-$v" .)
  "$tmp/orders-$v" -addr "$addr" &
  pid=$!
  i=0
  until curl -s -o /dev/null "http://$addr/health"; do
    i=$((i + 1))
    [ "$i" -gt 50 ] && { echo "orders-$v didn't start" >&2; kill "$pid"; exit 1; }
    sleep 0.1
  done
  for path in /orders /orders /orders; do curl -s -o /dev/null -X POST "http://$addr$path"; done
  for path in /orders/1001 /orders/1002 /orders/1003 /orders/abc /pay '/pay?fail=card_declined' '/pay?fail=insufficient_funds' /health; do
    curl -s -o /dev/null "http://$addr$path"
  done
  curl -sf "http://$addr/metrics" -o "$out/orders-$v.prom"
  kill "$pid"
  wait "$pid" 2>/dev/null || true
done
