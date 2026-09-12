#!/data/data/com.termux/files/usr/bin/bash
export LC_ALL=C.UTF-8 LANG=C.UTF-8   # FIX racine : sans ca, bash compte les emojis/blocs en OCTETS

clear
[ -f ~/223Gaming/.env ] && set -a && source ~/223Gaming/.env && set +a

R='\e[0m'; W='\e[1;97m'; D='\e[90m'; G='\e[1;92m'; Y='\e[1;93m'; Cy='\e[1;96m'; Bl='\e[1;94m'; M='\e[1;95m'; Or='\e[38;5;208m'; B='\e[1m'; Rd='\e[1;91m'
mkdir -p bin logs; rm -f logs/*.pid

pkill -f './bin/server' 2>/dev/null
sleep 0.3

NOW=$(date "+%Y-%m-%d %H:%M:%S")

HTTP_PORT="${HTTP_PORT:-8080}"
WS_PORT="${WS_PORT:-3000}"
VERIFY_PORT="${VERIFY_PORT:-8090}"

HAS_JQ=0
command -v jq >/dev/null 2>&1 && HAS_JQ=1

WIDE_EMOJI=("👤" "🎓" "⚙️" "📞" "📧" "🌍" "🕒" "🚀" "🎮" "🔐" "🌐")

row() {
  local border_color="$1" content="$2" inner="$3"
  local stripped="$content"
  stripped=$(printf '%s' "$stripped" | sed -E 's/\\e\[[0-9;]*m//g')
  local charcount=${#stripped}
  local wide=0 e without occ
  for e in "${WIDE_EMOJI[@]}"; do
    without=${stripped//$e/}
    occ=$(( (${#stripped} - ${#without}) / ${#e} ))
    wide=$(( wide + occ ))
  done
  local dispwidth=$(( charcount + wide ))
  local pad=$(( inner - dispwidth ))
  (( pad < 0 )) && pad=0
  printf "%b║%b%*s%b║%b\n" "$border_color" "$content" "$pad" "" "$border_color" "$R"
  sleep 0.06
}

bar() { local color="$1" l="$2" fill="$3" r="$4" inner="$5"; printf "%b%s" "$color"; printf "%s" "$l"; local i; for ((i=0;i<inner+2;i++)); do printf "%s" "$fill"; done; printf "%s%b\n" "$r" "$R"; sleep 0.06; }

a(){ echo -e "$1"; sleep 0.06; }

INNER1=48
INNER2=51   # dimensionnee pour le pire cas : port 5 chiffres + code HTTP 3 chiffres
INNER3=36

bar "$Bl" "╔" "═" "╗" "$INNER1"
row "$Bl" "${B}${W} 223GAMING STUDIO • LAB iGAMING B2B" "$INNER1"
row "$Bl" " Produit: ${G}223CRASH ${Y}v2.2.0.56 ${D}[STABLE B2B]" "$INNER1"
bar "$Bl" "╠" "═" "╣" "$INNER1"
row "$Bl" "${B}${Y} FONDATEUR & PDG" "$INNER1"
row "$Bl" " ${W}👤 Nom   : ${B}${Cy}Tidiane Diallo" "$INNER1"
row "$Bl" " ${W}🎓 Rôle  : ${B}${Y}Architecte Full-Stack Senior" "$INNER1"
row "$Bl" " ${W}⚙️ Stack : ${B}${G}Go ${W}• ${M}MongoDB ${W}• ${Or}B2B" "$INNER1"
row "$Bl" " ${W}📞 Tél   : ${B}${Or}(+223) 70 56 05 15" "$INNER1"
row "$Bl" " ${W}📧 Email : ${B}${W}tidiane.s.diallo@gmail.com" "$INNER1"
row "$Bl" " ${W}🌍 Pays  : ${B}${G}Mali - Bamako" "$INNER1"
row "$Bl" " ${D}🕒 Boot  : ${W}${NOW}" "$INNER1"
bar "$Bl" "╠" "═" "╣" "$INNER1"
row "$Bl" " 🚀 MOTEUR : ${G}Go 1.22 • net/http • Haute Perf" "$INNER1"
row "$Bl" " 🎮 JEU    : ${Cy}Crash x5000 • Temps Réel B2B" "$INNER1"
row "$Bl" " 🔐 ÉQUITÉ : ${Y}SHA256 HMAC • Prouvable Équitable" "$INNER1"
row "$Bl" " 🌐 LIVE   : ${G}99.9% Uptime • API Gateway" "$INNER1"
bar "$Bl" "╚" "═" "╝" "$INNER1"
a ""

bar "$Y" "╔" "═" "╗" "$INNER2"
row "$Y" "${W} [1/4] Compilation Go" "$INNER2"

GOFLAGS="-p=1" GOGC=50 CGO_ENABLED=0 go build -o bin/server ./cmd/server 2>logs/build.log
if [ $? -ne 0 ]; then
  row "$Y" "   ${Rd}✘ ÉCHEC DE COMPILATION" "$INNER2"
  bar "$Y" "╚" "═" "╝" "$INNER2"
  echo -e "${Rd}Erreur de compilation — voir logs/build.log:${R}"
  tail -n 20 logs/build.log
  exit 1
fi
row "$Y" "   ${G}Go binaire  ██████████ ✔ COMPILÉ" "$INNER2"

nohup ./bin/server >logs/server.log 2>&1 &
echo $! >logs/go.pid
PID=$(cat logs/go.pid)

row "$Y" "" "$INNER2"
row "$Y" "${W} [2/4] Démarrage du processus" "$INNER2"

sleep 0.8
if ! kill -0 "$PID" 2>/dev/null; then
  row "$Y" "   ${Rd}✘ PROCESSUS ARRÊTÉ (PID $PID)" "$INNER2"
  bar "$Y" "╚" "═" "╝" "$INNER2"
  echo -e "${Rd}Le processus a échoué au démarrage — voir logs/server.log:${R}"
  tail -n 20 logs/server.log
  exit 1
fi
row "$Y" "   ${G}Processus   ██████████ ✔ PID $PID" "$INNER2"

row "$Y" "" "$INNER2"
row "$Y" "${W} [3/4] Vérification HTTP + MongoDB" "$INNER2"

HC_JSON=""
HC_OK=0
for i in 1 2 3 4 5 6 7 8; do
  HC_JSON=$(curl -s -m 2 "http://localhost:${HTTP_PORT}/healthz" 2>/dev/null)
  if [ -n "$HC_JSON" ]; then
    HC_OK=1
    break
  fi
  sleep 0.5
done

MONGO_STATE="unknown"
APP_STATE="unknown"
if [ "$HC_OK" -eq 1 ]; then
  if [ "$HAS_JQ" -eq 1 ]; then
    APP_STATE=$(echo "$HC_JSON" | jq -r '.status // "unknown"')
    MONGO_STATE=$(echo "$HC_JSON" | jq -r '.mongo // "unknown"')
  else
    APP_STATE=$(echo "$HC_JSON" | grep -o '"status":"[^"]*"' | cut -d'"' -f4)
    MONGO_STATE=$(echo "$HC_JSON" | grep -o '"mongo":"[^"]*"' | cut -d'"' -f4)
  fi
fi

if [ "$HC_OK" -eq 1 ] && [ "$APP_STATE" = "ok" ]; then
  row "$Y" "   ${G}HTTP ${HTTP_PORT}   ██████████ ✔ RÉPOND" "$INNER2"
else
  row "$Y" "   ${Rd}HTTP ${HTTP_PORT}   ✘ PAS DE RÉPONSE" "$INNER2"
fi

if [ "$MONGO_STATE" = "connected" ]; then
  row "$Y" "   ${G}MongoDB      ██████████ ✔ CONNECTÉ" "$INNER2"
elif [ "$MONGO_STATE" = "down" ]; then
  row "$Y" "   ${Rd}MongoDB      ✘ DÉCONNECTÉ" "$INNER2"
else
  row "$Y" "   ${Rd}MongoDB      ? INDÉTERMINÉ" "$INNER2"
fi

row "$Y" "" "$INNER2"
row "$Y" "${W} [4/4] Services réseau" "$INNER2"

WS_CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 1 "http://localhost:${WS_PORT}/" 2>/dev/null)
if [ "$WS_CODE" != "000" ] && [ -n "$WS_CODE" ]; then
  row "$Y" "   ${G}WS   ${WS_PORT}   ██████████ ✔ EN ÉCOUTE (HTTP ${WS_CODE})" "$INNER2"
else
  row "$Y" "   ${Rd}WS   ${WS_PORT}   ✘ INJOIGNABLE" "$INNER2"
fi

VERIFY_OK=0
curl -sf -o /dev/null -m 1 "http://localhost:${VERIFY_PORT}/verify/ping" 2>/dev/null && VERIFY_OK=1
if [ "$VERIFY_OK" -eq 1 ]; then
  row "$Y" "   ${G}Vérif. ${VERIFY_PORT} ██████████ ✔ RÉPOND" "$INNER2"
else
  row "$Y" "   ${Rd}Vérif. ${VERIFY_PORT} ✘ PAS DE RÉPONSE" "$INNER2"
fi

bar "$Y" "╚" "═" "╝" "$INNER2"
a ""

ALL_OK=0
if [ "$HC_OK" -eq 1 ] && [ "$APP_STATE" = "ok" ] && [ "$MONGO_STATE" = "connected" ]; then
  ALL_OK=1
fi

if [ "$ALL_OK" -eq 1 ]; then
  bar "$G" "┏" "━" "┓" "$INNER3"
  row "$G" "${B}${W} MOTEUR EN LIGNE • PID $PID" "$INNER3"
  row "$G" "${W} HTTP OK • MONGO OK • COMPILATION OK" "$INNER3"
  bar "$G" "┗" "━" "┛" "$INNER3"
else
  bar "$Rd" "┏" "━" "┓" "$INNER3"
  row "$Rd" " DÉGRADÉ • VOIR DÉTAIL CI-DESSUS" "$INNER3"
  bar "$Rd" "┗" "━" "┛" "$INNER3"
fi
echo ""
