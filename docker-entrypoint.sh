#!/bin/sh
set -eu

DATA_DIR="${TRANSLATION_PATH:-/data/translations}"
SEED_DIR="/app/seed-translations"

echo "=== MOESEKAI STARTUP ==="
echo "DATA_DIR: $DATA_DIR"
echo "SEED_DIR: $SEED_DIR"
echo "SEED file count: $(find "$SEED_DIR" -type f 2>/dev/null | wc -l)"

mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ] && [ ! -f "$DATA_DIR/cards.json" ]; then
  echo "Initializing data from seed..."
  if cp -R "$SEED_DIR"/* "$DATA_DIR"/ 2>/dev/null; then
    echo "Seed data copied successfully."
  elif (cd "$SEED_DIR" && tar cf - .) | (cd "$DATA_DIR" && tar xf -); then
    echo "Seed data copied via tar."
  else
    echo "ERROR: All copy methods failed!"
  fi
  # Also handle subdirectories (like eventStory/)
  if [ -d "$SEED_DIR/eventStory" ] && [ ! -d "$DATA_DIR/eventStory" ]; then
    cp -R "$SEED_DIR/eventStory" "$DATA_DIR/" 2>/dev/null || true
  fi
else
  echo "Data already initialized or no seed data."
fi

echo "DATA file count: $(find "$DATA_DIR" -type f 2>/dev/null | wc -l)"
echo "=== END STARTUP ==="

exec ./sekai-translate
