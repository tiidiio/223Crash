# go-env.sh — active la toolchain Go "current" pour le shell courant.
# Usage : . scripts/go-env.sh
# À SOURCER (pas d'exécution directe), pas de `set -e` ici.

_go_root_dir="${GO_TOOLCHAINS_DIR:-$HOME/.local/go-toolchains}"
_go_prefix="${PREFIX:-/data/data/com.termux/files/usr}"

if [ -L "$_go_root_dir/current" ] && [ -x "$_go_root_dir/current/bin/go" ]; then
  GOROOT="$(readlink -f "$_go_root_dir/current")"
  export GOROOT
  case ":$PATH:" in
    *":$GOROOT/bin:"*) ;;
    *) PATH="$GOROOT/bin:$PATH" ;;
  esac
  case ":$PATH:" in
    *":$HOME/go/bin:"*) ;;
    *) PATH="$PATH:$HOME/go/bin" ;;
  esac
  export PATH
  export GOTOOLCHAIN=local
  export CGO_ENABLED=0
else
  echo "go-env: aucune toolchain 'current' dans $_go_root_dir — Go système conservé" >&2
fi

if [ -z "${SSL_CERT_FILE:-}" ] && [ -f "$_go_prefix/etc/tls/cert.pem" ]; then
  export SSL_CERT_FILE="$_go_prefix/etc/tls/cert.pem"
fi

unset _go_root_dir _go_prefix
