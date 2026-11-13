#!/data/data/com.termux/files/usr/bin/bash
set -e
export JWT_SECRET=${JWT_SECRET:-$(grep JWT_SECRET .env | cut -d= -f2- | tr -d '"')}
echo "JWT_SECRET ok len=${#JWT_SECRET}"
go build -o .cache/loadtest ./cmd/loadtest
echo "=== 100 conns ==="
./.cache/loadtest -c 100 -d 20s
echo "=== 500 conns ==="
./.cache/loadtest -c 500 -d 30s
echo "=== 1000 conns (si RAM) ==="
ulimit -n 4096 2>/dev/null || true
./.cache/loadtest -c 1000 -d 30s || echo "1000 a échoué (normal sur Termux)"
