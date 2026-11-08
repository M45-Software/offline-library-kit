#!/bin/sh
# Create a plain black image locally and apply it using the Pi desktop's file manager.
set -eu
background="$HOME/.local/share/offline-library/black-background.png"
mkdir -p "$(dirname "$background")"
python3 - "$background" <<'PY'
import struct, sys, zlib
from pathlib import Path

def chunk(kind, data):
    return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)

width, height = 1920, 1080
rows = (b'\x00' + b'\x00' * (width * 3)) * height
png = (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', width, height, 8, 2, 0, 0, 0))
       + chunk(b'IDAT', zlib.compress(rows)) + chunk(b'IEND', b''))
path = Path(sys.argv[1])
path.write_bytes(png)
PY
exec pcmanfm --set-wallpaper "$background"
