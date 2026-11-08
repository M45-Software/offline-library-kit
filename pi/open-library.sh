#!/bin/sh
url=${LIBRARY_URL:-http://127.0.0.1:8765/}
python3 - "$url" <<'PY'
import sys,time,urllib.request
for _ in range(60):
    try:
        with urllib.request.urlopen(sys.argv[1],timeout=1) as response:
            if response.status==200: break
    except OSError: pass
    time.sleep(1)
PY
exec chromium --start-maximized "$url"
