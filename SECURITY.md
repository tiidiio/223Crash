# SECURITY - 223CRASH

## Secrets

Ne jamais committer:
- .env, .env.crash, .env.*
- MONGO_URI, JWT_SECRET
- .admin-cookie.jar

.gitignore protege ces fichiers.

## Si secret expose

1. Revoquer dans Atlas
2. Rotation secret
3. Mettre a jour .env local + prod
4. git rm --cached, commit, push

## Fairness

- ServerSeed + ClientSeed:Nonce -> HMAC-SHA256 -> crash point
- Verifiable via /verify

## Ports

- 8080 HTTP Runtime, 3000 WS, 8090 Verify
