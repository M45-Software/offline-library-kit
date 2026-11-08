#!/usr/bin/env python3
"""Create Wi-Fi and library QR images; install the optional qrcode package first."""
import argparse
from pathlib import Path
from urllib.parse import urlsplit
try:
    import qrcode
except ImportError:
    raise SystemExit('Install the optional qrcode package with PNG support first.')

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--ssid', required=True)
p.add_argument('--password-file', type=Path, required=True)
p.add_argument('--url', required=True, help='Library URL as seen by connected clients')
p.add_argument('--output-dir', type=Path, required=True)
a = p.parse_args()
password = a.password_file.read_text().rstrip('\r\n')
if not 8 <= len(password) <= 63 or not a.ssid:
    raise SystemExit('SSID and an 8–63 character WPA password are required.')
if urlsplit(a.url).scheme not in ('http', 'https') or not urlsplit(a.url).hostname:
    raise SystemExit('A complete http(s) library URL is required.')
def escape(s):
    return ''.join('\\'+c if c in '\\;,:"' else c for c in s)
a.output_dir.mkdir(parents=True, exist_ok=True)
for name, payload in [('wifi-qr.png', 'WIFI:T:WPA;S:'+escape(a.ssid)+';P:'+escape(password)+';;'), ('site-qr.png', a.url)]:
    path = a.output_dir/name
    if path.exists(): raise SystemExit('Refusing to overwrite '+str(path))
    qrcode.make(payload).save(path)
    path.chmod(0o600)
    print('Created', path)
print('The Wi-Fi QR contains the network password; share it intentionally.')
