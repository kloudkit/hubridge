#!/bin/sh
set -eu
HUBD_URL=''
CODE=''
HOME_DIR="${HUBD_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/hubd}"
MACHINE=''
TOKEN_FILE=''
VERSION=''
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  B=$(printf '\033[1m'); D=$(printf '\033[2m'); R=$(printf '\033[0m')
  G=$(printf '\033[32m'); E=$(printf '\033[31m'); W=$(printf '\033[33m'); A=$(printf '\033[38;5;202m')
else B=''; D=''; R=''; G=''; E=''; W=''; A=''; fi
die() { printf '  %s✗%s %s\n' "$E" "$R" "$*" >&2; exit 1; }
ok() { printf '  %s✓%s %s\n' "$G" "$R" "$*"; }
note() { [ -n "$1" ] && printf '%s\n' "$1" | sed "s/^/    $D/; s/$/$R/"; return 0; }
run() { label=$1; shift; if out=$("$@" 2>&1); then ok "$label"; note "$out"; else printf '  %s✗%s %s\n' "$E" "$R" "$label" >&2; note "$out" >&2; exit 1; fi; }
try() { label=$1; shift; if out=$("$@" 2>&1); then ok "$label"; note "$out"; else printf '  %s!%s %s; kept the current build, the daemon retries on its own\n' "$W" "$R" "$label"; note "$out"; fi; }
while [ $# -gt 0 ]; do
  case "$1" in
    --home) HOME_DIR=$2; shift 2 ;;
    --machine) MACHINE=$2; shift 2 ;;
    --token-file) TOKEN_FILE=$2; shift 2 ;;
    --version) VERSION=$2; shift 2 ;;
    *) die "unknown option $1 (use --home, --machine, --token-file or --version)" ;;
  esac
done
[ -n "$HUBD_URL" ] || die "run the copy hubd serves: curl -fsSL '<hubd>/install?code=<code>' | sh"
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) die "unsupported OS $(uname -s)" ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "unsupported CPU $(uname -m)" ;; esac
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -q "$1" -O "$2"
  else die "needs curl or wget"; fi
}
sha() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  else die "needs sha256sum or shasum"; fi
}
umask 077
printf '\n  %shub%sridge%s installer %s· %s%s\n\n' "$B" "$A" "$R" "$D" "$HUBD_URL" "$R"
mkdir -p "$HOME_DIR/bin"
chmod 700 "$HOME_DIR"
name="hubridge-$os-$arch"
tmp="$HOME_DIR/bin/.download"
fetch "$HUBD_URL/install/checksums.txt" "$tmp.sums" || die "could not reach $HUBD_URL"
want=$(grep " $name\$" "$tmp.sums" | cut -d' ' -f1)
rm -f "$tmp.sums"
[ -n "$want" ] || die "hubd has no hubridge build for $os/$arch"
fetch "$HUBD_URL/install/files/$name" "$tmp" || die "could not download $name"
[ "$(sha "$tmp")" = "$want" ] || { rm -f "$tmp"; die "checksum mismatch for $name"; }
chmod 700 "$tmp"
size=$(( $(wc -c < "$tmp") / 1048576 ))
bin="$HOME_DIR/bin/hubridge"
[ -f "$bin" ] && mv -f "$bin" "$bin.prev"
mv -f "$tmp" "$bin"
ok "Installed hubd's bundled build for $os/$arch $D(${size} MB, checksum verified)$R"
set -- enroll --home "$HOME_DIR" --url "$HUBD_URL" --machine "$MACHINE" --code "$CODE" --token-file "$TOKEN_FILE"
if [ -n "$CODE" ]; then run "Enrolled with the install code" "$bin" "$@"; else run "Kept the installed token" "$bin" "$@"; fi
machine=${out#enrolled as }
machine=${machine%%;*}
if [ -n "$VERSION" ]; then
  run "Set the update channel to $VERSION" "$bin" update --home "$HOME_DIR" --version "$VERSION"
else
  try "Checked the saved channel for updates" "$bin" update --home "$HOME_DIR"
fi
run "Set up Claude Code (hooks and the hubd MCP server)" "$bin" setup --home "$HOME_DIR"
run "Started the client daemon" "$bin" supervise --detach --home "$HOME_DIR"
printf '\n  %sDone.%s %s%s%s is connected to hubd %s(hubridge %s, %s)%s\n\n' "$B" "$R" "$A" "$machine" "$R" "$D" "$("$bin" version --home "$HOME_DIR")" "$HOME_DIR" "$R"
