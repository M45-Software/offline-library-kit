#!/usr/bin/env python3
"""Record supplied source-build details for a local Linux reader preview.

This records claims and hashes; it does not prove the binary was built from the
specified inputs or establish complete corresponding source or licensing.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re


def sha256(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError('Input must be a regular non-symlink file: ' + path.name)
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--reader-dir', type=Path, required=True)
    p.add_argument('--input-lock', type=Path, required=True)
    p.add_argument('--build-adapter', type=Path, required=True)
    p.add_argument('--warning-policy', type=Path, required=True)
    p.add_argument('--kiwix-build-commit', required=True)
    args = p.parse_args()
    if not re.fullmatch(r'[0-9a-f]{40}', args.kiwix_build_commit):
        p.error('kiwix-build commit must be a lowercase 40-character Git hash')
    directory = args.reader_dir.resolve(strict=True)
    if not directory.is_dir():
        p.error('reader directory must exist')
    record = {'schema': 1, 'kind': 'source-build', 'target': 'linux-amd64',
              'reader_files_sha256': {'kiwix-serve': sha256(directory / 'kiwix-serve')},
              'kiwix_build_commit': args.kiwix_build_commit,
              'input_lock_sha256': sha256(args.input_lock),
              'build_adapter_sha256': sha256(args.build_adapter),
              'warning_policy_sha256': sha256(args.warning_policy)}
    target = directory / 'reader-provenance.json'
    with target.open('x', encoding='utf-8') as stream:
        json.dump(record, stream, indent=2)
        stream.write('\n')
    print('Recorded supplied source-build details:', target)


if __name__ == '__main__':
    main()
