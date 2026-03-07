#!/bin/sh
set -eu

DATA_DIR="${TRANSLATION_PATH:-/data/translations}"
SEED_DIR="/app/translations"

mkdir -p "$DATA_DIR"

if [ -d "$SEED_DIR" ] && [ -z "$(ls -A "$DATA_DIR" 2>/dev/null)" ]; then
  cp -a "$SEED_DIR"/. "$DATA_DIR"/
fi

exec ./sekai-translate
