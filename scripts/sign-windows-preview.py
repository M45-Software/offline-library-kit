#!/usr/bin/env python3
"""Sign a separate Windows preview folder locally; never publish or upload it."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
from urllib.parse import urlsplit


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def checked_hashes(folder, hashes):
    if not isinstance(hashes, dict) or not {'offline-library-core.exe', 'kiwix-serve.exe'} <= hashes.keys():
        raise ValueError('Missing reader or core hash')
    for name, expected in hashes.items():
        if not re.fullmatch(r'[A-Za-z0-9_.-]+', name) or not isinstance(expected, str) or not re.fullmatch(r'[a-f0-9]{64}', expected):
            raise ValueError('Invalid binary hash record')
        file = folder / name
        if file.is_symlink() or not file.is_file() or digest(file) != expected:
            raise ValueError('Input binary differs from its build record: ' + name)


def sign_copy(source, output, signtool, thumbprint, timestamp_url=None):
    if platform.system() != 'Windows':
        raise ValueError('Signing must run on Windows')
    if not re.fullmatch(r'[a-fA-F0-9]{40}', thumbprint):
        raise ValueError('Supply a 40-character certificate thumbprint')
    if timestamp_url:
        u = urlsplit(timestamp_url)
        if u.scheme != 'https' or not u.hostname or u.username or u.password or u.fragment:
            raise ValueError('Timestamp URL must be HTTPS without credentials or a fragment')
    signtool = signtool.resolve(strict=True)
    if signtool.name.lower() != 'signtool.exe' or not signtool.is_file():
        raise ValueError('Select signtool.exe from the Windows SDK')
    source = source.resolve(strict=True)
    output = output.expanduser().resolve()
    if output.is_relative_to(source) or source.is_relative_to(output):
        raise ValueError('Source and output must be separate folders')
    if output.exists():
        raise FileExistsError('Output already exists')
    info = json.loads((source / 'build-info.json').read_text(encoding='utf-8'))
    if info.get('target') != 'windows-amd64' or info.get('signing'):
        raise ValueError('Use an unsigned Windows preview as input')
    shell = source / 'Offline Library.exe'
    if shell.is_symlink() or not shell.is_file() or digest(shell) != info.get('shell_executable_sha256'):
        raise ValueError('Input app executable differs from its build record')
    checked_hashes(source / 'resources/bin', info.get('binary_sha256'))
    output.mkdir()  # Exclusive; never replace an existing folder.
    marker = output / 'SIGNING-INCOMPLETE'
    marker.write_text('Signing did not complete. Inspect this new folder before retrying.\n', encoding='utf-8')
    shutil.copytree(source, output, dirs_exist_ok=True, symlinks=True)
    signed_shell = output / 'Offline Library.exe'
    binary_dir = output / 'resources/bin'
    checked_hashes(binary_dir, info['binary_sha256'])
    if digest(signed_shell) != info['shell_executable_sha256']:
        raise ValueError('Copied app executable differs from its build record')
    for file in (signed_shell, binary_dir / 'offline-library-core.exe'):
        command = [str(signtool), 'sign', '/fd', 'SHA256', '/sha1', thumbprint]
        if timestamp_url:
            command += ['/tr', timestamp_url, '/td', 'SHA256']
        print(f'Signing {file.name}', flush=True)
        subprocess.run(command + [str(file)], check=True, timeout=120)
        print(f'Verifying {file.name}', flush=True)
        subprocess.run([str(signtool), 'verify', '/pa', str(file)], check=True, timeout=120)
    info['pre_signing_binary_sha256'] = info['binary_sha256']
    info['binary_sha256'] = {name: digest(binary_dir / name) for name in info['pre_signing_binary_sha256']}
    info['pre_signing_shell_sha256'] = info['shell_executable_sha256']
    info['shell_executable_sha256'] = digest(signed_shell)
    info['signing'] = {'kind': 'certificate', 'timestamped': bool(timestamp_url),
        'note': 'Local signing only; trust and reputation depend on the certificate. No binaries were published. The reader and runtime files retain their prior signatures.'}
    (output / 'build-info.json').write_text(json.dumps(info, indent=2) + '\n', encoding='utf-8')
    marker.unlink()
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('output', type=Path)
    parser.add_argument('--signtool', type=Path, required=True)
    parser.add_argument('--thumbprint', required=True)
    parser.add_argument('--timestamp-url', help='Optional HTTPS timestamp service; sends a digest to that service')
    args = parser.parse_args()
    try:
        result = sign_copy(args.source, args.output, args.signtool, args.thumbprint, args.timestamp_url)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        print(f'Signing failed: {exc}', file=sys.stderr)
        return 1
    print(f'Signed local copy: {result}\nReview source and licenses before redistribution.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
