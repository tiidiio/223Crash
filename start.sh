#!/data/data/com.termux/files/usr/bin/bash

set -Eeuo pipefail

# ============================================================
# 223GAMING STUDIO • STARTUP ORCHESTRATOR
# Provider Core + 223CRASH + MockWallet
# Intelligent / Idempotent Startup
# ============================================================

PROJECT_DIR="${HOME}/223Gaming/223Crash"
cd "$PROJECT_DIR" || exit 1

# COLORS
if [[ -t 1 ]]; then
    RESET=$'\033[0m'
    BOLD=$'\033[1m'
    RED=$'\033[1;91m'
    GREEN=$'\033[1;92m'
    YELLOW=$'\033[1;93m'
    BLUE=$'\033[1;94m'
    MAGENTA=$'\033[1;95m'
    CYAN=$'\033[1;96m'
    WHITE=$'\033[1;97m'
else
    RESET="" BOLD="" RED="" GREEN=""
    YELLOW="" BLUE="" MAGENTA="" CYAN="" WHITE=""
fi

# ENVIRONMENT
if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a
    source "$PROJECT_DIR/.env"
    set +a
fi

HOST="${HOST:-127.0.0.1}"
HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"
MOCKWALLET_PORT="${MOCKWALLET_PORT:-9000}"
START_MOCKWALLET="${START_MOCKWALLET:-1}"

export GOFLAGS="${GOFLAGS:--p=1}"
export GOGC="${GOGC:-50}"
export CGO_ENABLED="${CGO_ENABLED:-0}"

# DIRECTORIES
BIN_DIR="$PROJECT_DIR/bin"
LOG_DIR="$PROJECT_DIR/logs"
RUNTIME_DIR="$PROJECT_DIR/.runtime"
PID_DIR="$RUNTIME_DIR/pids"

mkdir -p "$BIN_DIR" "$LOG_DIR" "$PID_DIR"

# BINARIES
SERVER_BIN="$BIN_DIR/server"
MOCKWALLET_BIN="$BIN_DIR/mockwallet"

# PID FILES
PROVIDER_PID_FILE="$PID_DIR/provider-server.pid"
MOCKWALLET_PID_FILE="$PID_DIR/mockwallet.pid"

# LEGACY PID FILES
LEGACY_PROVIDER_PID="$LOG_DIR/go.pid"
LEGACY_MOCK_PID="$LOG_DIR/mockwallet.pid"

# LOG FILES
PROVIDER_LOG="$LOG_DIR/provider-server.log"
MOCKWALLET_LOG="$LOG_DIR/mockwallet.log"
BUILD_LOG="$LOG_DIR/build.log"
MOCK_BUILD_LOG="$LOG_DIR/build-mockwallet.log"

# FUNCTIONS
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
    [[ "$1" =~ ^[0-9]+$ ]] && kill -0 "$1" 2>/dev/null
}

pid_matches_binary() {
    local pid="$1"
    local expected="$2"
    local exe

    pid_alive "$pid" || return 1

    exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
    exe="${exe% (deleted)}"

    [[ "$exe" == "$expected" ]]
}

# Find a process by its actual executable, not only PID files.
find_process() {
    local expected="$1"
    local proc_dir pid exe

    for proc_dir in /proc/[0-9]*; do
        [[ -d "$proc_dir" ]] || continue

        pid="${proc_dir##*/}"
        [[ "$pid" == "$$" || "$pid" == "$PPID" ]] && continue

        exe="$(readlink "$proc_dir/exe" 2>/dev/null || true)"
        exe="${exe% (deleted)}"

        if [[ "$exe" == "$expected" ]]; then
            printf '%s\n' "$pid"
            return 0
        fi
    done

    return 1
}

read_pid_file() {
    local file="$1"
    [[ -f "$file" ]] || return 1

    local pid
    pid="$(cat "$file" 2>/dev/null || true)"

    [[ "$pid" =~ ^[0-9]+$ ]] || return 1
    printf '%s\n' "$pid"
}

save_provider_pid() {
    local pid="$1"
    [[ "$pid" =~ ^[0-9]+$ ]] || return 0

    printf '%s\n' "$pid" > "$PROVIDER_PID_FILE"
    printf '%s\n' "$pid" > "$LEGACY_PROVIDER_PID"
}

save_mock_pid() {
    local pid="$1"
    [[ "$pid" =~ ^[0-9]+$ ]] || return 0

    printf '%s\n' "$pid" > "$MOCKWALLET_PID_FILE"
    printf '%s\n' "$pid" > "$LEGACY_MOCK_PID"
}

wait_for_http() {
    local url="$1"
    local attempts="${2:-20}"
    local i

    for ((i=1; i<=attempts; i++)); do
        if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
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
        nc -z -w 2 "$host" "$port" >/dev/null 2>&1
        return $?
    fi

    curl -sS -o /dev/null --max-time 2 \
        "http://${host}:${port}/" >/dev/null 2>&1
}

build_server() {
    section "BUILD PROVIDER CORE"

    info "Compiling ./cmd/server ..."

    if ! go build -p=1 -o "$SERVER_BIN" ./cmd/server \
        >"$BUILD_LOG" 2>&1; then
        cat "$BUILD_LOG" >&2
        die "Provider Core compilation failed."
    fi

    chmod +x "$SERVER_BIN"
    success "Provider binary compiled."
}

build_mockwallet() {
    section "BUILD MOCKWALLET"

    info "Compiling ./cmd/mockwallet ..."

    if ! go build -p=1 -o "$MOCKWALLET_BIN" ./cmd/mockwallet \
        >"$MOCK_BUILD_LOG" 2>&1; then
        cat "$MOCK_BUILD_LOG" >&2
        die "MockWallet compilation failed."
    fi

    chmod +x "$MOCKWALLET_BIN"
    success "MockWallet binary compiled."
}

# ============================================================
# HEADER
# ============================================================

clear 2>/dev/null || true

BOOT_TIME="$(date '+%Y-%m-%d %H:%M:%S')"
GO_VERSION="$(go version 2>/dev/null | awk '{print $3}')"

printf "\n"
printf "${CYAN}╔══════════════════════════════════════════════════════╗${RESET}\n"
printf "${CYAN}║ ${WHITE}${BOLD}223GAMING STUDIO${RESET}${CYAN} • ${MAGENTA}LAB iGAMING B2B${CYAN}             ║${RESET}\n"
printf "${CYAN}║ ${WHITE}Provider Core + Game Products${CYAN}                    ║${RESET}\n"
printf "${CYAN}╠══════════════════════════════════════════════════════╣${RESET}\n"
printf "${CYAN}║ ${YELLOW}${BOLD}FOUNDER & CEO${RESET}${CYAN}                                  ║${RESET}\n"
printf "${CYAN}║ ${WHITE}Tidiane Diallo${CYAN}                                  ║${RESET}\n"
printf "${CYAN}║ ${WHITE}Engine : Go${CYAN}                                      ║${RESET}\n"
printf "${CYAN}║ ${WHITE}Architecture : Provider B2B${CYAN}                     ║${RESET}\n"
printf "${CYAN}║ ${WHITE}Boot : %-41s${CYAN} ║${RESET}\n" "$BOOT_TIME"
printf "${CYAN}╚══════════════════════════════════════════════════════╝${RESET}\n"

# ============================================================
# PRECHECK
# ============================================================

section "PRECHECK"

[[ -d "$PROJECT_DIR" ]] || die "Project directory missing."
command -v go >/dev/null 2>&1 || die "Go unavailable."
command -v curl >/dev/null 2>&1 || die "curl unavailable."

success "Project : $PROJECT_DIR"
success "Go      : $GO_VERSION"

# ============================================================
# PROVIDER CORE - INTELLIGENT START
# ============================================================

section "PROVIDER CORE"

PROVIDER_PID=""
PROVIDER_HEALTH=""

if wait_for_http "http://${HOST}:${HTTP_PORT}/healthz" 2; then

    PROVIDER_HEALTH="$(curl -fsS --max-time 2 \
        "http://${HOST}:${HTTP_PORT}/healthz" 2>/dev/null || true)"

    PROVIDER_PID="$(find_process "$SERVER_BIN" || true)"

    if [[ -z "$PROVIDER_PID" ]]; then
        PROVIDER_PID="$(read_pid_file "$PROVIDER_PID_FILE" || true)"
        if ! pid_matches_binary "$PROVIDER_PID" "$SERVER_BIN"; then
            PROVIDER_PID=""
        fi
    fi

    if [[ -n "$PROVIDER_PID" ]]; then
        save_provider_pid "$PROVIDER_PID"
        success "Existing Provider preserved — PID $PROVIDER_PID"
    else
        warn "HTTP is healthy but process ownership could not be established."
        warn "Existing service will not be restarted."
    fi

    success "HTTP health already OK."

else

    PROVIDER_PID="$(find_process "$SERVER_BIN" || true)"

    if [[ -n "$PROVIDER_PID" ]]; then
        save_provider_pid "$PROVIDER_PID"
        die "Provider PID $PROVIDER_PID exists but HTTP health is failing. No duplicate started."
    fi

    if port_reachable "$HOST" "$HTTP_PORT"; then
        die "Port $HTTP_PORT is occupied by an unidentified process."
    fi

    build_server

    section "START PROVIDER"

    info "Starting Provider Core..."

    nohup "$SERVER_BIN" >>"$PROVIDER_LOG" 2>&1 &
    PROVIDER_PID="$!"

    save_provider_pid "$PROVIDER_PID"

    sleep 1

    if ! pid_matches_binary "$PROVIDER_PID" "$SERVER_BIN"; then
        tail -n 80 "$PROVIDER_LOG" >&2 || true
        die "Provider process failed to start."
    fi

    success "Provider started — PID $PROVIDER_PID"

    if ! wait_for_http "http://${HOST}:${HTTP_PORT}/healthz" 30; then
        tail -n 80 "$PROVIDER_LOG" >&2 || true
        die "Provider HTTP health check failed."
    fi

    success "Provider HTTP health OK."
fi

# ============================================================
# PROVIDER ENDPOINT CHECKS
# ============================================================

section "PROVIDER HEALTH CHECKS"

HTTP_OK=0
VERIFY_OK=0
WS_OK=0

HEALTH_JSON="$(curl -fsS --max-time 3 \
    "http://${HOST}:${HTTP_PORT}/healthz" 2>/dev/null || true)"

if [[ -n "$HEALTH_JSON" ]]; then
    HTTP_OK=1
    success "HTTP :$HTTP_PORT — $HEALTH_JSON"
else
    warn "HTTP health unavailable."
fi

if wait_for_http "http://${HOST}:${VERIFY_PORT}/verify/ping" 4; then
    VERIFY_OK=1
    success "Verify :$VERIFY_PORT — OK"
else
    warn "Verify endpoint unavailable."
fi

if port_reachable "$HOST" "$WS_PORT"; then
    WS_OK=1
    success "WebSocket :$WS_PORT — REACHABLE"
else
    warn "WebSocket :$WS_PORT could not be verified."
fi

# ============================================================
# MOCKWALLET - INTELLIGENT START
# ============================================================

MOCK_PID=""
MOCK_OK=0

if [[ "$START_MOCKWALLET" == "1" ]]; then

    section "MOCKWALLET"

    MOCK_URL="http://${HOST}:${MOCKWALLET_PORT}/wallet/balance?operatorId=default&playerId=probe"

    if wait_for_http "$MOCK_URL" 2; then

        MOCK_PID="$(find_process "$MOCKWALLET_BIN" || true)"

        if [[ -z "$MOCK_PID" ]]; then
            MOCK_PID="$(read_pid_file "$MOCKWALLET_PID_FILE" || true)"
            if ! pid_matches_binary "$MOCK_PID" "$MOCKWALLET_BIN"; then
                MOCK_PID=""
            fi
        fi

        if [[ -n "$MOCK_PID" ]]; then
            save_mock_pid "$MOCK_PID"
            success "Existing MockWallet preserved — PID $MOCK_PID"
        else
            warn "MockWallet endpoint is healthy; ownership unknown."
        fi

        MOCK_OK=1
        success "MockWallet endpoint OK."

    else

        MOCK_PID="$(find_process "$MOCKWALLET_BIN" || true)"

        if [[ -n "$MOCK_PID" ]]; then
            save_mock_pid "$MOCK_PID"
            die "MockWallet PID $MOCK_PID exists but health check fails."
        fi

        if port_reachable "$HOST" "$MOCKWALLET_PORT"; then
            die "MockWallet port $MOCKWALLET_PORT occupied by unidentified process."
        fi

        build_mockwallet

        section "START MOCKWALLET"

        nohup "$MOCKWALLET_BIN" >>"$MOCKWALLET_LOG" 2>&1 &
        MOCK_PID="$!"

        save_mock_pid "$MOCK_PID"

        sleep 1

        if ! pid_matches_binary "$MOCK_PID" "$MOCKWALLET_BIN"; then
            tail -n 80 "$MOCKWALLET_LOG" >&2 || true
            die "MockWallet failed to start."
        fi

        if ! wait_for_http "$MOCK_URL" 20; then
            tail -n 80 "$MOCKWALLET_LOG" >&2 || true
            die "MockWallet health check failed."
        fi

        MOCK_OK=1
        success "MockWallet started — PID $MOCK_PID"
    fi

else
    warn "MockWallet disabled by START_MOCKWALLET=0."
fi

# ============================================================
# FINAL REPORT
# ============================================================

section "FINAL SYSTEM STATUS"

[[ "$HTTP_OK" -eq 1 ]] && success "Provider HTTP :$HTTP_PORT ONLINE" \
    || warn "Provider HTTP OFFLINE"

[[ "$VERIFY_OK" -eq 1 ]] && success "Verify :$VERIFY_PORT ONLINE" \
    || warn "Verify OFFLINE"

[[ "$WS_OK" -eq 1 ]] && success "WebSocket :$WS_PORT REACHABLE" \
    || warn "WebSocket UNVERIFIED"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    [[ "$MOCK_OK" -eq 1 ]] && success "MockWallet :$MOCKWALLET_PORT ONLINE" \
        || warn "MockWallet OFFLINE"
fi

if echo "$HEALTH_JSON" | grep -q '"mongo":"connected"'; then
    success "MongoDB Atlas CONNECTED"
else
    warn "MongoDB Atlas not confirmed connected."
fi

printf "\n"
printf "${GREEN}${BOLD}╔══════════════════════════════════════════════════════╗${RESET}\n"
printf "${GREEN}${BOLD}║              223GAMING STUDIO STATUS                ║${RESET}\n"
printf "${GREEN}${BOLD}╠══════════════════════════════════════════════════════╣${RESET}\n"

printf "${GREEN}║${RESET} Provider Core : ${GREEN}%-33s${RESET}${GREEN}║${RESET}\n" "$([[ "$HTTP_OK" -eq 1 ]] && echo ONLINE || echo DEGRADED)"
printf "${GREEN}║${RESET} 223CRASH      : ${GREEN}%-33s${RESET}${GREEN}║${RESET}\n" "$([[ "$HTTP_OK" -eq 1 ]] && echo ACTIVE || echo DEGRADED)"
printf "${GREEN}║${RESET} HTTP          : ${WHITE}:%-4s${RESET}                            ${GREEN}║${RESET}\n" "$HTTP_PORT"
printf "${GREEN}║${RESET} WebSocket     : ${WHITE}:%-4s${RESET}                            ${GREEN}║${RESET}\n" "$WS_PORT"
printf "${GREEN}║${RESET} Verify        : ${WHITE}:%-4s${RESET}                            ${GREEN}║${RESET}\n" "$VERIFY_PORT"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    printf "${GREEN}║${RESET} MockWallet    : ${WHITE}:%-4s${RESET}                            ${GREEN}║${RESET}\n" "$MOCKWALLET_PORT"
fi

printf "${GREEN}║${RESET} Provider PID  : ${WHITE}%-33s${RESET}${GREEN}║${RESET}\n" "${PROVIDER_PID:-EXTERNAL/UNKNOWN}"
printf "${GREEN}╚══════════════════════════════════════════════════════╝${RESET}\n"

printf "\n"
info "Logs:"
printf "  Provider : $PROVIDER_LOG\n"
printf "  Build    : $BUILD_LOG\n"

if [[ "$START_MOCKWALLET" == "1" ]]; then
    printf "  Mock     : $MOCKWALLET_LOG\n"
fi

printf "\n"
info "Stop Studio: ./stop.sh"

# Critical endpoint validation
if [[ "$HTTP_OK" -ne 1 || "$VERIFY_OK" -ne 1 ]]; then
    exit 1
fi

if [[ "$START_MOCKWALLET" == "1" && "$MOCK_OK" -ne 1 ]]; then
    exit 1
fi

exit 0
