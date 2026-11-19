#!/bin/sh
# Materialize third_party/sing-box from the module cache, then overlay Resin's
# gated Reality ML-KEM patch (keep X25519MLKEM768 only when support_x25519mlkem768 is set).
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
VERSION="v1.12.21"
DEST="$ROOT/third_party/sing-box"
PATCH_DIR="$ROOT/patches/sing-box-${VERSION}"
MARKER="$DEST/.resin-mlkem-patched"

if [ -f "$MARKER" ] && [ -f "$DEST/option/tls.go" ] && grep -q SupportX25519MLKEM768 "$DEST/option/tls.go"; then
	echo "sing-box ${VERSION} already prepared at $DEST"
	exit 0
fi

if [ ! -f "$PATCH_DIR/option/tls.go" ] || [ ! -f "$PATCH_DIR/common/tls/reality_client.go" ]; then
	echo "missing patched sing-box sources in $PATCH_DIR" >&2
	exit 1
fi

GOMODCACHE="$(go env GOMODCACHE)"
SRC="${GOMODCACHE}/github.com/sagernet/sing-box@${VERSION}"

if [ ! -d "$SRC" ]; then
	echo "downloading github.com/sagernet/sing-box@${VERSION} into module cache"
	tmp="$(mktemp -d)"
	# Fetch in a throwaway module so Resin's replace directive is not applied.
	( cd "$tmp" && go mod init resin-singbox-fetch >/dev/null && go get "github.com/sagernet/sing-box@${VERSION}" )
	rm -rf "$tmp"
fi

if [ ! -d "$SRC" ]; then
	echo "failed to locate $SRC" >&2
	exit 1
fi

rm -rf "$DEST"
mkdir -p "$(dirname "$DEST")"
cp -a "$SRC" "$DEST"
chmod -R u+w "$DEST"

cp "$PATCH_DIR/option/tls.go" "$DEST/option/tls.go"
cp "$PATCH_DIR/common/tls/reality_client.go" "$DEST/common/tls/reality_client.go"

if ! grep -q SupportX25519MLKEM768 "$DEST/option/tls.go"; then
	echo "patched option/tls.go is missing SupportX25519MLKEM768" >&2
	exit 1
fi
if ! grep -q supportX25519MLKEM768 "$DEST/common/tls/reality_client.go"; then
	echo "patched reality_client.go is missing supportX25519MLKEM768" >&2
	exit 1
fi

echo "$VERSION" > "$MARKER"
echo "prepared patched sing-box at $DEST"
