#!/data/data/com.termux/files/usr/bin/bash
# Tests séquentiels, empreinte mémoire réduite (Termux / Android phantom killer)
# Usage : ./scripts/test.sh                      -> vet + toute la suite
#         ./scripts/test.sh -run TestX ./internal/engine  -> ciblé (sans vet)
set -e
cd "$(dirname "$0")/.."
export GOMAXPROCS=2 GOGC=50
if [ $# -eq 0 ]; then
  go vet -p=1 ./...
  go test -p=1 -count=1 ./...
else
  go test -p=1 -count=1 "$@"
fi
