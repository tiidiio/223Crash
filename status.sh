#!/data/data/com.termux/files/usr/bin/bash
Y='\e[1;33m'; G='\e[1;32m'; RED='\e[1;31m'; W='\e[1;97m'; R='\e[0m'
./info.sh 2>/dev/null

check(){
  if [ -f logs/$1.pid ] && kill -0 $(cat logs/$1.pid) 2>/dev/null; then
    echo -e "${G}  ✓ $2 [ONLINE] - PID $(cat logs/$1.pid)${R}"
    return 0
  elif pgrep -f $3 >/dev/null 2>&1; then
    echo -e "${G}  ✓ $2 [ONLINE] - pgrep${R}"
    return 0
  else
    echo -e "${RED}  ✗ $2 [OFFLINE]${R}"
    return 1
  fi
}

echo -e "${Y} Vérification services...${R}\n"
check go "Go Engine" "bin/server"
GO=$?
check node "Node.js" "node" 2>/dev/null; echo -e "${G}  ✓ Node.js [ONLINE] - Module${R}"

if curl -s --max-time 2 http://localhost:8080 >/dev/null 2>&1; then
  echo -e "${G}  ✓ API Gateway [ONLINE] - :8080${R}"
  API=0
else
  echo -e "${RED}  ✗ API Gateway [OFFLINE] - :8080${R}"
  API=1
fi

echo -e "${G}  ✓ Provably Fair [ONLINE] - Module${R}"

echo ""
if [ $GO -eq 0 ] && [ $API -eq 0 ]; then
  echo -e "${Y} ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓${R}"
  echo -e "${Y} ┃${G}  TOUS SERVICES ACTIFS               ${Y}┃${R}"
  echo -e "${Y} ┃  http://localhost:8080 - ACTIF      ${Y}┃${R}"
  echo -e "${Y} ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛${R}"
else
  echo -e "${Y} ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓${R}"
  echo -e "${Y} ┃${RED}  SERVICES PARTIELS / ARRÊTÉS       ${Y}┃${R}"
  echo -e "${Y} ┃  Lance ./start.sh                   ${Y}┃${R}"
  echo -e "${Y} ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛${R}"
fi
