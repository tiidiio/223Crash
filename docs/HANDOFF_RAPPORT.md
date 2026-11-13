# HANDOFF 223CRASH B2B - 2026-11-11

## Binaires prod
- bin/223crash-b2b 15M (séparé 223GAMING infra vs 223CRASH produit)
- .cache/mockwallet 7.8M (wallet opérateur mock)
- .cache/loadtest 7.5M (100/500 conns 0% fail 25-30ms)
- .cache/gentoken 4.1M

## Charge validée (0% fail <0.5% GLI)
- 20 conns: 4ms 191 bets
- 100 conns: 25ms 2517 bets 994 cashouts
- 500 conns: 30ms 18469 bets 7390 cashouts

## Architecture finale
- 223GAMING infra: ProviderCore, OperatorRegistry, Auth JWT HS512, Wallet DEBIT/CREDIT/ROLLBACK queue avec retry 0.5s-60s 6 tentatives, Ledger provider_ledger_entries unique index operatorId_1_idempotencyKey_1, WS Gateway maxConns 5000, Console B2B
- 223CRASH produit: CrashEngine, RoundLifecycle, BetManagement, Cashout (multiplicateur serveur autoritaire), Multiplier, CrashResolution, Provably Fair NonceStore+ChainStore Mongo persistants
- Séparation: main.go n'importe plus internal/engine, factory product.NewGameEngine -> v1.GameEngine
- forwardEvents utilise PublishRoundStart/PublishTick/PublishCrash (méthodes réelles)
- encodeDebitContext dans internal/product/money.go (format round=%s|panel=%d|amount=%d|autoCashout=%.2f) + crash/debit.go

## Ledger réel
- total, by type DEBIT/CREDIT/ROLLBACK, by status CONFIRMED/PENDING via quick.go
- overview admin agrégé: buckets + health components

## Sécurité
- JWT HS512, exp required, subject pattern ^[A-Za-z0-9_.-]{1,64}$ ':' interdit, JWT_SECRET >=32
- operator allowlist Mongo status=active refresh 5s fail-closed
- HMAC-SHA256 wallet callback, prodguard bloque http/localhost en prod
- idempotencyKey debit:op:player:betId + credit:debitTxnId (ObjectID unique)
- Rate limit 10msg/s burst 20 viol 100 -> disconnect

## Frontend
- Vanilla 41KB prod: play.html 2.6K ui.css 7.8K ws.js 9.8K bets.js 14K ui-canvas.js 6.4K ui-hud.js 5.8K
- Provider UI React typed: adminApi.ts OverviewData TransactionsPage HealthComponent, dist 159KB gzip 51KB

## Docs B2B
- OPERATOR_INTEGRATION.md, WALLET_SPEC.md, SECURITY_AUDIT.md, PROD_DEPLOYMENT.md

## TODO
- Provider UI page noire: vite.config.ts proxy 8081->8080 + apiClient baseURL http://127.0.0.1:8080 rebuild
- 1000 conns: ulimit -n 8192, GOMEMLIMIT
- TLS Caddy/Nginx devant 8080/3000, backup Mongo PITR
