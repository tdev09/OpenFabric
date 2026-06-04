#!/bin/bash
set -e

# OpenFabric macOS single-binary installer
echo "Installing OpenFabric for macOS..."

REPO="tdev09/OpenFabric"
# Fetch latest release tag from github API
LATEST_TAG=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_TAG" ]; then
    LATEST_TAG="v0.1.0"
    echo "Warning: could not fetch latest release tag, using fallback $LATEST_TAG"
fi

# Detect architecture
ARCH=$(uname -m)
if [ "$ARCH" = "arm64" ]; then
    BINARY_NAME="fabric-darwin-arm64"
else
    BINARY_NAME="fabric-darwin-amd64"
fi

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST_TAG/$BINARY_NAME"
INSTALL_PATH="/usr/local/bin/fabric"

echo "Downloading OpenFabric $LATEST_TAG ($ARCH)..."
curl -s -L -o /tmp/fabric "$DOWNLOAD_URL" || {
    echo "Error: Failed to download binary from $DOWNLOAD_URL"
    exit 1
}

echo "Installing binary to $INSTALL_PATH..."
sudo mv /tmp/fabric "$INSTALL_PATH"
sudo chmod +x "$INSTALL_PATH"

echo "OpenFabric successfully installed to $INSTALL_PATH"
echo "You can start OpenFabric by running 'fabric'"
