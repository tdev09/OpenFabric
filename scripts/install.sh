#!/bin/sh
# install.sh - One-liner installer for Linux / Raspberry Pi.
# Usage: curl -fsSL https://raw.githubusercontent.com/tdev09/OpenFabric/main/scripts/install.sh | sh
set -e

REPO="tdev09/OpenFabric"
INSTALL_DIR="/usr/local/bin"
BINARY="fabric"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64)  ARCH="amd64"  ;;
  aarch64) ARCH="arm64"  ;;
  armv7l)  ARCH="armv7"  ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$VERSION" ]; then
  echo "Could not determine latest version"
  exit 1
fi

URL="https://github.com/$REPO/releases/download/$VERSION/${BINARY}-${OS}-${ARCH}"
echo "⬇  Downloading OpenFabric $VERSION for ${OS}/${ARCH}…"
curl -fsSL "$URL" -o /tmp/fabric
chmod +x /tmp/fabric

if [ -w "$INSTALL_DIR" ]; then
  mv /tmp/fabric "$INSTALL_DIR/$BINARY"
else
  sudo mv /tmp/fabric "$INSTALL_DIR/$BINARY"
fi

echo "✅ OpenFabric installed at $INSTALL_DIR/$BINARY"
echo "Run: fabric start"
