#!/bin/sh
set -eu

TRANSLATION_DIR="${TRANSLATION_PATH:-/data/translations}"
DATA_DIR="${DATA_DIR:-/data}"
SEED_DIR="/app/seed-translations"

echo "=== MOESEKAI STARTUP ==="
echo "TRANSLATION_DIR: $TRANSLATION_DIR"
echo "DATA_DIR: $DATA_DIR"
echo "SEED_DIR: $SEED_DIR"
echo "SEED file count: $(find "$SEED_DIR" -type f 2>/dev/null | wc -l)"

mkdir -p "$TRANSLATION_DIR"
mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ] && [ ! -f "$TRANSLATION_DIR/cards.json" ]; then
  echo "Initializing data from seed..."
  if cp -R "$SEED_DIR"/* "$TRANSLATION_DIR"/ 2>/dev/null; then
    echo "Seed data copied successfully."
  elif (cd "$SEED_DIR" && tar cf - .) | (cd "$TRANSLATION_DIR" && tar xf -); then
    echo "Seed data copied via tar."
  else
    echo "ERROR: All copy methods failed!"
  fi
  # Also handle subdirectories (like eventStory/)
  if [ -d "$SEED_DIR/eventStory" ] && [ ! -d "$TRANSLATION_DIR/eventStory" ]; then
    cp -R "$SEED_DIR/eventStory" "$TRANSLATION_DIR/" 2>/dev/null || true
  fi
else
  echo "Data already initialized or no seed data."
fi

echo "DATA file count: $(find "$TRANSLATION_DIR" -type f 2>/dev/null | wc -l)"
echo "=== END STARTUP ==="

exec ./sekai-translate
