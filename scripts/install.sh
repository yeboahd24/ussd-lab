#!/usr/bin/env sh
set -e

# USSD Lab installer
# Usage: curl -fsSL https://raw.githubusercontent.com/yeboahd24/ussd-lab/main/scripts/install.sh | sh

REPO="yeboahd24/ussd-lab"
BIN_NAME="ussd"
INSTALL_DIR="${USSD_INSTALL_DIR:-$HOME/.local/bin}"

# --- detect version ---------------------------------------------------------
VERSION="${USSD_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
fi
VERSION="${VERSION#v}"
echo "Installing ${BIN_NAME} v${VERSION}"

# --- detect os / arch -------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  mingw*|msys*|cygwin*) OS="windows" ;;
  *)
    echo "error: unsupported OS: $OS" >&2
    exit 1
    ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

TARGET="${OS}-${ARCH}"
echo "Detected platform: ${TARGET}"

# --- download ---------------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ "$OS" = "windows" ]; then
  FILE="ussd-${TARGET}.zip"
  URL="https://github.com/${REPO}/releases/download/v${VERSION}/${FILE}"
  echo "Downloading ${URL}"
  curl -fsSL "$URL" -o "$TMP/$FILE"
  unzip -o "$TMP/$FILE" -d "$TMP" >/dev/null
  BIN="$TMP/ussd-${TARGET}.exe"
else
  FILE="ussd-${TARGET}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/v${VERSION}/${FILE}"
  echo "Downloading ${URL}"
  curl -fsSL "$URL" -o "$TMP/$FILE"
  tar -xzf "$TMP/$FILE" -C "$TMP"
  BIN="$TMP/ussd-${TARGET}"
fi

# --- install ----------------------------------------------------------------
mkdir -p "$INSTALL_DIR"
install -m 0755 "$BIN" "$INSTALL_DIR/$BIN_NAME"

echo
echo "Installed ${BIN_NAME} to ${INSTALL_DIR}/${BIN_NAME}"
echo

case "$SHELL" in
  *zsh) PROFILE="$HOME/.zshrc" ;;
  *bash) PROFILE="$HOME/.bashrc" ;;
  *fish) PROFILE="$HOME/.config/fish/config.fish" ;;
  *) PROFILE="" ;;
esac

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    if [ -n "$PROFILE" ]; then
      echo "NOTE: ${INSTALL_DIR} is not on your PATH."
      echo "      Add this line to ${PROFILE}:"
      echo "        export PATH=\"${INSTALL_DIR}:\$PATH\""
    else
      echo "NOTE: ${INSTALL_DIR} is not on your PATH. Add it to your shell profile."
    fi
    ;;
esac

echo
echo "Run '${BIN_NAME} --help' to get started."
