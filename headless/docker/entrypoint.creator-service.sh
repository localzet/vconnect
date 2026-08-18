#!/bin/sh
set -eu

SERVICE_AUTH_MODE="${SERVICE_AUTH_MODE:-remnawave}"
USER_IDS="${USER_IDS:-${USER_ID:-}}"
BINS_DIR="${BINS_DIR:-/opt/wlb/bin}"
SESSIONS_DIR="${SESSIONS_DIR:-/data/sessions}"
RESOURCES="${RESOURCES:-default}"
EGRESS_CONFIG="${EGRESS_CONFIG:-}"
FABRIC_ROUTES="${FABRIC_ROUTES:-}"
XRAY_BIN="${XRAY_BIN:-/usr/local/bin/xray}"
SERVICE_COOKIES="${SERVICE_COOKIES:-/data/cookies-yandex.json}"
SERVICE_VK_COOKIES="${SERVICE_VK_COOKIES:-}"
VAULT_DIR="${VAULT_DIR:-/data/vault}"
WORK_PLATFORM="${WORK_PLATFORM:-telemost}"
WORK_VK_PEER_ID="${WORK_VK_PEER_ID:-}"
WORK_COOKIE_SOURCE="${WORK_COOKIE_SOURCE:-user}"
MAX_ACTIVE_USERS="${MAX_ACTIVE_USERS:-32}"
WORK_TTL="${WORK_TTL:-30m}"
SERVICE_WRITE_FILE="${SERVICE_WRITE_FILE:-/data/service-call.txt}"
SERVICE_STATS_FILE="${SERVICE_STATS_FILE:-/data/active-users.txt}"

if [ "$SERVICE_AUTH_MODE" = "allowlist" ]; then
    [ -n "$USER_IDS" ] || { echo "USER_IDS is required in allowlist mode" >&2; exit 1; }
fi

if [ -n "${VAULT_KEY_FILE:-}" ]; then
    VAULT_KEY_BASE64="$(cat "$VAULT_KEY_FILE")"
fi
: "${VAULT_KEY_BASE64:?VAULT_KEY_BASE64 or VAULT_KEY_FILE is required}"

if [ "$SERVICE_AUTH_MODE" = "remnawave" ]; then
    if [ -n "${FABRIC_AUTH_KEY_FILE:-}" ]; then
        FABRIC_AUTH_KEY_BASE64="$(cat "$FABRIC_AUTH_KEY_FILE")"
    fi
    if [ -n "${REMNAWAVE_API_TOKEN_FILE:-}" ]; then
        REMNAWAVE_API_TOKEN="$(cat "$REMNAWAVE_API_TOKEN_FILE")"
    fi
    : "${FABRIC_AUTH_KEY_BASE64:?FABRIC_AUTH_KEY_BASE64 or FABRIC_AUTH_KEY_FILE is required}"
    : "${REMNAWAVE_URL:?REMNAWAVE_URL is required}"
    : "${REMNAWAVE_API_TOKEN:?REMNAWAVE_API_TOKEN or REMNAWAVE_API_TOKEN_FILE is required}"
    REMNAWAVE_LOOKUP="${REMNAWAVE_LOOKUP:-short-uuid}"
    AUTH_CACHE_TTL="${AUTH_CACHE_TTL:-30s}"
    AUTH_STALE_GRACE="${AUTH_STALE_GRACE:-5m}"
    [ -n "$FABRIC_ROUTES" ] || { echo "FABRIC_ROUTES is required in Remnawave Fabric mode" >&2; exit 1; }
    [ -x "$XRAY_BIN" ] || { echo "Xray binary is not executable: $XRAY_BIN" >&2; exit 1; }
fi

mkdir -p "$VAULT_DIR" "$SESSIONS_DIR"
if [ -z "${SERVICE_ROOM:-}" ] && [ -s "$SERVICE_WRITE_FILE" ]; then
    SERVICE_ROOM="$(head -n 1 "$SERVICE_WRITE_FILE" | tr -d '\r\n')"
    [ -n "$SERVICE_ROOM" ] && echo "Reusing service room from $SERVICE_WRITE_FILE"
fi
if [ -z "${SERVICE_ROOM:-}" ]; then
    rm -f "$SERVICE_WRITE_FILE"
fi

set -- \
    --service-control \
    --service-auth-mode "$SERVICE_AUTH_MODE" \
    --service-user-ids "$USER_IDS" \
    --cookies "$SERVICE_COOKIES" \
    --vault-dir "$VAULT_DIR" \
    --vault-key-base64 "$VAULT_KEY_BASE64" \
    --bins-dir "$BINS_DIR" \
    --sessions-dir "$SESSIONS_DIR" \
    --resources "$RESOURCES" \
    --work-platform "$WORK_PLATFORM" \
    --work-cookie-source "$WORK_COOKIE_SOURCE" \
    --max-active-users "$MAX_ACTIVE_USERS" \
    --work-ttl "$WORK_TTL" \
    --service-stats-file "$SERVICE_STATS_FILE" \
    --write-file "$SERVICE_WRITE_FILE"

[ -n "$EGRESS_CONFIG" ] && set -- "$@" --egress-config "$EGRESS_CONFIG"
[ -n "$WORK_VK_PEER_ID" ] && set -- "$@" --work-vk-peer-id "$WORK_VK_PEER_ID"
[ -n "$SERVICE_VK_COOKIES" ] && set -- "$@" --service-vk-cookies "$SERVICE_VK_COOKIES"
if [ "$SERVICE_AUTH_MODE" = "remnawave" ]; then
    set -- "$@" \
        --remnawave-url "$REMNAWAVE_URL" \
        --remnawave-api-token "$REMNAWAVE_API_TOKEN" \
        --remnawave-lookup "$REMNAWAVE_LOOKUP" \
        --fabric-auth-key-base64 "$FABRIC_AUTH_KEY_BASE64" \
        --auth-cache-ttl "$AUTH_CACHE_TTL" \
        --auth-stale-grace "$AUTH_STALE_GRACE" \
        --fabric-routes "$FABRIC_ROUTES" \
        --xray-bin "$XRAY_BIN"
fi
[ -n "${SERVICE_ROOM:-}" ] && set -- "$@" --tm-link "$SERVICE_ROOM"

exec "$BINS_DIR/headless-telemost-creator" "$@"
