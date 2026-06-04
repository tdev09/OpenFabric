#!/bin/bash
set -e

# OpenFabric Linux single-binary installer
echo "Installing OpenFabric for Linux..."

REPO="openfabric/openfabric"
LATEST_TAG=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_TAG" ]; then
    LATEST_TAG="v0.1.0"
    echo "Warning: could not fetch latest release tag, using fallback $LATEST_TAG"
fi

# Detect architecture
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    BINARY_NAME="fabric-linux-amd64"
elif [[ "$ARCH" == "arm"* || "$ARCH" == "aarch64" ]]; then
    BINARY_NAME="fabric-linux-arm64"
else
    echo "Unsupported architecture: $ARCH"
    exit 1
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

# Setup Systemd Service
echo "Configuring Systemd service..."
SERVICE_FILE="/etc/systemd/system/openfabric.service"

sudo bash -c "cat > $SERVICE_FILE" <<EOF
[Unit]
Description=OpenFabric Agent Daemon
After=network.target

[Service]
ExecStart=$INSTALL_PATH --port 4892
Restart=always
RestartSec=5
User=$USER
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=multi-user.target
EOF

echo "Reloading systemd, enabling and starting openfabric service..."
sudo systemctl daemon-reload
sudo systemctl enable openfabric
sudo systemctl restart openfabric

echo "OpenFabric successfully installed as a systemd service!"
echo "Status: sudo systemctl status openfabric"
