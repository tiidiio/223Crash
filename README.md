# 223CRASH - Crash Game Product

Entreprise: 223GAMING
Produit: 223CRASH
Type: Game Product B2B
Architecture: OPERATOR -> 223GAMING PROVIDER CORE -> 223CRASH -> OPERATOR

## Description

223CRASH est le premier Game Product de 223GAMING. Crash game avec:
- Engine: Betting 6s, Tick 50ms, GrowthFactor e^(0.00006*t), MaxCrash 5000x, RTP 97%
- Fairness: HMAC-SHA256 serverSeed + clientSeed:nonce -> crashFromX verifiable
- Persistance: MongoDB Atlas DB 223crash (rounds, nonce_counter, fairness_chain, bets PanelA/B)
- Gateway: contracts/product/v1.GameEngine opaque
- Finance: ledger Provider via debit:op:player:betId / credit: / rollback:

## Stack

- Go 1.26.4, MongoDB Atlas, WebSocket
- Ports: 8080 HTTP, 3000 WS, 8090 Verify
- CGO_ENABLED=0 Termux ARM64

## Tests

go vet ./...
GOFLAGS="-p=1" GOGC=50 CGO_ENABLED=0 go test ./internal/engine -count=1
GOFLAGS="-p=1" GOGC=50 CGO_ENABLED=0 go test ./internal/game -count=1

## Licence

Proprietaire - 223GAMING - Tous droits reserves.
