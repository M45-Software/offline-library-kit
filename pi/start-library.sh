#!/bin/sh
# Run on the Pi from a directory containing index.html and content/zim/*.zim.
set -eu
root=${LIBRARY_ROOT:-"$HOME/Offline-Library"}
kiwix=${KIWIX_SERVE:-kiwix-serve}
cd "$root"
mkdir -p "$HOME/.cache/offline-library"
python3 -m http.server 8765 --bind 0.0.0.0 --directory "$root" >"$HOME/.cache/offline-library/web.log" 2>&1 &
web=$!
set -- "$root"/content/zim/*.zim
if [ -s "$root/library.xml" ]; then
  "$kiwix" --address=ipv4 --port=8766 --blockexternal --library "$root/library.xml" >"$HOME/.cache/offline-library/kiwix.log" 2>&1 &
  reader=$!
elif [ -f "$1" ]; then
  "$kiwix" --address=ipv4 --port=8766 --blockexternal "$@" >"$HOME/.cache/offline-library/kiwix.log" 2>&1 &
  reader=$!
else
  reader=''
fi
trap 'kill "$web" ${reader:+"$reader"} 2>/dev/null || true' EXIT INT TERM
wait "$web"
