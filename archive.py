#!/usr/bin/env python3
"""Download verified files to fast staging storage and publish sequentially."""
import argparse
import concurrent.futures
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import uuid

BLOCK = 8 * 1024 * 1024
MARKER = '.offline-library-target.json'


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        while data := f.read(BLOCK):
            h.update(data)
    return h.hexdigest()


def load_manifest(path):
    items = json.loads(Path(path).read_text())['items']
    seen = set()
    for item in items:
        name = item['name']
        if not isinstance(name, str) or not name or name in ('.', '..') or Path(name).name != name or '/' in name or '\\' in name:
            raise ValueError('Each name must be a plain filename')
        if name in seen:
            raise ValueError('Duplicate filename: ' + name)
        seen.add(name)
        h = item['sha256']
        if len(h) != 64 or any(x not in '0123456789abcdef' for x in h):
            raise ValueError('A lowercase SHA-256 is required for ' + name)
        if not isinstance(item['bytes'], int) or isinstance(item['bytes'], bool) or item['bytes'] <= 0:
            raise ValueError('A positive expected byte size is required for ' + name)
        if not item['url'].startswith('https://'):
            raise ValueError('Use an HTTPS publisher URL for ' + name)
    return items


def matches(path, item):
    return path.is_file() and path.stat().st_size == item['bytes'] and digest(path) == item['sha256']


def download_one(item, stage, reserve):
    final = stage / item['name']
    part = stage / (item['name'] + '.part')
    if final.exists():
        if not matches(final, item):
            raise RuntimeError('Existing staged file failed verification: ' + str(final))
        return 'Already verified: ' + item['name']
    if shutil.disk_usage(stage).free < item['bytes'] + reserve:
        raise RuntimeError('Insufficient staging space for ' + item['name'])
    subprocess.run(['curl', '--fail', '--location', '--show-error', '--retry', '5',
                    '--continue-at', '-', '--output', str(part), item['url']], check=True)
    if not matches(part, item):
        raise RuntimeError('Download failed size or SHA-256 check; partial retained: ' + str(part))
    os.replace(part, final)
    return 'Verified: ' + item['name']


def init_target(target):
    if not target.is_mount():
        raise ValueError('Target must be the root of a mounted filesystem')
    marker = target / MARKER
    if marker.exists():
        raise ValueError('Target already initialized; inspect its marker')
    record = {'id': str(uuid.uuid4()), 'purpose': 'offline-library-target'}
    with marker.open('x') as f:
        json.dump(record, f, indent=2); f.write('\n'); f.flush(); os.fsync(f.fileno())
    return record['id']


def target_ok(target, target_id):
    if not target.is_mount():
        raise ValueError('Target is not a mounted filesystem')
    record = json.loads((target / MARKER).read_text())
    if record.get('purpose') != 'offline-library-target' or record.get('id') != target_id:
        raise ValueError('Target marker does not match the supplied ID')


def publish(items, stage, target, target_id, reserve):
    # One writer across processes sharing this staging directory.
    with (stage / '.publish.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        target_ok(target, target_id)
        dest = target / 'content' / 'zim'
        dest.mkdir(parents=True, exist_ok=True)
        for item in items:
            target_ok(target, target_id)
            source, final = stage / item['name'], dest / item['name']
            part = dest / (item['name'] + '.part')
            if not matches(source, item):
                raise RuntimeError('Staged source failed verification: ' + str(source))
            if final.exists():
                if not matches(final, item):
                    raise RuntimeError('Existing destination failed verification: ' + str(final))
                print('Already verified:', item['name'], flush=True)
                continue
            if part.exists():
                raise RuntimeError('Previous partial retained; inspect it first: ' + str(part))
            if shutil.disk_usage(target).free < item['bytes'] + reserve:
                raise RuntimeError('Insufficient destination space for ' + item['name'])
            print('Copying:', item['name'], flush=True)
            with source.open('rb') as src, part.open('xb') as out:
                shutil.copyfileobj(src, out, BLOCK); out.flush(); os.fsync(out.fileno())
            target_ok(target, target_id)
            if not matches(part, item):
                raise RuntimeError('Destination verification failed; partial retained: ' + str(part))
            os.replace(part, final)
            print('Verified:', item['name'], flush=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest='command', required=True)
    a = sub.add_parser('init-target'); a.add_argument('target', type=Path)
    a = sub.add_parser('download'); a.add_argument('manifest', type=Path); a.add_argument('--stage', type=Path, required=True); a.add_argument('--jobs', type=int, default=2); a.add_argument('--reserve-gib', type=float, default=10)
    a = sub.add_parser('publish'); a.add_argument('manifest', type=Path); a.add_argument('--stage', type=Path, required=True); a.add_argument('--target', type=Path, required=True); a.add_argument('--target-id', required=True); a.add_argument('--reserve-gib', type=float, default=10)
    a = p.parse_args()
    try:
        if a.command == 'init-target':
            print('Target ID:', init_target(a.target.resolve())); return
        if a.reserve_gib < 0: raise ValueError('Reserve must not be negative')
        items = load_manifest(a.manifest)
        stage = a.stage.resolve()
        if a.command == 'download':
            if not 1 <= a.jobs <= 8: raise ValueError('Jobs must be between 1 and 8')
            stage.mkdir(parents=True, exist_ok=True)
            # Reserve the worst-case space before starting concurrent downloads.
            required = sum(x['bytes'] for x in items if not (stage / x['name']).exists())
            reserve = int(a.reserve_gib * 1024**3)
            if shutil.disk_usage(stage).free < required + reserve: raise ValueError('Insufficient space for complete download batch and reserve')
            with (stage / '.download.lock').open('a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                with concurrent.futures.ThreadPoolExecutor(max_workers=a.jobs) as pool:
                    for result in pool.map(lambda x: download_one(x, stage, reserve), items): print(result, flush=True)
        else:
            publish(items, stage, a.target.resolve(), a.target_id, int(a.reserve_gib * 1024**3))
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as e:
        print('Error:', e, file=sys.stderr); sys.exit(1)
    except RuntimeError as e:
        print('Error:', e, file=sys.stderr); sys.exit(1)

if __name__ == '__main__': main()
