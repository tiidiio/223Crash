#!/data/data/com.termux/files/usr/bin/bash

export LC_ALL=C.UTF-8
export LANG=C.UTF-8

PROJECT_DIR="$HOME/223Gaming/223Crash"
cd "$PROJECT_DIR" || exit 1

HOST="127.0.0.1"
HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"

SERVER_BIN="$PROJECT_DIR/bin/server"
LOG_DIR="$PROJECT_DIR/logs"
SERVER_LOG="$LOG_DIR/server.log"
BUILD_LOG="$LOG_DIR/build.log"
PID_FILE="$LOG_DIR/go.pid"

mkdir -p "$PROJECT_DIR/bin" "$LOG_DIR"

# ============================================================
# ENV
# ============================================================

if [ -f "$PROJECT_DIR/.env" ]; then
    set -a
    . "$PROJECT_DIR/.env"
    set +a
fi

HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"

# ============================================================
# THÈME 223CRASH
# ============================================================

R='\033[0m'
B='\033[1m'

# Structure
CY='\033[1;96m'
BL='\033[1;94m'
WH='\033[1;97m'

# États
GN='\033[1;92m'
GO='\033[1;93m'
RD='\033[1;91m'

# Accent
MG='\033[1;95m'

# Texte secondaire
GR='\033[90m'

# ============================================================
# OUTILS
# ============================================================

if ! command -v go >/dev/null 2>&1; then
    echo "Go absent."
    exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
    echo "curl absent."
    exit 1
fi

if [ -z "${MONGO_URI:-}" ]; then
    echo "MONGO_URI manquant."
    exit 1
fi

# ============================================================
# ARRÊT ANCIEN PROCESSUS (graceful, avec poll — pas de sleep fixe)
# ============================================================

SHUTDOWN_GRACE_SECS="${SHUTDOWN_GRACE_SECS:-5}"

if [ -f "$PID_FILE" ]; then
    OLD_PID="$(cat "$PID_FILE" 2>/dev/null)"

    if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
        kill "$OLD_PID" 2>/dev/null

        i=0
        STEPS=$((SHUTDOWN_GRACE_SECS * 10))
        while [ "$i" -lt "$STEPS" ]; do
            if ! kill -0 "$OLD_PID" 2>/dev/null; then
                break
            fi
            sleep 0.1
            i=$((i + 1))
        done

        if kill -0 "$OLD_PID" 2>/dev/null; then
            echo "AVERTISSEMENT: PID $OLD_PID n'a pas terminé proprement en ${SHUTDOWN_GRACE_SECS}s — SIGKILL forcé."
            echo "Vérifie s'il y avait un round en cours (voir logs/server.log)."
            kill -9 "$OLD_PID" 2>/dev/null
        fi
    fi

    rm -f "$PID_FILE"
fi

# ============================================================
# DATE / VERSION
# ============================================================

BOOT_TIME="$(date '+%Y-%m-%d %H:%M:%S')"
GO_VERSION="$(go version | awk '{print $3}')"

# ============================================================
# HEADER
# ============================================================

clear

printf "${CY}╔══════════════════════════════════════════════╗${R}\n"
printf "${CY}║ ${WH}${B}223GAMING STUDIO${R}${CY} • ${MG}LAB iGAMING B2B${CY}       ║${R}\n"
printf "${CY}║ ${WH}Produit : ${GO}223CRASH v2.2.0.56${WH} [${GN}STABLE B2B${WH}] ${CY}║${R}\n"
printf "${CY}╠══════════════════════════════════════════════╣${R}\n"
printf "${CY}║ ${GO}${B}FONDATEUR & PDG${R}${CY}                             ║${R}\n"
printf "${CY}║ ${WH}👤 Tidiane Diallo${CY}                           ║${R}\n"
printf "${CY}║ ${WH}🎓 Architecte Full-Stack Senior${CY}             ║${R}\n"
printf "${CY}║ ${WH}⚙️  Go • MongoDB • ${GO}B2B${CY}                      ║${R}\n"
printf "${CY}║ ${WH}🌍 Mali • Bamako${CY}                            ║${R}\n"
printf "${CY}║ ${WH}🕒 Boot : ${GO}%-31s${CY} ║${R}\n" "$BOOT_TIME"
printf "${CY}╠══════════════════════════════════════════════╣${R}\n"
printf "${CY}║ ${BL}🚀 MOTEUR${CY} : ${WH}%-32s ${CY}║${R}\n" "$GO_VERSION"
printf "${CY}║ ${BL}🎮 JEU${CY}    : ${WH}Crash x5000 • Temps Réel B2B${CY}    ║${R}\n"
printf "${CY}║ ${BL}🔐 ÉQUITÉ${CY} : ${WH}SHA256 HMAC • ${GN}Provably Fair${WH}     ${CY}║${R}\n"
printf "${CY}╚══════════════════════════════════════════════╝${R}\n"

# ============================================================
# [1/4] COMPILATION
# ============================================================

printf "\n${CY}╔══════════════════════════════════════════════╗${R}\n"
printf "${CY}║ ${GO}${B}[1/4]${R}${CY} Préparation + Compilation Go          ║${R}\n"

printf "${CY}║   ${GN}✓${R}${WH} MONGO_URI présent${CY}                      ║${R}\n"
printf "${CY}║   ${GN}✓${R}${WH} MONGO_DB=${MONGO_DB:-223crash}${CY}                    ║${R}\n"

GOFLAGS="-p=1" \
GOGC=50 \
CGO_ENABLED=0 \
go build \
    -o "$SERVER_BIN" \
    ./cmd/server \
    >"$BUILD_LOG" 2>&1

if [ $? -ne 0 ]; then
    printf "${CY}║   ${RD}✗ COMPILATION ÉCHOUÉE${CY}                   ║${R}\n"
    printf "${CY}║   ${RD}Voir logs/build.log${CY}                     ║${R}\n"
    printf "${CY}╚══════════════════════════════════════════════╝${R}\n"
    exit 1
fi

printf "${CY}║   ${GN}✓ Go binaire • COMPILÉ${CY}                   ║${R}\n"
printf "${CY}╚══════════════════════════════════════════════╝${R}\n"

# ============================================================
# [2/4] DÉMARRAGE
# ============================================================

printf "\n${CY}╔══════════════════════════════════════════════╗${R}\n"
printf "${CY}║ ${GO}${B}[2/4]${R}${CY} Démarrage du processus               ║${R}\n"

nohup "$SERVER_BIN" >"$SERVER_LOG" 2>&1 &
PID=$!

echo "$PID" >"$PID_FILE"

sleep 0.5

if kill -0 "$PID" 2>/dev/null; then
    printf "${CY}║   ${GN}✓${R}${WH} Processus • PID ${GO}%-18s${CY}║${R}\n" "$PID"
else
    printf "${CY}║   ${RD}✗ Processus arrêté${CY}                      ║${R}\n"
    printf "${CY}╚══════════════════════════════════════════════╝${R}\n"
    exit 1
fi

printf "${CY}╚══════════════════════════════════════════════╝${R}\n"

# ============================================================
# [3/4] HTTP + MONGODB
# ============================================================

printf "\n${CY}╔══════════════════════════════════════════════╗${R}\n"
printf "${CY}║ ${GO}${B}[3/4]${R}${CY} Vérification HTTP + MongoDB         ║${R}\n"
printf "${CY}║   ${BL}Adresse${CY} : ${WH}${HOST}${CY}                         ║${R}\n"
printf "${CY}║   ${BL}Health${CY}  : ${WH}/healthz${CY}                        ║${R}\n"

HC_OK=0
HC_CODE="000"
HC_JSON=""

for i in $(seq 1 24); do

    RAW="$(
        curl \
            -sS \
            --connect-timeout 1 \
            --max-time 2 \
            -w '\n%{http_code}' \
            "http://${HOST}:${HTTP_PORT}/healthz" \
            2>/dev/null
    )"

    HC_CODE="${RAW##*$'\n'}"
    HC_JSON="${RAW%$'\n'*}"

    if [ "$HC_CODE" = "200" ]; then
        HC_OK=1
        break
    fi

    if ! kill -0 "$PID" 2>/dev/null; then
        break
    fi

    sleep 0.5
done

APP_STATE="unknown"
MONGO_STATE="unknown"

if [ "$HC_OK" -eq 1 ]; then

    if command -v jq >/dev/null 2>&1; then
        APP_STATE="$(printf '%s' "$HC_JSON" | jq -r '.status // "unknown"' 2>/dev/null)"
        MONGO_STATE="$(printf '%s' "$HC_JSON" | jq -r '.mongo // "unknown"' 2>/dev/null)"
    else
        APP_STATE="$(printf '%s' "$HC_JSON" |
            sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"

        MONGO_STATE="$(printf '%s' "$HC_JSON" |
            sed -n 's/.*"mongo":"\([^"]*\)".*/\1/p')"
    fi
fi

if [ "$HC_OK" -eq 1 ] && [ "$APP_STATE" = "ok" ]; then
    printf "${CY}║   ${GN}✓${R}${WH} HTTP %s • OK (${GN}%s${WH})${CY}                 ║${R}\n" "$HTTP_PORT" "$HC_CODE"
else
    printf "${CY}║   ${RD}✗${R}${WH} HTTP %s • DOWN${CY}                     ║${R}\n" "$HTTP_PORT"
fi

if [ "$MONGO_STATE" = "connected" ]; then
    printf "${CY}║   ${GN}✓${R}${WH} MongoDB • ${GN}CONNECTÉ${WH}                   ║${R}\n"
else
    printf "${CY}║   ${RD}✗${R}${WH} MongoDB • ${RD}DOWN${WH}                       ║${R}\n"
fi

printf "${CY}╚══════════════════════════════════════════════╝${R}\n"

# ============================================================
# [4/4] SERVICES RÉSEAU
# ============================================================

printf "\n${CY}╔══════════════════════════════════════════════╗${R}\n"
printf "${CY}║ ${GO}${B}[4/4]${R}${CY} Services réseau                     ║${R}\n"

WS_CODE="$(
    curl \
        -sS \
        -o /dev/null \
        -w '%{http_code}' \
        --connect-timeout 1 \
        --max-time 2 \
        "http://${HOST}:${WS_PORT}/" \
        2>/dev/null
)"

if [ -n "$WS_CODE" ] && [ "$WS_CODE" != "000" ]; then
    printf "${CY}║   ${GN}✓${R}${WH} WS %s • EN ÉCOUTE (${GN}%s${WH})${CY}              ║${R}\n" "$WS_PORT" "$WS_CODE"
    WS_OK=1
else
    printf "${CY}║   ${RD}✗${R}${WH} WS %s • ${RD}DOWN${WH}                        ║${R}\n" "$WS_PORT"
    WS_OK=0
fi

VERIFY_CODE="$(
    curl \
        -sS \
        -o /dev/null \
        -w '%{http_code}' \
        --connect-timeout 1 \
        --max-time 2 \
        "http://${HOST}:${VERIFY_PORT}/verify/ping" \
        2>/dev/null
)"

if [ "$VERIFY_CODE" = "200" ]; then
    printf "${CY}║   ${GN}✓${R}${WH} Vérif. %s • ${GN}RÉPOND${WH} (${GN}%s${WH})${CY}            ║${R}\n" "$VERIFY_PORT" "$VERIFY_CODE"
    VERIFY_OK=1
else
    printf "${CY}║   ${RD}✗${R}${WH} Vérif. %s • ${RD}DOWN${WH}                     ║${R}\n" "$VERIFY_PORT"
    VERIFY_OK=0
fi

printf "${CY}╚══════════════════════════════════════════════╝${R}\n"

# ============================================================
# VERDICT FINAL
# ============================================================

if [ "$HC_OK" -eq 1 ] &&
   [ "$APP_STATE" = "ok" ] &&
   [ "$MONGO_STATE" = "connected" ] &&
   [ "$WS_OK" -eq 1 ] &&
   [ "$VERIFY_OK" -eq 1 ]; then

    printf "\n${GN}┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓${R}\n"
    printf "${GN}┃ ${WH}MOTEUR EN LIGNE • PID ${GO}%-10s${GN} ┃${R}\n" "$PID"
    printf "${GN}┃ ${WH}HTTP ${GN}OK${WH} • MONGO ${GN}OK${WH} • WS ${GN}OK${WH}          ${GN}┃${R}\n"
    printf "${GN}┃ ${WH}VERIFY ${GN}OK${WH} • COMPILATION ${GN}OK${WH}          ${GN}┃${R}\n"
    printf "${GN}┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛${R}\n"

    exit 0
else

    printf "\n${GO}┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓${R}\n"
    printf "${GO}┃ ${WH}MOTEUR • VÉRIFICATION REQUISE        ${GO}┃${R}\n"
    printf "${GO}┃ ${WH}Consulte logs/server.log              ${GO}┃${R}\n"
    printf "${GO}┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛${R}\n"

    exit 1
fi
