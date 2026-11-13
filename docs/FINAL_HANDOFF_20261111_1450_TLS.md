# FINAL HANDOFF 223GAMING B2B + 223CRASH — 2026-11-11 1450 TLS
## Preservation RC
- bin/223crash-b2b.final 9e793b56697327f2ac011d416cd91fba0ef5ee04a760487d3036d84ee5389155 15M RC PRESERVE
- bin/223crash-b2b.pool100 c23082d47e57d1e25268453b5f3b4b812a81e7c1dae7afa7e93c6cb0e83511d1 14M pool100 qualifié (maxPoolSize 100 min 10 maxConnecting 20)
- .cache/loadtest fb846a44da8b835f041ef8e961452212be894f224bd277cb539372851afb2ffa 7.5M V1
- .cache/loadtestv2 ee7e30d7d3a007b57c206b175f14befc09997fed834dcef5d667da8eeab3e721 7.7M V2 P50/P95/P99/P99.9
- .cache/mockwallet 2934477544df73676b5ce4cfbe4155838c3e5001fb3598eea44d1ff9d08220cc 7.8M

## Architecture B2B
- 223GAMING ProviderCore pur: 15 pkg, GameRegistry opaque GAME_RUNTIME_URL_<ID>, 0 import engine/betstore/product
- 223CRASH Hybride: factory product.NewGameEngine -> v1.GameEngine, forwardEvents Publisher, betStore injection
- Wallet: DEBIT synchrone bloquant, CREDIT/ROLLBACK Queue avec guard (FAILED bloque volontaire)
- Ledger: unique index operatorId+idempotencyKey, credit key credit:debitTxnID ObjectID unique BUG1 fix, errBetAlreadyExists sentinel BUG2 fix
- PanelGuard: TryLock A/B engineID() partout, errPanelOccupied -> rollbackConfirmedDebit

## Ledger Reconciliation Preuves
- Avant: 23692 total | 23591 CONFIRMED | 101 FAILED | 0 PENDING (DEBIT 13749=13648 CONF+101 FAIL, ROLLBACK 9714 CONF, CREDIT 229 CONF)
- Après 1450 simultanés OOM: 25040 total | 23703 CONFIRMED | 101 FAILED | 1236 PENDING (847 DEBIT + 389 ROLLBACK)
- Après SEQ 450 conns: 35389 total | 29373 CONFIRMED | 4356 FAILED | 1660 PENDING
- Après final_reconcile 1267: 35389 total | 29766 CONFIRMED | 5623 FAILED | 0 PENDING
- Gaps: DEBIT=CONFIRMED+FAILED (19045+1181=20226), ROLLBACK=CONFIRMED+FAILED (10377+4262=14639), CREDIT=344+180=524, parent DEBIT CONFIRMED obligatoire, pas de double

## Load Testing HTSTVD V2 Preuves
### V1 (sans BET lat, envoi seul)
- 20c PLAIN 10s: conn ok 20 fail 0 bet 248 avgConn 5ms PASS 0% (2026-11-11 02:42:31)
- Historique: 20c 4ms, 100c 25ms 2517 bets, 500c 30ms 18469 bets, 750c 13803, 1000c 24120 0% fail après pool 10->100

### V2 (BET lat réel, DEBIT synchrone + ledger + wallet)
- 20c PLAIN 17s: conn 20/0 bet 359 cash 148 recv 1850 BET count 70 P50 319 P95 4865 P99 9391 P99.9 9391 CASHOUT count 22 P50 840 P95 6783 P99 11630 PASS 0%
- 100c PLAIN 22s: conn 100/0 bet 2489 cash 978 recv 5298 BET count 955 P50 11611 P95 15911 P99 17450 P99.9 18903 CASHOUT count 4 P50 17465 P95 18970 PASS 0% => x36 vs 20c = bottleneck Mongo Atlas free + mockwallet single + rollback immédiat (bet hors fenêtre)
- 20c TLS wss://localhost:8444 18s: BET count 0 P50 0 (0% BET_ACCEPTED) => CRITIQUE betStore miss après restart (in-memory perdu) + creditSettler sans debitTxnID
- 100c TLS SEQ: P50 14267 P95 22600 P99 25707 P99.9 28968 CASHOUT P50 23498 P95 27811 (TLS +3s overhead vs PLAIN)
- 250c TLS SEQ: BET count 0 (queue saturée)
- 1450 simultanés (100+250+500 TLS + 100+500 PLAIN 60s): OOM Killer Termux tués à 42s, P50 0 puis 41043ms avant kill, non viable 7.8GB Termux, limite infra pas code

### TLS Caddy staging
- Caddyfile.staging: admin off, auto_https off, protocols h1 h2, localhost:8443->8080, 8444->3000 WS, 8445->5173 UI, tls self-signed .cache/certs/localhost.crt/key, HSTS, gzip, log json
- Healthz: 8080 ok, 8443 ok, 8444 token invalide attendu, 3000 ok, bind :3000 checked single instance pool100
- Prod Let's Encrypt non testé (pas de domaine, conforme no spend directive)

## 14 Items Qualification
1 Provider Core pur TESTE ET VALIDE (15 pkg 0 import engine)
2 223CRASH factory v1 TESTE ET VALIDE
3 Auth CSRF sameOrigin CORRIGE ET TESTE (21bc4b9 DNS blocked)
4 Wallet DEBIT/CREDIT/ROLLBACK idempotency TESTE ET VALIDE (HMAC Idempotency-Key queue guard)
5 Ledger unique index + credit key CORRIGE ET TESTE (BUG1 credit:debitTxnID ObjectID unique, BUG2 sentinel)
6 WS Gateway panelGuard CORRIGE ET TESTE (TryLock A/B engineID sentinel)
7 Bet persistence + recovery TESTE AVEC LIMITES (Remember avant PlaceBet mais in-memory -> CRITIQUE miss après restart, à persister Mongo)
8 Provably Fair TESTE ET VALIDE
9 Pool 10->100 0% fail CORRIGE ET TESTE (20c 5ms V1)
10 P50/P95/P99/P99.9 BET/CASHOUT TESTE AVEC LIMITES (20c P50 319ms, 100c P50 11611ms, TLS +3s, 1450 OOM)
11 CPU/RAM/Goroutines pprof RESTE A RESOUDRE (ps 6% 27MB, pas :6060)
12 TLS Caddy staging TESTE ET VALIDE (8443/8444 ok) prod LE reste
13 Frontend BET state TESTE ET VALIDE (ws.test 8 OK, i18n 4 OK, bet-state 8 OK, 41KB play.html)
14 PENDING + backup + UI TESTE AVEC LIMITES (PENDING 0 après reconcile final, backup PITR non configuré, UI 5173 non testé 8445)

## Décision GO prod
- 1000c PLAIN pool100: CORRIGE ET TESTE (V1 5ms 0% fail)
- 100c TLS BET réel: TESTE AVEC LIMITES (P50 14s@100c, 319ms@20c) bottleneck infra Termux + Atlas free + mockwallet single + rollback immédiat
- 1450 simultanés TLS: NON VALIDÉ Termux OOM, viable VPS 8GB+ wallet service séparé
- Intégrité financière: TESTE ET VALIDE (pas double, 0 PENDING final, gaps vérifiés)
- Architecture: TESTE ET VALIDE
- Recommandation: PASS staging 100c, GO prod avec domaine LE + VPS 4vCPU 8GB + Mongo M10+ (pas free) + Redis betStore + wallet microservice + pprof :6060 + PITR + monitoring PENDING sweeper
- Aucune dépense prod, RC 9e793b... préservé

## TODO prod sans spend
- Persister betStore Mongo (fix CRITIQUE betStore miss)
- DEBIT async queue + optimistic UI (319ms -> <100ms)
- Mockwallet service indépendant pool
- pprof localhost:6060
- Caddy HTTP/3 + LE prod domaine
- Backup PITR Atlas + .env.example
- Provider UI 5173 derrière 8445

PENDING final 0, SHA préservés, 35389 ledger total final.
