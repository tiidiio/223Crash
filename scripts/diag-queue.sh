#!/data/data/com.termux/files/usr/bin/bash
cd "$(dirname "$0")/.." || exit 1
set -a; . ./.env; set +a
OUT=logs/diag-queue.txt
{
  echo "##### 1. ECHEC DU TEST"
  go test -v -run 'TestProcess_AcceptedButConfirmFails' ./internal/wallet/ 2>&1 | head -40
  echo; echo "##### 2. TEST"
  awk '/^func TestProcess_AcceptedButConfirmFails/,/^}/' internal/wallet/queue_test.go
  echo; echo "##### 3. process()"
  awk '/^func \(q \*Queue\) process/,/^}/' internal/wallet/queue.go
  echo; echo "##### 4. USAGES"
  grep -n -E "Confirm|\.Fail\(|Pending|deadLetter|DeadLetter" internal/wallet/queue.go internal/wallet/queue_mongo.go
  echo; echo "##### 5. TAILLES"
  wc -l internal/wallet/*.go cmd/server/gateway.go
  echo; echo "##### 6. FONCTIONS gateway.go"
  grep -n "^func" cmd/server/gateway.go
} 2>&1 | sed -E 's#(mongodb(\+srv)?://)[^@]*@#\1***@#g' > "$OUT"
echo "OK -> $OUT ($(wc -l < "$OUT") lignes)"
