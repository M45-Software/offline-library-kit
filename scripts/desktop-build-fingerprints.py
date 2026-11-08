#!/usr/bin/env python3
"""Print hashes of selected app components for local/native build comparison."""
import argparse
import hashlib
import json
from pathlib import Path


def fingerprint(folder):
    folder = folder.resolve(strict=True)
    info = json.loads((folder / 'build-info.json').read_text(encoding='utf-8'))
    target = info['target']
    if target.startswith('darwin-'):
        app = folder / 'Offline Library.app' / 'Contents'
        resources = app / 'Resources'
        shell = app / 'MacOS' / 'Electron'
    elif target == 'windows-amd64':
        resources, shell = folder / 'resources', folder / 'Offline Library.exe'
    elif target.startswith('linux-'):
        resources, shell = folder / 'resources', folder / 'offline-library'
    else:
        raise ValueError('Unknown target')
    paths = {'shell': shell}
    if target.startswith('linux-') and (folder / 'offline-library-bin').is_file():
        paths['electron-runtime'] = folder / 'offline-library-bin'
    paths.update({'app/' + name: resources / 'app' / name for name in
                  ('package.json', 'main.cjs', 'preload.cjs', 'policy.cjs', 'library-selection.cjs')})
    paths.update({'bin/' + name: resources / 'bin' / name for name in info['binary_sha256']})
    hashes = {}
    for label, path in paths.items():
        with path.open('rb') as stream:
            hashes[label] = hashlib.file_digest(stream, 'sha256').hexdigest()
    for name, expected in info['binary_sha256'].items():
        if hashes['bin/' + name] != expected:
            raise ValueError('Binary differs from build record: ' + name)
    return {'target': target, 'sha256': hashes}


def folder_fingerprint(folder):
    folder = folder.resolve(strict=True)
    entries = {}
    for path in sorted(folder.rglob('*')):
        name = path.relative_to(folder).as_posix()
        if name in {'build-info.json', 'START-HERE.txt', 'component-comparison.json', 'folder-comparison.json'}:
            continue
        if path.is_symlink():
            entries[name] = {'link': str(path.readlink())}
        elif path.is_file():
            with path.open('rb') as stream:
                entries[name] = {'sha256': hashlib.file_digest(stream, 'sha256').hexdigest()}
    return {'target': json.loads((folder / 'build-info.json').read_text(encoding='utf-8'))['target'], 'entries': entries}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('folder', type=Path)
    parser.add_argument('--all-files', action='store_true')
    args = parser.parse_args()
    print('BUILD_FINGERPRINTS=' + json.dumps(fingerprint(args.folder), sort_keys=True))
    if args.all_files:
        print('FOLDER_FINGERPRINTS=' + json.dumps(folder_fingerprint(args.folder), sort_keys=True))


if __name__ == '__main__':
    main()
