#!/data/data/com.termux/files/usr/bin/bash
cd "$(dirname "$0")" || exit 1

[ -f .env ] || { echo "✗ .env introuvable"; exit 1; }
grep -q '^JWT_SECRET=' .env || { echo "✗ JWT_SECRET absent de .env"; exit 1; }
command -v node >/dev/null 2>&1 || { echo "✗ node absent (pkg install nodejs)"; exit 1; }

envval() { grep -m1 "^$1=" .env | cut -d= -f2- | tr -d '"'"'"' \r'; }

HTTP_PORT="${HTTP_PORT:-$(envval HTTP_PORT)}"; HTTP_PORT="${HTTP_PORT:-8080}"
# L'opérateur par défaut = OPERATOR_ID de .env (celui que le gateway autorise)
DEFAULT_OP="$(envval OPERATOR_ID)"
PLAYER="${1:-test-player-1}"
OPERATOR="${2:-${DEFAULT_OP:-test-operator-1}}"

curl -s -m 2 -o /dev/null "http://localhost:$HTTP_PORT/" || { echo "✗ :$HTTP_PORT ne répond pas, lance ./start.sh"; exit 1; }

TOKEN=$(node tools/gen-test-token.js "$PLAYER" "$OPERATOR") || { echo "✗ génération du token échouée"; exit 1; }
URL="http://localhost:$HTTP_PORT/?token=$TOKEN${DBG:+&dbg=1}"

echo "Joueur: $PLAYER | Opérateur: $OPERATOR | validité: 1h"
if command -v termux-open-url >/dev/null 2>&1; then
  termux-open-url "$URL"
else
  echo "termux-open-url absent, colle cette URL dans Chrome :"
  echo "$URL"
fi
