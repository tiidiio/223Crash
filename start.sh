#!/data/data/com.termux/files/usr/bin/bash

set -Eeuo pipefail

# ============================================================
# 223GAMING STUDIO
# STARTUP ORCHESTRATOR
# Provider Core + Game Products
# ============================================================

PROJECT_DIR="${HOME}/223Gaming/223Crash"
cd "$PROJECT_DIR"

# ============================================================
# COLORS
# ============================================================

if [[ -t 1 ]]; then
    RESET=$'\033[0m'
    BOLD=$'\033[1m'
    RED=$'\033[31m'
    GREEN=$'\033[32m'
    YELLOW=$'\033[33m'
    BLUE=$'\033[34m'
    MAGENTA=$'\033[35m'
    CYAN=$'\033[36m'
    WHITE=$'\033[37m'
else
    RESET=""
    BOLD=""
    RED=""
    GREEN=""
    YELLOW=""
    BLUE=""
    MAGENTA=""
    CYAN=""
    WHITE=""
fi

# Compatibility alias used by the Studio header.
CY="$CYAN"

# ============================================================
# ENVIRONMENT
# ============================================================

if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$PROJECT_DIR/.env"
    set +a
fi

HOST="${HOST:-127.0.0.1}"

HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"
MOCKWALLET_PORT="${MOCKWALLET_PORT:-9000}"

START_MOCKWALLET="${START_MOCKWALLET:-0}"

# ============================================================
# DIRECTORIES
# ============================================================

BIN_DIR="$PROJECT_DIR/bin"
LOG_DIR="$PROJECT_DIR/logs"
RUNTIME_DIR="$PROJECT_DIR/.runtime"
PID_DIR="$RUNTIME_DIR/pids"

mkdir -p \
    "$BIN_DIR" \
    "$LOG_DIR" \
    "$PID_DIR"

# ============================================================
# PID FILES
# ============================================================

PROVIDER_PID_FILE="$PID_DIR/provider-server.pid"
MOCKWALLET_PID_FILE="$PID_DIR/mockwallet.pid"

# ============================================================
# LOG FILES
# ============================================================

PROVIDER_LOG="$LOG_DIR/provider-server.log"
MOCKWALLET_LOG="$LOG_DIR/mockwallet.log"
BUILD_LOG="$LOG_DIR/build.log"
MOCK_BUILD_LOG="$LOG_DIR/build-mockwallet.log"

# ============================================================
# BINARIES
# ============================================================

SERVER_BIN="$BIN_DIR/server"
MOCKWALLET_BIN="$BIN_DIR/mockwallet"

# ============================================================
# FUNCTIONS
# ============================================================

die() {
    printf "\n${RED}${BOLD}ERROR:${RESET} %s\n" "$*" >&2
    exit 1
}

info() {
    printf "${CYAN}▸${RESET} %s\n" "$*"
}

success() {
    printf "${GREEN}✓${RESET} %s\n" "$*"
}

warn() {
    printf "${YELLOW}⚠${RESET} %s\n" "$*"
}

section() {
    printf "\n${BLUE}${BOLD}== %s ==${RESET}\n" "$*"
}

pid_alive() {
    local pid="$1"

    [[ "$pid" =~ ^[0-9]+$ ]] || return 1
    kill -0 "$pid" 2>/dev/null
}

pid_matches_binary() {
    local pid="$1"
    local expected="$2"

    pid_alive "$pid" || return 1

    [[ -r "/proc/$pid/cmdline" ]] || return 1

    local cmdline
    cmdline="$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null || true)"

    [[ "$cmdline" == *"$expected"* ]]
}

read_pid() {
    local pid_file="$1"

    [[ -f "$pid_file" ]] || return 1

    local pid
    pid="$(cat "$pid_file" 2>/dev/null || true)"

    [[ "$pid" =~ ^[0-9]+$ ]] || return 1

    printf '%s' "$pid"
}

cleanup_stale_pid() {
    local pid_file="$1"
    local expected_binary="$2"

    if [[ ! -f "$pid_file" ]]; then
        return 0
    fi

    local pid
    pid="$(read_pid "$pid_file" || true)"

    if [[ -z "$pid" ]]; then
        rm -f "$pid_file"
        return 0
    fi

    if pid_matches_binary "$pid" "$expected_binary"; then
        die "Process already running: $expected_binary (PID $pid)"
    fi

    rm -f "$pid_file"
}

wait_for_http() {
    local url="$1"
    local attempts="${2:-30}"

    local i
    for ((i=1; i<=attempts; i++)); do
        if curl -fsS --max-time 1 "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done

    return 1
}

port_reachable() {
    local host="$1"
    local port="$2"

    if command -v nc >/dev/null 2>&1; then
        nc -z "$host" "$port" >/dev/null 2>&1
        return $?
    fi

    if command -v curl >/dev/null 2>&1; then
        curl \
            --silent \
            --output /dev/null \
            --max-time 1 \
            "http://${host}:${port}/" >/dev/null 2>&1

        return $?
    fi

    return 1
}

# ============================================================
# HEADER
# ============================================================

clear 2>/dev/null || true

BOOT_TIME="$(date '+%Y-%m-%d %H:%M:%S')"

if command -v go >/dev/null 2>&1; then
    GO_VERSION="$(go version | awk '{print $3}')"
else
    GO_VERSION="absent"
fi

printf "\n"
printf "${CY}╔══════════════════════════════════════════════════════╗${RESET}\n"
printf "${CY}║ ${WHITE}${BOLD}223GAMING STUDIO${RESET}${CY} • ${MAGENTA}LAB iGAMING B2B${CY}             ║${RESET}\n"
printf "${CY}║ ${WHITE}Provider Core + Game Products${CY}                    ║${RESET}\n"
printf "${CY}╠══════════════════════════════════════════════════════╣${RESET}\n"
printf "${CY}║ ${YELLOW}${BOLD}FOUNDER & CEO${RESET}${CY}                                  ║${RESET}\n"
printf "${CY}║ ${WHITE}Tidiane Diallo${CY}                                  ║${RESET}\n"
printf "${CY}║ ${WHITE}Moteur : Go${CY}                                      ║${RESET}\n"
printf "${CY}║ ${WHITE}Architecture : Provider B2B${CY}                     ║${RESET}\n"
printf "${CY}║ ${WHITE}Boot : %-41s${CY} ║${RESET}\n" "$BOOT_TIME"
printf "${CY}╚══════════════════════════════════════════════════════╝${RESET}\n"
printf "\n"

# ============================================================
# PRECHECK
# ============================================================

section "PRECHECK"

[[ -d "$PROJECT_DIR" ]] || die "Project directory not found: $PROJECT_DIR"

command -v go >/dev/null 2>&1 \
    || die "Go is not installed."

command -v curl >/dev/null 2>&1 \
    || die "curl is required."

success "Project : $PROJECT_DIR"
success "Go      : $GO_VERSION"

# ============================================================
# STOP DUPLICATE INSTANCES
# ============================================================

section "PROCESS CHECK"

cleanup_stale_pid "$PROVIDER_PID_FILE" "$SERVER_BIN"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    cleanup_stale_pid "$MOCKWALLET_PID_FILE" "$MOCKWALLET_BIN"
else
    if [[ -f "$MOCKWALLET_PID_FILE" ]]; then
        warn "MockWallet PID file exists but START_MOCKWALLET=0."
        rm -f "$MOCKWALLET_PID_FILE"
    fi
fi

success "No conflicting Studio process detected."

# ============================================================
# BUILD CONFIGURATION
# ============================================================

export GOFLAGS="${GOFLAGS:--p=1}"
export GOGC="${GOGC:-50}"
export CGO_ENABLED="${CGO_ENABLED:-0}"

section "BUILD CONFIGURATION"

printf "  GOFLAGS      = %s\n" "$GOFLAGS"
printf "  GOGC         = %s\n" "$GOGC"
printf "  CGO_ENABLED  = %s\n" "$CGO_ENABLED"

# ============================================================
# BUILD PROVIDER + CURRENT GAME PRODUCTS
# ============================================================

section "BUILD PROVIDER CORE"

info "Building ./cmd/server ..."

if ! go build -p=1 -o "$SERVER_BIN" ./cmd/server >"$BUILD_LOG" 2>&1; then
    cat "$BUILD_LOG" >&2
    die "Provider build failed."
fi

chmod +x "$SERVER_BIN"

success "Provider binary ready: $SERVER_BIN"

# ============================================================
# OPTIONAL MOCK WALLET
# ============================================================

if [[ "$START_MOCKWALLET" == "1" ]]; then

    section "BUILD MOCK WALLET"

    info "Building ./cmd/mockwallet ..."

    if ! go build -p=1 -o "$MOCKWALLET_BIN" ./cmd/mockwallet >"$MOCK_BUILD_LOG" 2>&1; then
        cat "$MOCK_BUILD_LOG" >&2
        die "MockWallet build failed."
    fi

    chmod +x "$MOCKWALLET_BIN"

    success "MockWallet binary ready: $MOCKWALLET_BIN"

else

    info "MockWallet disabled."
    info "Use: START_MOCKWALLET=1 ./start.sh"

fi

# ============================================================
# START PROVIDER
# ============================================================

section "START PROVIDER CORE"

info "HTTP    : $HOST:$HTTP_PORT"
info "WebSocket: $HOST:$WS_PORT"
info "Verify  : $HOST:$VERIFY_PORT"

nohup "$SERVER_BIN" \
    >>"$PROVIDER_LOG" 2>&1 &

PROVIDER_PID="$!"

printf '%s\n' "$PROVIDER_PID" >"$PROVIDER_PID_FILE"

sleep 1

if ! pid_matches_binary "$PROVIDER_PID" "$SERVER_BIN"; then
    warn "Provider process exited during startup."

    tail -n 80 "$PROVIDER_LOG" >&2 || true

    rm -f "$PROVIDER_PID_FILE"

    die "Provider failed to start."
fi

success "Provider started — PID $PROVIDER_PID"

# ============================================================
# HEALTH CHECKS
# ============================================================

section "HEALTH CHECKS"

info "Checking HTTP health..."

if wait_for_http "http://${HOST}:${HTTP_PORT}/healthz" 30; then
    success "HTTP /healthz ............. OK"
else
    tail -n 80 "$PROVIDER_LOG" >&2 || true
    die "HTTP health check failed."
fi

info "Checking verification endpoint..."

if wait_for_http "http://${HOST}:${VERIFY_PORT}/verify/ping" 30; then
    success "Verify /verify/ping ........ OK"
else
    tail -n 80 "$PROVIDER_LOG" >&2 || true
    die "Verify health check failed."
fi

info "Checking WebSocket port..."

if port_reachable "$HOST" "$WS_PORT"; then
    success "WebSocket :$WS_PORT ............ REACHABLE"
else
    warn "WebSocket port could not be verified with available tools."
fi

# ============================================================
# MOCK WALLET START
# ============================================================

if [[ "$START_MOCKWALLET" == "1" ]]; then

    section "START MOCK WALLET"

    nohup "$MOCKWALLET_BIN" \
        >>"$MOCKWALLET_LOG" 2>&1 &

    MOCK_PID="$!"

    printf '%s\n' "$MOCK_PID" >"$MOCKWALLET_PID_FILE"

    sleep 1

    if ! pid_matches_binary "$MOCK_PID" "$MOCKWALLET_BIN"; then
        warn "MockWallet failed to start."
        tail -n 80 "$MOCKWALLET_LOG" >&2 || true
        rm -f "$MOCKWALLET_PID_FILE"
        die "MockWallet startup failed."
    fi

    success "MockWallet started — PID $MOCK_PID"

    if wait_for_http \
        "http://${HOST}:${MOCKWALLET_PORT}/wallet/balance?operatorId=default&playerId=demo" \
        20; then

        success "MockWallet :$MOCKWALLET_PORT ........ OK"

    else

        warn "MockWallet HTTP check failed."
        warn "Provider remains running; inspect $MOCKWALLET_LOG"

    fi

fi

# ============================================================
# FINAL STATUS
# ============================================================

printf "\n"
printf "${GREEN}${BOLD}╔══════════════════════════════════════════════════════╗${RESET}\n"
printf "${GREEN}${BOLD}║              223GAMING STUDIO ONLINE                ║${RESET}\n"
printf "${GREEN}${BOLD}╠══════════════════════════════════════════════════════╣${RESET}\n"
printf "${GREEN}║${RESET} Provider Core      : ${GREEN}ONLINE${RESET}                     ${GREEN}║${RESET}\n"
printf "${GREEN}║${RESET} 223CRASH           : ${GREEN}ACTIVE${RESET}                     ${GREEN}║${RESET}\n"
printf "${GREEN}║${RESET} HTTP               : ${WHITE}:%-4s${RESET}                     ${GREEN}║${RESET}\n" "$HTTP_PORT"
printf "${GREEN}║${RESET} WebSocket          : ${WHITE}:%-4s${RESET}                     ${GREEN}║${RESET}\n" "$WS_PORT"
printf "${GREEN}║${RESET} Verify             : ${WHITE}:%-4s${RESET}                     ${GREEN}║${RESET}\n" "$VERIFY_PORT"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    printf "${GREEN}║${RESET} MockWallet         : ${YELLOW}DEV :%-4s${RESET}                  ${GREEN}║${RESET}\n" "$MOCKWALLET_PORT"
else
    printf "${GREEN}║${RESET} MockWallet         : ${WHITE}DISABLED${RESET}                  ${GREEN}║${RESET}\n"
fi

printf "${GREEN}║${RESET} Provider PID       : ${WHITE}%-29s${RESET}${GREEN}║${RESET}\n" "$PROVIDER_PID"
printf "${GREEN}╚══════════════════════════════════════════════════════╝${RESET}\n"

printf "\n"
info "Logs:"
printf "  Provider : %s\n" "$PROVIDER_LOG"
printf "  Build    : %s\n" "$BUILD_LOG"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    printf "  Mock     : %s\n" "$MOCKWALLET_LOG"
fi

printf "\n"
info "Stop Studio: ./stop.sh"
printf "\n"
