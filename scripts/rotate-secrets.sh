#!/data/data/com.termux/files/usr/bin/bash
set -e
cd ~/223Gaming/223Crash
rnd(){ head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }
cp .env ".env.bak.$(date +%s)"
sed -i -E "s/^JWT_SECRET=.*/JWT_SECRET=$(rnd 64)/" .env
sed -i -E "s/^WALLET_HMAC_SECRET=.*/WALLET_HMAC_SECRET=$(rnd 32)/" .env
chmod 600 .env
sed -E 's/=.*/=<masqué>/' .env
echo "OK : relance ./stop.sh && ./start.sh, puis supprime .env.bak.*"
