#!/usr/bin/env python3
"""Fetch small, checked upstream inputs for desktop smoke tests, not distribution."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import urllib.request
import zipfile

# SHA-256 values are local baselines recorded after a download over HTTPS
# matched the publisher's MD5; they are not publisher-provided signatures.
READERS = {
    'linux-amd64': ('kiwix-tools_linux-x86_64-3.8.2.tar.gz', 21661067, 'b0ae98dd344aa0469a15ab42feff6d5aafb79541a82fb4e2647c74b073123815'),
    'darwin-amd64': ('kiwix-tools_macos-x86_64-3.8.2.tar.gz', 10817537, 'ae341280e97ad5902e7a4282a24340eac7d7061c267661c0ef2a88a34547f247'),
    'darwin-arm64': ('kiwix-tools_macos-arm64-3.8.2.tar.gz', 10283222, '5c64d43176627e558a117146b02ea36b7da2b0cd3a332cbff075cde05d9585f9'),
    'windows-amd64': ('kiwix-tools_win-x86_64-3.8.1.zip', 18301924, 'fcd01ed2b93e9a68632c7863c83b9f66bf64406a66357be1df7b8b75596f3e45'),
}
BASE = 'https://download.kiwix.org/release/kiwix-tools/'
FIXTURE = 'https://download.kiwix.org/zim/devdocs/devdocs_en_vue_2026-08.zim'
MIRROR = 'https://ny.mirror.driftle.ss/kiwix/'
FIXTURE_HASH = '33d5231e492d05a47cc844afcb13540d61ff8acc938329e1118ea264fd8de70b'
FIXTURE_SIZE = 975797
COLLISION_FIXTURE = 'https://download.kiwix.org/zim/devdocs/devdocs_en_vue-router_2026-10.zim'
COLLISION_FIXTURE_HASH = '9d70f2114301b63cde23acd9c88bf469e429671972132145ea67fe54cc2cc1e7'
COLLISION_FIXTURE_SIZE = 524042


def get(url, destination, limit):
    with destination.open('xb') as f:
        try:
            with urllib.request.urlopen(url, timeout=120) as response:
                total = 0
                while True:
                    block = response.read(min(1024 * 1024, limit - total + 1))
                    if not block:
                        break
                    total += len(block)
                    if total > limit:
                        raise ValueError('Test input exceeds its expected size')
                    f.write(block)
        except BaseException:
            f.close()
            destination.unlink(missing_ok=True)
            raise


def get_with_fallback(urls, destination, limit):
    errors = []
    for url in urls:
        try:
            get(url, destination, limit)
            return url
        except FileExistsError:
            raise
        except (OSError, ValueError) as exc:
            errors.append(str(exc))
    raise RuntimeError('Test input unavailable: ' + '; '.join(errors))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--target', choices=READERS, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--reader-only', action='store_true', help='Fetch the reader without a ZIM test fixture')
    a = p.parse_args()
    a.output.mkdir(parents=True, exist_ok=False)
    reader = a.output / 'reader'
    reader.mkdir()
    name, size, local_sha256 = READERS[a.target]
    with tempfile.TemporaryDirectory() as tmp:
        archive = Path(tmp) / name
        download_url = get_with_fallback([MIRROR + 'release/kiwix-tools/' + name, BASE + name], archive, size)
        with urllib.request.urlopen(BASE + name + '.md5', timeout=30) as response:
            checksum_line = response.read(1024).decode('ascii').strip().split()
        if len(checksum_line) != 2 or checksum_line[1].lstrip('*') != name:
            raise ValueError('Unexpected Kiwix checksum metadata')
        digest = hashlib.md5(usedforsecurity=False)
        with archive.open('rb') as f:
            for data in iter(lambda: f.read(1024 * 1024), b''):
                digest.update(data)
        if digest.hexdigest() != checksum_line[0].lower():
            raise ValueError('Kiwix archive does not match its publisher MD5')
        with archive.open('rb') as f:
            if archive.stat().st_size != size or hashlib.file_digest(f, 'sha256').hexdigest() != local_sha256:
                raise ValueError('Kiwix archive differs from the recorded local SHA-256 baseline')
        if name.endswith('.zip'):
            with zipfile.ZipFile(archive) as z:
                for member in z.infolist():
                    base = Path(member.filename).name
                    if base.lower() == 'kiwix-serve.exe' or base.lower().endswith('.dll'):
                        with z.open(member) as src, (reader / base).open('xb') as dst:
                            shutil.copyfileobj(src, dst)
        else:
            with tarfile.open(archive, 'r:gz') as t:
                matches = [m for m in t.getmembers() if Path(m.name).name == 'kiwix-serve' and m.isfile()]
                if len(matches) != 1:
                    raise ValueError('Expected exactly one Kiwix reader')
                with t.extractfile(matches[0]) as src, (reader / 'kiwix-serve').open('xb') as dst:
                    shutil.copyfileobj(src, dst)
                os.chmod(reader / 'kiwix-serve', 0o755)
        files = {}
        for file in sorted(reader.iterdir()):
            if file.is_file():
                with file.open('rb') as f:
                    files[file.name] = hashlib.file_digest(f, 'sha256').hexdigest()
        provenance = {
            'schema': 1, 'target': a.target, 'archive_name': name,
            'archive_url': download_url, 'publisher_checksum_url': BASE + name + '.md5',
            'archive_size_bytes': size, 'publisher_md5': checksum_line[0].lower(),
            'local_sha256_baseline': local_sha256, 'reader_files_sha256': files,
            'note': 'SHA-256 values are local baselines, not publisher signatures. MD5 is not strong authenticity verification.',
        }
        (reader / 'reader-provenance.json').write_text(json.dumps(provenance, indent=2) + '\n', encoding='utf-8')
    if a.reader_only:
        print('Fetched checked reader:', a.output)
        return
    fixture = a.output / 'fixture.zim'
    get_with_fallback([MIRROR + 'zim/devdocs/devdocs_en_vue_2026-08.zim', FIXTURE], fixture, FIXTURE_SIZE)
    with fixture.open('rb') as f:
        if fixture.stat().st_size != FIXTURE_SIZE or hashlib.file_digest(f, 'sha256').hexdigest() != FIXTURE_HASH:
            raise ValueError('Fixture does not match the publisher SHA-256')
    collision_fixture = a.output / 'collision-fixture.zim'
    get_with_fallback([MIRROR + 'zim/devdocs/devdocs_en_vue-router_2026-10.zim', COLLISION_FIXTURE], collision_fixture, COLLISION_FIXTURE_SIZE)
    with collision_fixture.open('rb') as f:
        if collision_fixture.stat().st_size != COLLISION_FIXTURE_SIZE or hashlib.file_digest(f, 'sha256').hexdigest() != COLLISION_FIXTURE_HASH:
            raise ValueError('Collision fixture does not match the publisher SHA-256')
    print('Fetched checked test inputs:', a.output)


if __name__ == '__main__':
    main()
