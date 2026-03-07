#!/bin/sh
set -eu

DATA_DIR="${TRANSLATION_PATH:-/data/translations}"
SEED_DIR="/app/translations"

mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ]; then
  # Use -n if busybox supports it, or just check for a file
  if [ ! -f "$DATA_DIR/cards.json" ]; then
    cp -a "$SEED_DIR"/. "$DATA_DIR"/
  fi
fi

exec ./sekai-translate
