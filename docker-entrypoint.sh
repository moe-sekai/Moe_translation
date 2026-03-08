#!/bin/sh
set -eu

TRANSLATION_DIR="${TRANSLATION_PATH:-/data/translations}"
DATA_DIR="${DATA_DIR:-/data}"
echo "=== MOESEKAI STARTUP ==="
echo "TRANSLATION_DIR: $TRANSLATION_DIR"
echo "DATA_DIR: $DATA_DIR"

mkdir -p "$TRANSLATION_DIR"
mkdir -p "$DATA_DIR"

echo "DATA file count: $(find "$TRANSLATION_DIR" -type f 2>/dev/null | wc -l)"
echo "=== END STARTUP ==="

exec ./sekai-translate
