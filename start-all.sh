#!/data/data/com.termux/files/usr/bin/bash
set -uo pipefail
cd "$(dirname "$0")"

R='\e[0m'; G='\e[1;92m'; Y='\e[1;93m'; Rd='\e[1;91m'; W='\e[1;97m'

echo -e "${W}=== 223CRASH — démarrage complet ===${R}"

mkdir -p bin logs
[ -f .env ] && set -a && source .env && set +a

HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"
MOCKWALLET_PORT="${MOCKWALLET_PORT:-9000}"

echo -e "${W}[0/5] Vérification réseau (DNS)...${R}"
if command -v getent >/dev/null 2>&1; then
  RESOLVED=$(getent hosts cluster0.38tbpuz.mongodb.net 2>/dev/null)
else
  RESOLVED=$(python3 -c "import socket; print(socket.gethostbyname('cluster0.38tbpuz.mongodb.net'))" 2>/dev/null)
fi
if [ -z "$RESOLVED" ]; then
  echo -e "${Rd}✘ DNS injoignable (cluster0.38tbpuz.mongodb.net) — vérifie le WiFi/données mobiles du device.${R}"
  echo -e "${Rd}  Le serveur démarrera quand même mais Mongo restera down tant que le réseau ne revient pas.${R}"
else
  echo -e "${G}✔ DNS résout correctement${R}"
fi

echo -e "${W}[1/5] Nettoyage des processus existants...${R}"
pkill -f './bin/server' 2>/dev/null
pkill -f './bin/mockwallet' 2>/dev/null
sleep 0.5

echo -e "${W}[2/5] Compilation...${R}"
GOFLAGS="-p=1" GOGC=50 CGO_ENABLED=0 go build -o bin/mockwallet ./cmd/mockwallet 2>logs/build-mockwallet.log
if [ $? -ne 0 ]; then
  echo -e "${Rd}✘ Échec compilation mockwallet — voir logs/build-mockwallet.log${R}"
  tail -n 20 logs/build-mockwallet.log
  exit 1
fi
GOFLAGS="-p=1" GOGC=50 CGO_ENABLED=0 go build -o bin/server ./cmd/server 2>logs/build.log
if [ $? -ne 0 ]; then
  echo -e "${Rd}✘ Échec compilation server — voir logs/build.log${R}"
  tail -n 20 logs/build.log
  exit 1
fi
echo -e "${G}✔ Compilation OK (mockwallet + server)${R}"

echo -e "${W}[3/5] Démarrage mockwallet...${R}"
nohup ./bin/mockwallet >logs/mockwallet.log 2>&1 &
echo $! >logs/mockwallet.pid
sleep 0.5
if ! kill -0 "$(cat logs/mockwallet.pid)" 2>/dev/null; then
  echo -e "${Rd}✘ mockwallet arrêté immédiatement — voir logs/mockwallet.log${R}"
  tail -n 20 logs/mockwallet.log
  exit 1
fi
MW_OK=0
for i in 1 2 3 4; do
  curl -sf -m 1 "http://localhost:${MOCKWALLET_PORT}/wallet/balance?operatorId=default&playerId=probe" >/dev/null 2>&1 && { MW_OK=1; break; }
  sleep 0.5
done
if [ "$MW_OK" -eq 1 ]; then
  echo -e "${G}✔ mockwallet répond sur :${MOCKWALLET_PORT}${R}"
else
  echo -e "${Rd}✘ mockwallet ne répond pas sur :${MOCKWALLET_PORT}${R}"
fi

echo -e "${W}[4/5] Démarrage server principal...${R}"
nohup ./bin/server >logs/server.log 2>&1 &
echo $! >logs/go.pid
PID=$(cat logs/go.pid)
sleep 0.8
if ! kill -0 "$PID" 2>/dev/null; then
  echo -e "${Rd}✘ server arrêté immédiatement (PID $PID) — voir logs/server.log${R}"
  tail -n 20 logs/server.log
  exit 1
fi
echo -e "${G}✔ server lancé, PID $PID${R}"

echo -e "${W}[5/5] Vérification santé (jusqu'à 8s)...${R}"
HC_OK=0
for i in 1 2 3 4 5 6 7 8; do
  HC_JSON=$(curl -s -m 2 "http://localhost:${HTTP_PORT}/healthz" 2>/dev/null)
  [ -n "$HC_JSON" ] && { HC_OK=1; break; }
  sleep 1
done

if [ "$HC_OK" -eq 1 ]; then
  echo -e "${G}✔ HTTP ${HTTP_PORT} répond: ${HC_JSON}${R}"
else
  echo -e "${Rd}✘ HTTP ${HTTP_PORT} ne répond pas après 8s${R}"
fi

WS_CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 1 "http://localhost:${WS_PORT}/" 2>/dev/null)
[ "$WS_CODE" != "000" ] && [ -n "$WS_CODE" ] \
  && echo -e "${G}✔ WS ${WS_PORT} en écoute (HTTP ${WS_CODE})${R}" \
  || echo -e "${Rd}✘ WS ${WS_PORT} injoignable${R}"

curl -sf -o /dev/null -m 1 "http://localhost:${VERIFY_PORT}/verify/ping" 2>/dev/null \
  && echo -e "${G}✔ Verify ${VERIFY_PORT} répond${R}" \
  || echo -e "${Rd}✘ Verify ${VERIFY_PORT} ne répond pas${R}"

echo ""
if [ "$HC_OK" -eq 1 ] && [ "$MW_OK" -eq 1 ] && echo "$HC_JSON" | grep -q '"mongo":"connected"'; then
  echo -e "${G}=== TOUT EST OPÉRATIONNEL (server + mockwallet + Mongo) ===${R}"
else
  echo -e "${Rd}=== DÉGRADÉ — voir le détail ci-dessus (souvent : réseau/DNS device) ===${R}"
fi
