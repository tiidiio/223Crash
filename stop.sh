#!/data/data/com.termux/files/usr/bin/bash
R='\e[0m'; W='\e[1;97m'; D='\e[90m'; G='\e[1;92m'; Y='\e[1;93m'; Cy='\e[1;96m'; Bl='\e[1;94m'
a(){ echo -e "$1"; sleep 0.05; }

a "${Bl}╔════════════════════════════════════════════════╗${R}"
a "${Bl}║${B}${W} 223CRASH • ARRÊT DU MOTEUR                  ${R}${Bl}║${R}"
a "${Bl}╚════════════════════════════════════════════════╝${R}\n"

PIDFILE="logs/go.pid"

if [ ! -f "$PIDFILE" ]; then
  a "${Y}⚠ Aucun logs/go.pid trouvé — rien à arrêter (ou déjà stoppé).${R}"
  exit 0
fi

PID=$(cat "$PIDFILE")

if ! kill -0 "$PID" 2>/dev/null; then
  a "${Y}⚠ PID $PID déjà mort — nettoyage du pidfile.${R}"
  rm -f "$PIDFILE"
  exit 0
fi

a "${Cy}→ Envoi SIGTERM au PID $PID...${R}"
kill -TERM "$PID" 2>/dev/null

for i in 1 2 3 4 5 6 7 8 9 10; do
  if ! kill -0 "$PID" 2>/dev/null; then
    a "${G}✔ Process $PID arrêté proprement.${R}"
    rm -f "$PIDFILE"
    exit 0
  fi
  sleep 0.3
done

a "${Y}⚠ Pas de shutdown propre après 3s — SIGKILL.${R}"
kill -KILL "$PID" 2>/dev/null
sleep 0.2

if kill -0 "$PID" 2>/dev/null; then
  a "${D}\e[1;91m✘ Échec : PID $PID toujours vivant. Vérifie manuellement (ps | grep $PID).${R}"
  exit 1
else
  a "${G}✔ Process $PID tué (SIGKILL).${R}"
  rm -f "$PIDFILE"
fi
