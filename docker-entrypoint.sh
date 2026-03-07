#!/bin/sh
set -eu

DATA_DIR="${TRANSLATION_PATH:-/data/translations}"
SEED_DIR="/app/translations"

echo "=== MOESEKAI STARTUP DEBUG ==="
echo "DATA_DIR: $DATA_DIR"
echo "SEED_DIR: $SEED_DIR"

# Check filesystem info
echo "--- Filesystem info ---"
df -h "$DATA_DIR" 2>/dev/null || true
mount | grep -E "data|translations" || true

echo "--- SEED_DIR contents ---"
ls -la "$SEED_DIR" 2>/dev/null | head -20 || echo "SEED_DIR not accessible!"
echo "SEED_DIR file count: $(find "$SEED_DIR" -type f 2>/dev/null | wc -l)"

echo "--- DATA_DIR contents BEFORE ---"
ls -la "$DATA_DIR" 2>/dev/null || true

mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ]; then
  echo "SEED_DIR exists. Checking for cards.json..."
  if [ ! -f "$DATA_DIR/cards.json" ]; then
    echo "cards.json NOT found. Copying seed data via tar..."
    # Use tar pipe instead of cp -a for reliable cross-filesystem copy
    # BusyBox cp -a can silently fail across filesystem boundaries
    (cd "$SEED_DIR" && tar cf - .) | (cd "$DATA_DIR" && tar xf -)
    TAR_EXIT=$?
    if [ "$TAR_EXIT" -ne 0 ]; then
      echo "WARNING: tar copy failed with code $TAR_EXIT"
      echo "Falling back to cp -r..."
      cp -r "$SEED_DIR"/* "$DATA_DIR"/ 2>&1 || echo "WARNING: cp also failed with code $?"
    fi
    # Verify the copy actually worked
    if [ -f "$DATA_DIR/cards.json" ]; then
      echo "Copy VERIFIED: cards.json exists in DATA_DIR"
    else
      echo "ERROR: Copy appeared to succeed but cards.json still missing!"
      echo "Attempting direct file test..."
      touch "$DATA_DIR/.write_test" 2>&1 && rm -f "$DATA_DIR/.write_test" && echo "Volume IS writable" || echo "Volume is NOT writable!"
    fi
  else
    echo "cards.json ALREADY EXISTS. Skipping copy."
  fi
else
  echo "SEED_DIR does NOT exist!"
fi

echo "--- DATA_DIR contents AFTER ---"
ls -la "$DATA_DIR" 2>/dev/null | head -20 || true
echo "DATA_DIR file count: $(find "$DATA_DIR" -type f 2>/dev/null | wc -l)"
echo "=== END DEBUG ==="

exec ./sekai-translate
