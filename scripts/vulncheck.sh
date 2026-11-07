#!/data/data/com.termux/files/usr/bin/bash
#
# vulncheck.sh — govulncheck avec la toolchain active, sortie résumée.
# Le rapport complet (traces incluses) est archivé dans REPORT_DIR.
# Code retour : celui de govulncheck (0 = propre, 3 = vulnérabilités atteignables).
#
# Env : REFRESH_TOOL=1 (réinstalle govulncheck avec la toolchain active)
#       GOVULNCHECK=<chemin>  REPORT_DIR=<dir>  REPO_DIR=<dir>
#
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="${REPO_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}"
REPORT_DIR="${REPORT_DIR:-$HOME/.local/state/223crash/vulncheck}"
GOVULN="${GOVULNCHECK:-$HOME/go/bin/govulncheck}"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/go-env.sh"

command -v go >/dev/null 2>&1 || { echo "ERREUR: go introuvable dans le PATH" >&2; exit 1; }
[[ -f "$REPO_DIR/go.mod" ]] || { echo "ERREUR: go.mod introuvable dans $REPO_DIR" >&2; exit 1; }

GOV_VERSION="$(go env GOVERSION)"
echo "toolchain: $GOV_VERSION ($(go env GOROOT))"

if [[ "${REFRESH_TOOL:-0}" == "1" || ! -x "$GOVULN" ]]; then
  echo "installation de govulncheck avec $GOV_VERSION"
  GOFLAGS="${GOFLAGS:-} -p=1" GOGC=50 go install golang.org/x/vuln/cmd/govulncheck@latest
  GOVULN="$(go env GOBIN)"
  [[ -n "$GOVULN" ]] || GOVULN="$(go env GOPATH)/bin"
  GOVULN="$GOVULN/govulncheck"
fi
[[ -x "$GOVULN" ]] || { echo "ERREUR: govulncheck introuvable ($GOVULN)" >&2; exit 1; }

mkdir -p "$REPORT_DIR"
REPORT="$REPORT_DIR/govulncheck-$(date +%Y%m%d-%H%M%S).txt"

cd "$REPO_DIR"
set +e
GOFLAGS="${GOFLAGS:-} -p=1" GOGC=50 CGO_ENABLED=0 "$GOVULN" ./... > "$REPORT" 2>&1
rc=$?
set -e

awk '
  /^Vulnerability #/                { print; getline; print; next }
  /^    (Found|Fixed) in:/          { print; next }
  /^(Your code|No vulnerabilities)/ { print; next }
' "$REPORT" || true

echo "--- code retour govulncheck: $rc | rapport complet: $REPORT"
exit "$rc"
