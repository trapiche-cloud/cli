#!/bin/bash
set -e

REPO="trapiche/trapiche"
BIN_NAME="trapiche"
INSTALL_DIR="/usr/local/bin"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

ASSET="${BIN_NAME}_${OS}_${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
DEST="${INSTALL_DIR}/${BIN_NAME}"

echo "Installing trapiche CLI..."
echo "  Platform : ${OS}/${ARCH}"
echo "  Source   : ${URL}"
echo "  Target   : ${DEST}"
echo ""

if ! curl -fsSL "$URL" -o "$DEST"; then
  echo "Download failed. Check that the release exists:"
  echo "  https://github.com/${REPO}/releases/latest"
  exit 1
fi

chmod +x "$DEST"

echo "Done! Verify with:"
echo "  trapiche --help"
