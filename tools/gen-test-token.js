const fs = require('fs');
const crypto = require('crypto');
const path = require('path');

function b64url(buf) {
  return Buffer.from(buf).toString('base64')
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function loadEnvVar(name) {
  const envPath = path.join(__dirname, '..', '.env');
  const content = fs.readFileSync(envPath, 'utf8');
  const line = content.split('\n').find(l => l.trim().startsWith(name + '='));
  if (!line) throw new Error(`${name} introuvable dans ${envPath}`);
  return line.split('=').slice(1).join('=').trim().replace(/^["']|["']$/g, '');
}

const playerId = process.argv[2] || 'test-player-1';
const operatorId = process.argv[3] || 'test-operator-1';

const secret = loadEnvVar('JWT_SECRET');
const now = Math.floor(Date.now() / 1000);

const header = { alg: 'HS512', typ: 'JWT' };
const payload = { sub: playerId, operatorId, iat: now, exp: now + 3600 };

const headerB64 = b64url(JSON.stringify(header));
const payloadB64 = b64url(JSON.stringify(payload));
const signingInput = `${headerB64}.${payloadB64}`;

const sig = crypto.createHmac('sha512', secret).update(signingInput).digest();
const token = `${signingInput}.${b64url(sig)}`;

console.log(token);
