#!/bin/sh
set -eu

DATA_DIR="${TRANSLATION_PATH:-/data/translations}"
SEED_DIR="/app/translations"

echo "=== MOESEKAI STARTUP DEBUG ==="
echo "DATA_DIR: $DATA_DIR"
echo "SEED_DIR: $SEED_DIR"
echo "DATA_DIR contents BEFORE:"
ls -la "$DATA_DIR" || true

mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ]; then
  echo "SEED_DIR exists. Checking for cards.json..."
  if [ ! -f "$DATA_DIR/cards.json" ]; then
    echo "cards.json NOT found. Copying seed data..."
    # We use cp -a . to copy all contents including hidden files (if any) and it avoids shell glob expansion issues
    cp -a "$SEED_DIR"/. "$DATA_DIR"/ || echo "WARNING: cp command failed with code $?"
    echo "Copy completed!"
  else
    echo "cards.json ALREADY EXISTS. Skipping copy."
  fi
else
  echo "SEED_DIR does NOT exist!"
fi

echo "DATA_DIR contents AFTER:"
ls -la "$DATA_DIR" || true
echo "=== END DEBUG ==="

exec ./sekai-translate
