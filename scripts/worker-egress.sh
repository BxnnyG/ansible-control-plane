#!/bin/sh
# Worker egress allowlist (iptables). Usage: worker-egress.sh apply|remove|status
# Reads ACP_WORKER_EGRESS_CIDRS and ACP_WORKER_EGRESS_PORTS from .env (grep only, never sourced).
# All rules match only the egress bridge and carry the comment "acp-egress".
set -eu

TAG="acp-egress"
ROOT="${ACP_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
ENV_FILE="${ACP_ENV_FILE:-$ROOT/.env}"
NET="${ACP_EGRESS_NETWORK:-acp_egress}"

getvar() {
	grep -E "^$1=" "$ENV_FILE" 2>/dev/null | tail -n 1 | cut -d= -f2- | tr -d "\"'" || true
}

bridge() {
	id=$(docker network inspect -f '{{.Id}}' "$NET" 2>/dev/null) || return 1
	[ -n "$id" ] || return 1
	printf 'br-%s' "$(printf '%s' "$id" | cut -c1-12)"
}

# Delete every rule tagged with our comment from a chain (any interface).
purge() {
	chain=$1
	iptables -S "$chain" 2>/dev/null | grep -F -- "--comment $TAG" | sed 's/^-A /-D /' |
		while read -r rule; do
			# shellcheck disable=SC2086
			eval iptables $rule
		done
}

cmd=${1:-}
case "$cmd" in
apply)
	cidrs=$(getvar ACP_WORKER_EGRESS_CIDRS | tr ',' ' ')
	ports=$(getvar ACP_WORKER_EGRESS_PORTS | tr -d ' ')
	if [ -z "$cidrs" ] || [ -z "$ports" ]; then
		echo "ACP_WORKER_EGRESS_CIDRS / ACP_WORKER_EGRESS_PORTS missing in $ENV_FILE; nothing changed" >&2
		exit 2
	fi
	case "$ports" in *[!0-9,]*) echo "invalid ports" >&2; exit 2 ;; esac
	for c in $cidrs; do
		case "$c" in *[!0-9./]*) echo "invalid CIDR: $c" >&2; exit 2 ;; esac
	done
	br=$(bridge) || { echo "network $NET not found; nothing changed" >&2; exit 3; }
	purge DOCKER-USER
	purge INPUT
	# Build in reverse so that -I 1 yields: established, allows, deny at the end of our block.
	for chain in DOCKER-USER INPUT; do
		iptables -A "$chain" -i "$br" -m comment --comment "$TAG" -j DROP
		if [ "$chain" = INPUT ]; then
			# Keep the DROP below pre-existing ACCEPT rules by inserting everything at the top in order.
			iptables -D INPUT -i "$br" -m comment --comment "$TAG" -j DROP
			iptables -I INPUT 1 -i "$br" -m comment --comment "$TAG" -j DROP
		fi
		for c in $cidrs; do
			iptables -I "$chain" 1 -i "$br" -d "$c" -p tcp -m multiport --dports "$ports" -m comment --comment "$TAG" -j ACCEPT
		done
		iptables -I "$chain" 1 -i "$br" -m conntrack --ctstate RELATED,ESTABLISHED -m comment --comment "$TAG" -j ACCEPT
	done
	echo "applied on $br: $(iptables -S DOCKER-USER | grep -c -F -- "--comment $TAG") DOCKER-USER, $(iptables -S INPUT | grep -c -F -- "--comment $TAG") INPUT rules"
	;;
remove)
	purge DOCKER-USER
	purge INPUT
	echo "removed"
	;;
status)
	echo "DOCKER-USER:"; iptables -S DOCKER-USER | grep -F -- "--comment $TAG" || true
	echo "INPUT:"; iptables -S INPUT | grep -F -- "--comment $TAG" || true
	;;
*)
	echo "usage: $0 apply|remove|status" >&2
	exit 64
	;;
esac
