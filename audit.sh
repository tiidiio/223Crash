#!/data/data/com.termux/files/usr/bin/bash
cd ~/223Gaming/223Crash
sec(){ printf '\n===== %s =====\n' "$*"; }
mask(){ sed -E 's#(://)[^/@ "]*@#\1***@#g'; }

sec "0. ETAT DES PATCHS"
echo "allLanded(index)=$(grep -c allLanded index.html) refreshBalance(gw)=$(grep -c refreshBalance cmd/server/gateway.go) holdUntil(bets)=$(grep -c holdUntil bets.js)"
ls -l --time-style=full-iso bin/server; date

sec "1. SECRETS"
ls -la .env 2>&1
sed -E 's/=.*/=<masqué>/' .env 2>/dev/null
echo "--- suivis par git:"; git ls-files 2>/dev/null | grep -E '\.env|\.pem|\.key|\.bak'
echo "--- .gitignore:"; cat .gitignore 2>/dev/null
echo "--- secrets en dur:"
grep -rnEi '(mongodb(\+srv)?://|secret|password|apikey|api_key|hs512)' --include=*.go --include=*.js --include=*.sh --include=*.html . 2>/dev/null | grep -v '_test.go' | mask | cut -c1-150 | head -30

sec "2. WEBSOCKET / JWT"
grep -rnE 'CheckOrigin|SetReadLimit|SetReadDeadline|SetWriteDeadline|MaxBytesReader|rate\.|Limiter|maxConn|MaxConn' --include=*.go cmd internal | grep -v _test.go | cut -c1-160
echo "--- JWT:"; grep -rnE 'jwt\.|ParseWithClaims|SigningMethod|ExpiresAt|Audience|WithValidMethods|Valid\(' --include=*.go cmd internal | grep -v _test.go | cut -c1-160 | head -20

sec "3. VALIDATION DES MISES"
grep -rnE 'MaxBet|MinBet|maxBet|minBet|amount *<=|amount *<|amount *>|slotToPanel|NaN|IsInf|autoCashout|cashoutTarget' --include=*.go cmd internal | grep -v _test.go | cut -c1-160 | head -30

sec "4. PROVABLY FAIR"
grep -rnEi 'serverSeed|clientSeed|sha256|hmac|commit|hashChain|houseEdge|house_edge|crashPoint *:?=' --include=*.go cmd internal | grep -v _test.go | cut -c1-160 | head -40
echo "--- champs envoyés dans ROUND_START / TICK:"; grep -rnE '"ROUND_START"|"TICK"' --include=*.go cmd internal | grep -v _test.go | cut -c1-200

sec "5. ENDPOINTS HTTP"
grep -rnE 'HandleFunc|Handle\(|pprof|Access-Control|ListenAndServe|net\.Listen|Addr:' --include=*.go cmd internal | grep -v _test.go | cut -c1-170
echo "--- ports en écoute:"; (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | head -15

sec "6. WALLET / CALLBACKS"
grep -rnEi 'signature|hmac|X-Signature|Authorization|TLS|InsecureSkipVerify' --include=*.go internal cmd | grep -v _test.go | cut -c1-160 | head -25
echo "--- queue: retry/persistance:"; grep -rnEi 'retry|backoff|dead|persist|MaxAttempts|Enqueue' --include=*.go internal | grep -v _test.go | cut -c1-150 | head -20

sec "7. QUALITE"
go vet ./... 2>&1 | head -25
timeout 240 go test ./... 2>&1 | tail -25
command -v govulncheck >/dev/null && govulncheck ./... 2>&1 | tail -20 || echo "govulncheck absent (go install golang.org/x/vuln/cmd/govulncheck@latest)"

sec "8. FRONT"
grep -nE 'innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|localStorage|token' index.html bets.js ws.js | cut -c1-150 | head -25

sec "9. DETTE CONNUE"
grep -rnE 'TODO|FIXME|XXX|Tranche|best effort|hors scope|not implemented' --include=*.go --include=*.js cmd internal *.js 2>/dev/null | grep -v _test.go | cut -c1-160 | head -30

sec "10. sendJSON / trySend (panic sur client fermé ?)"
sed -n 83,106p cmd/server/gateway.go
