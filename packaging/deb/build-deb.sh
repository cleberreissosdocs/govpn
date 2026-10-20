#!/bin/bash
# build-deb.sh — monta o pacote .deb do govpn-client a partir do
# skeleton em packaging/deb/skeleton/ e de um binário já compilado.
#
# Uso (rodar a partir da raiz do repositório):
#   ./packaging/deb/build-deb.sh [versão] [arquitetura]
#
# Exemplos:
#   ./packaging/deb/build-deb.sh                 # versão 1.0.0, amd64
#   ./packaging/deb/build-deb.sh 1.2.0 arm64      # versão e arch customizadas
#
# Espera encontrar o binário em bin/govpn-client-linux-<arquitetura>
# (ou bin/govpn-client se a arquitetura for amd64 e esse for o único
# binário disponível). Builda com GOOS=linux se o binário ainda não
# existir.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SKELETON="$REPO_ROOT/packaging/deb/skeleton"
VERSION="${1:-1.0.0}"
ARCH="${2:-amd64}"

case "$ARCH" in
	amd64) GOARCH=amd64 ;;
	arm64) GOARCH=arm64 ;;
	armhf) GOARCH=arm ;;
	*) echo "Arquitetura não suportada: $ARCH (use amd64, arm64 ou armhf)" >&2; exit 1 ;;
esac

BIN_CANDIDATE="$REPO_ROOT/bin/govpn-client-linux-$ARCH"
if [ ! -f "$BIN_CANDIDATE" ] && [ "$ARCH" = "amd64" ] && [ -f "$REPO_ROOT/bin/govpn-client" ]; then
	BIN_CANDIDATE="$REPO_ROOT/bin/govpn-client"
fi
if [ ! -f "$BIN_CANDIDATE" ]; then
	echo "==> Binário não encontrado ($BIN_CANDIDATE), compilando com GOOS=linux GOARCH=$GOARCH"
	mkdir -p "$REPO_ROOT/bin"
	( cd "$REPO_ROOT" && GOOS=linux GOARCH=$GOARCH go build -o "bin/govpn-client-linux-$ARCH" ./client/ )
	BIN_CANDIDATE="$REPO_ROOT/bin/govpn-client-linux-$ARCH"
fi

echo "==> Montando pacote govpn-client ${VERSION} (${ARCH}) a partir de $BIN_CANDIDATE"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

cp -r "$SKELETON"/. "$BUILD_DIR/"
mkdir -p "$BUILD_DIR/usr/bin"
cp "$BIN_CANDIDATE" "$BUILD_DIR/usr/bin/govpn-client"

sed -i "s/VERSION_PLACEHOLDER/$VERSION/; s/ARCH_PLACEHOLDER/$ARCH/" "$BUILD_DIR/DEBIAN/control"

# permissões corretas (dpkg-deb é chato com isso)
find "$BUILD_DIR" -type d -exec chmod 755 {} \;
chmod 755 "$BUILD_DIR/usr/bin/govpn-client"
chmod 644 "$BUILD_DIR/etc/govpn/config.yml"
chmod 644 "$BUILD_DIR/lib/systemd/system/govpn-client.service"
chmod 755 "$BUILD_DIR/DEBIAN/postinst" "$BUILD_DIR/DEBIAN/prerm" "$BUILD_DIR/DEBIAN/postrm"
chmod 644 "$BUILD_DIR/DEBIAN/control" "$BUILD_DIR/DEBIAN/conffiles"

OUT="$REPO_ROOT/bin/govpn-client_${VERSION}_${ARCH}.deb"
mkdir -p "$REPO_ROOT/bin"
fakeroot dpkg-deb --build --root-owner-group "$BUILD_DIR" "$OUT"

echo "==> Pronto: $OUT"
dpkg-deb --info "$OUT" | sed -n '1,15p'
