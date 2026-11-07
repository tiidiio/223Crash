#!/data/data/com.termux/files/usr/bin/bash
#
# install-go-toolchain.sh — compile et installe une toolchain Go officielle
# depuis les sources (Termux / android-arm64).
#
#  - bootstrap avec le Go présent dans le PATH
#  - SHA-256 vérifié contre go.dev (ou EXPECTED_SHA256 pour un hash épinglé)
#  - patch des chemins Android (resolv.conf / hosts / nsswitch) avant build
#  - build en staging, smoke tests DNS + TLS, installation atomique, rollback
#  - ne modifie jamais le Go du paquet Termux
#
# Usage : install-go-toolchain.sh go1.26.10
# Env   : FORCE=1  SKIP_SMOKE=1  BUILD_JOBS=2  EXPECTED_SHA256=<hex>
#         GO_TOOLCHAINS_DIR=$HOME/.local/go-toolchains  KEEP_BACKUPS=1
#
set -Eeuo pipefail
umask 022

PREFIX="${PREFIX:-/data/data/com.termux/files/usr}"
ROOT_DIR="${GO_TOOLCHAINS_DIR:-$HOME/.local/go-toolchains}"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/go-toolchain"
MIN_FREE_KB="${MIN_FREE_KB:-2097152}"
BUILD_JOBS="${BUILD_JOBS:-2}"
KEEP_BACKUPS="${KEEP_BACKUPS:-1}"
FORCE="${FORCE:-0}"
SKIP_SMOKE="${SKIP_SMOKE:-0}"
EXPECTED_SHA256="${EXPECTED_SHA256:-}"
VERSION="${1:-${GO_VERSION:-}}"

WORK_DIR=""
STAGE=""
LOCK_DIR=""
PROMOTED=0
LOCKED=0
WAKE=0

ts()   { date '+%H:%M:%S'; }
log()  { printf '[%s] %s\n' "$(ts)" "$*"; }
warn() { printf '[%s] WARN %s\n' "$(ts)" "$*" >&2; }
die()  { printf '[%s] ERREUR %s\n' "$(ts)" "$*" >&2; exit 1; }

usage() {
  cat <<USAGE
Usage: $(basename "$0") <goX.Y.Z>
Variables: FORCE=1 SKIP_SMOKE=1 BUILD_JOBS=2 EXPECTED_SHA256=<hex> GO_TOOLCHAINS_DIR=<dir> KEEP_BACKUPS=1
USAGE
}

cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then rm -rf "$WORK_DIR"; fi
  if [[ "$PROMOTED" -eq 0 && -n "$STAGE" && -d "$STAGE" ]]; then rm -rf "$STAGE"; fi
  if [[ "$LOCKED" -eq 1 && -n "$LOCK_DIR" ]]; then rm -rf "$LOCK_DIR"; fi
  if [[ "$WAKE" -eq 1 ]]; then termux-wake-unlock >/dev/null 2>&1 || true; fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'warn "commande en échec (ligne $LINENO): $BASH_COMMAND"' ERR

# ---------------------------------------------------------------- arguments
[[ -n "$VERSION" ]] || { usage; exit 2; }
[[ "$VERSION" =~ ^go1\.[0-9]+(\.[0-9]+)?$ ]] || die "version invalide: '$VERSION' (attendu: go1.X.Y)"

mkdir -p "$ROOT_DIR" "$STATE_DIR"
LOG="$STATE_DIR/install-$VERSION-$(date +%Y%m%d-%H%M%S).log"
exec > >(tee -a "$LOG") 2>&1
log "journal complet: $LOG"

for bin in curl tar sha256sum awk sed grep df find mv ln readlink timeout env head stat mktemp; do
  command -v "$bin" >/dev/null 2>&1 || die "outil manquant: $bin"
done

# ---------------------------------------------------------------- verrou
LOCK_DIR="$ROOT_DIR/.install.lock"
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  old_pid="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
  if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
    LOCK_DIR=""
    die "installation déjà en cours (pid $old_pid)"
  fi
  warn "verrou obsolète (pid ${old_pid:-?}) — récupération"
  rm -rf "$LOCK_DIR"
  mkdir "$LOCK_DIR"
fi
echo "$$" > "$LOCK_DIR/pid"
LOCKED=1

if command -v termux-wake-lock >/dev/null 2>&1; then
  termux-wake-lock && WAKE=1
fi

# ---------------------------------------------------------------- helpers
TARGET="$ROOT_DIR/$VERSION"

activate_current() {
  ln -sfn "$TARGET" "$ROOT_DIR/.current.$$"
  mv -Tf "$ROOT_DIR/.current.$$" "$ROOT_DIR/current"
  log "toolchain active: $ROOT_DIR/current -> $TARGET"
}

# ---------------------------------------------------------------- déjà installé ?
if [[ -x "$TARGET/bin/go" && "$FORCE" != "1" ]]; then
  installed="$(head -n1 "$TARGET/VERSION" 2>/dev/null || true)"
  if [[ "$installed" == "$VERSION" ]]; then
    log "$VERSION déjà installé dans $TARGET (FORCE=1 pour recompiler)"
    activate_current
    exit 0
  fi
fi

# ---------------------------------------------------------------- préflight
BOOT_GO="$(command -v go || true)"
[[ -n "$BOOT_GO" ]] || die "aucun Go de bootstrap dans le PATH (pkg install golang)"
BOOT_ROOT="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$BOOT_GO" env GOROOT)"
BOOT_VER="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$BOOT_GO" env GOVERSION)"
HOST_OS="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$BOOT_GO" env GOOS)"
HOST_ARCH="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$BOOT_GO" env GOARCH)"
[[ -n "$BOOT_ROOT" && -d "$BOOT_ROOT/src" ]] || die "GOROOT de bootstrap invalide: '$BOOT_ROOT'"

avail_kb="$(df -Pk "$ROOT_DIR" | awk 'NR==2 {print $4}')"
[[ "${avail_kb:-0}" -ge "$MIN_FREE_KB" ]] || die "espace disque insuffisant: ${avail_kb:-0} Ko libres, $MIN_FREE_KB requis"

log "cible: $VERSION | hôte: $HOST_OS/$HOST_ARCH | bootstrap: $BOOT_VER ($BOOT_ROOT) | libre: $((avail_kb / 1024)) Mo"

# ---------------------------------------------------------------- téléchargement
WORK_DIR="$(mktemp -d "$ROOT_DIR/.work-XXXXXX")"
SRC_FILE="$VERSION.src.tar.gz"
SRC_URL="https://go.dev/dl/$SRC_FILE"
CURL=(curl --fail --location --silent --show-error --proto '=https' --tlsv1.2
      --retry 5 --retry-delay 3 --retry-connrefused --connect-timeout 20)

if [[ -z "$EXPECTED_SHA256" ]]; then
  log "récupération du SHA-256 officiel (go.dev/dl, JSON)"
  "${CURL[@]}" -o "$WORK_DIR/releases.json" 'https://go.dev/dl/?mode=json&include=all'
  EXPECTED_SHA256="$(awk -v f="$SRC_FILE" '
      index($0, "\"filename\": \"" f "\"") { found = 1; next }
      found && /"sha256"/ { gsub(/[",]/, "", $2); print $2; exit }
    ' "$WORK_DIR/releases.json")"
else
  log "SHA-256 épinglé via EXPECTED_SHA256"
fi
EXPECTED_SHA256="$(printf '%s' "$EXPECTED_SHA256" | tr 'A-F' 'a-f')"
[[ "$EXPECTED_SHA256" =~ ^[0-9a-f]{64}$ ]] || die "SHA-256 attendu introuvable ou invalide pour $SRC_FILE (version inexistante ?)"

log "téléchargement: $SRC_URL"
"${CURL[@]}" -C - -o "$WORK_DIR/$SRC_FILE" "$SRC_URL"

size_b="$(stat -c %s "$WORK_DIR/$SRC_FILE")"
[[ "$size_b" -gt 10000000 ]] || die "archive trop petite ($size_b octets) — téléchargement corrompu"

ACTUAL_SHA256="$(sha256sum "$WORK_DIR/$SRC_FILE" | awk '{print $1}')"
if [[ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]]; then
  die "SHA-256 différent — attendu $EXPECTED_SHA256, obtenu $ACTUAL_SHA256"
fi
log "SHA-256 OK: $ACTUAL_SHA256"

# ---------------------------------------------------------------- extraction
STAGE="$(mktemp -d "$ROOT_DIR/.stage-XXXXXX")"
chmod 755 "$STAGE"
tar -xzf "$WORK_DIR/$SRC_FILE" -C "$STAGE" --strip-components=1
[[ -f "$STAGE/src/make.bash" ]] || die "archive inattendue: src/make.bash absent"
[[ "$(head -n1 "$STAGE/VERSION")" == "$VERSION" ]] || die "VERSION de l'archive ≠ $VERSION"
rm -f "$WORK_DIR/$SRC_FILE"

# ---------------------------------------------------------------- patch Android
patch_android_paths() {
  local dir="$STAGE/src/net" f count=0
  while IFS= read -r f; do
    sed -i \
      -e "s#\"/etc/resolv\.conf\"#\"${PREFIX}/etc/resolv.conf\"#g" \
      -e "s#\"/etc/hosts\"#\"${PREFIX}/etc/hosts\"#g" \
      -e "s#\"/etc/nsswitch\.conf\"#\"${PREFIX}/etc/nsswitch.conf\"#g" \
      "$f"
    count=$((count + 1))
  done < <(grep -rlE '"/etc/(resolv\.conf|hosts|nsswitch\.conf)"' "$dir" --include='*.go' --exclude='*_test.go' || true)
  (( count > 0 )) || die "patch Android: aucun littéral /etc/* trouvé dans src/net (structure du source changée ?)"
  grep -rqF "${PREFIX}/etc/resolv.conf" "$dir" --include='*.go' \
    || die "patch Android: résolveur DNS non patché"
  log "patch chemins Android appliqué sur $count fichier(s) de src/net"
}
patch_android_paths

# ---------------------------------------------------------------- build
log "compilation (CGO_ENABLED=0, GOMAXPROCS=$BUILD_JOBS) — 10 à 25 min"
(
  cd "$STAGE/src"
  env -u GOROOT -u GOFLAGS -u GOBIN \
    GOROOT_BOOTSTRAP="$BOOT_ROOT" GOTOOLCHAIN=local CGO_ENABLED=0 \
    GOMAXPROCS="$BUILD_JOBS" GOGC=50 \
    ./make.bash
)

STAGE_VER_OUT="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$STAGE/bin/go" version)"
log "build terminé: $STAGE_VER_OUT"
[[ "$STAGE_VER_OUT" == "go version $VERSION "* ]] || die "version compilée inattendue: $STAGE_VER_OUT"

# ---------------------------------------------------------------- smoke tests
smoke_test() {
  local root="$1" dir="$WORK_DIR/smoke"
  mkdir -p "$dir"
  cat > "$dir/main.go" << 'GOSMOKE'
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	failed := false

	addrs, err := net.DefaultResolver.LookupHost(ctx, "go.dev")
	if err != nil || len(addrs) == 0 {
		fmt.Println("FAIL dns LookupHost go.dev:", err)
		failed = true
	} else {
		fmt.Println("OK   dns LookupHost go.dev ->", addrs[0])
	}

	_, srv, err := net.DefaultResolver.LookupSRV(ctx, "imaps", "tcp", "gmail.com")
	if err != nil || len(srv) == 0 {
		fmt.Println("WARN dns LookupSRV:", err)
	} else {
		fmt.Println("OK   dns LookupSRV ->", srv[0].Target)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://go.dev/dl/?mode=json")
	if err != nil {
		fmt.Println("FAIL tls https go.dev:", err)
		failed = true
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Println("FAIL tls https go.dev: statut", resp.StatusCode)
			failed = true
		} else {
			fmt.Println("OK   tls https go.dev ->", resp.Proto, resp.TLS.Version)
		}
	}

	if failed {
		os.Exit(1)
	}
}
GOSMOKE
  local cert=""
  if [[ -z "${SSL_CERT_FILE:-}" && -f "$PREFIX/etc/tls/cert.pem" ]]; then cert="$PREFIX/etc/tls/cert.pem"; fi
  (
    cd "$dir"
    env -u GOROOT -u GOFLAGS \
      GOTOOLCHAIN=local CGO_ENABLED=0 GOWORK=off GOGC=50 GOMAXPROCS="$BUILD_JOBS" \
      GOCACHE="$WORK_DIR/gocache" PATH="$root/bin:$PATH" \
      ${cert:+SSL_CERT_FILE="$cert"} \
      timeout 900 "$root/bin/go" run -p "$BUILD_JOBS" main.go
  )
}

if [[ "$SKIP_SMOKE" == "1" ]]; then
  warn "smoke tests ignorés (SKIP_SMOKE=1)"
else
  log "smoke tests réseau (DNS patché + TLS) avec la toolchain compilée"
  smoke_test "$STAGE" || die "smoke tests en échec — installation annulée, toolchain existante intacte"
  log "smoke tests OK"
fi

# ---------------------------------------------------------------- installation atomique
BACKUP=""
if [[ -e "$TARGET" ]]; then
  BACKUP="$TARGET.bak-$(date +%Y%m%d-%H%M%S)"
  mv "$TARGET" "$BACKUP"
  log "installation précédente sauvegardée: $BACKUP"
fi
mv "$STAGE" "$TARGET"
PROMOTED=1

rollback() {
  warn "rollback de l'installation"
  rm -rf "$TARGET"
  if [[ -n "$BACKUP" && -e "$BACKUP" ]]; then mv "$BACKUP" "$TARGET"; fi
}

INSTALLED_ROOT="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$TARGET/bin/go" env GOROOT 2>/dev/null || true)"
INSTALLED_VER="$(env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local "$TARGET/bin/go" version 2>/dev/null || true)"
if [[ "$INSTALLED_ROOT" != "$TARGET" || "$INSTALLED_VER" != "go version $VERSION "* ]]; then
  rollback
  die "vérification post-installation en échec (GOROOT='$INSTALLED_ROOT', version='$INSTALLED_VER')"
fi

activate_current

if [[ "$KEEP_BACKUPS" =~ ^[0-9]+$ ]]; then
  # shellcheck disable=SC2012
  ls -dt "$TARGET".bak-* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | while IFS= read -r old; do
    rm -rf "$old"
    log "ancienne sauvegarde supprimée: $old"
  done
fi

log "TERMINÉ: $INSTALLED_VER"
log "GOROOT: $TARGET | sha256 source: $ACTUAL_SHA256"
log "activation: . scripts/go-env.sh"
