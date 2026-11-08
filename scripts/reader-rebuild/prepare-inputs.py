#!/usr/bin/env python3
"""Prepare captured inputs for the experimental Linux reader rebuild.

This prepares inputs only. It does not provide a complete toolchain, enforce
network isolation, run a build, or establish redistribution rights.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import tarfile

TOOLS = Path(__file__).resolve().parent
GIT_INPUTS = {
    'kiwix-build-8062f23fb42454c9e6db319abe71d2add7263de7.tar': 'tools',
    'libzim-0e95c45ec5ebd758784060111ea5a80a08c3f9c7.tar': 'SOURCE/libzim_release',
    'libkiwix-30adab733771835f25697e4f2ea18dc2eec4e92d.tar': 'SOURCE/libkiwix_release',
    'kiwix-tools-f76e38bdc06b86b73ba8e84a5de234a0d6c3fa3d.tar': 'SOURCE/kiwix-tools_release',
}
ARCHIVES = (
    'Mustache-4.1.tar.gz', 'curl-8.4.0.tar.xz', 'curl_8.4.0-2_patch.zip',
    'docopt_0.6.3-3_patch.zip', 'e2fsprogs-1.47.3.tar.xz',
    'icu4c-73_2-data.zip', 'icu4c-73_2-src.tgz', 'liblzma_5.2.6-3_patch.zip',
    'libmicrohttpd-0.9.76.tar.gz', 'libmicrohttpd_0.9.16-3_patch.zip',
    'pugixml-1.15.tar.gz', 'v0.6.3.tar.gz', 'xapian-core-1.4.23.tar.xz',
    'xz-5.2.6.tar.gz', 'zim-testing-suite-0.9.0.tar.gz', 'zlib-1.3.1.tar.gz',
    'zlib_1.3.1-1_patch.zip', 'zstd-1.5.7.tar.gz',
)
TEST_INPUTS = (
    'libzim_release/gtest-1.14.0.tar.gz', 'libzim_release/gtest_1.14.0-1_patch.zip',
    'libkiwix_release/gtest-1.8.1.zip', 'libkiwix_release/gtest-1.8.1-1-wrap.zip',
)

def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('candidates', type=Path, help='captured input folder containing SHA256SUMS')
    parser.add_argument('output', type=Path, help='new output path; must not already exist')
    args = parser.parse_args()
    candidates = args.candidates.resolve(strict=True)
    output = args.output.resolve()
    if output.is_relative_to(candidates) or candidates.is_relative_to(output):
        raise ValueError('Output must be separate from candidate inputs')
    expected = {}
    manifest = candidates / 'SHA256SUMS'
    for line in manifest.read_text(encoding='utf-8').splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  (.+)', line)
        if not match:
            raise ValueError('Invalid checksum manifest')
        checksum, name = match.groups()
        rel = Path(name)
        if rel.is_absolute() or '..' in rel.parts or name in expected:
            raise ValueError('Unsafe or duplicate manifest path')
        expected[name] = checksum
    names = ['git-sources/' + name for name in GIT_INPUTS]
    names += ['kiwix-archives/' + name for name in ARCHIVES]
    names += ['test-subprojects/' + name for name in TEST_INPUTS]
    lock = json.loads((TOOLS / 'input-lock.json').read_text(encoding='utf-8'))
    if lock.get('schema') != 1 or not isinstance(lock.get('files'), dict) or set(lock['files']) != set(names):
        raise ValueError('Invalid recorded input lock')
    for name in names:
        file = candidates / name
        record = lock['files'][name]
        if not isinstance(record, dict) or not re.fullmatch(r'[a-f0-9]{64}', record.get('sha256', '')) or not isinstance(record.get('size'), int) or record['size'] < 1:
            raise ValueError('Invalid recorded input: ' + name)
        if name not in expected or not file.is_file() or file.is_symlink() or not file.resolve().is_relative_to(candidates):
            raise ValueError('Missing or unsafe input: ' + name)
        if expected[name] != record['sha256'] or file.stat().st_size != record['size'] or digest(file) != record['sha256']:
            raise ValueError('Checksum mismatch: ' + name)
    output.mkdir(parents=True, exist_ok=False)
    (output / 'BUILD-INCOMPLETE').write_text('Input preparation incomplete; inspect this directory.\n')
    for d in ('ARCHIVE', 'SOURCE', 'tools', 'tmp'):
        (output / d).mkdir()
    for name in names:
        if name.startswith('kiwix-archives/'):
            shutil.copy2(candidates / name, output / 'ARCHIVE' / Path(name).name)
        elif name.startswith('test-subprojects/'):
            rel = Path(name).relative_to('test-subprojects')
            target = output / 'SOURCE' / rel.parts[0] / 'subprojects' / 'packagecache' / Path(*rel.parts[1:])
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(candidates / name, target)
    for name, dest in GIT_INPUTS.items():
        target = output / dest
        target.mkdir(parents=True, exist_ok=True)
        with tarfile.open(candidates / 'git-sources' / name) as archive:
            archive.extractall(target, filter='data')
    for name in ('offline-builder.py', 'link_warning_policy.py', 'optional-qmake'):
        shutil.copy2(TOOLS / name, output / name)
    qmake = output / 'optional-qmake'
    qmake.chmod(qmake.stat().st_mode | 0o111)
    (output / 'INPUT-SHA256SUMS').write_text(''.join(expected[name] + '  ' + name + '\n' for name in names), encoding='utf-8')
    (output / 'PREPARED.txt').write_text('Captured source inputs only. Host tools and libraries are not included.\nRun a build only in a network-isolated environment; see the evidence record.\nThese inputs are not a complete corresponding-source or license bundle.\n')
    (output / 'BUILD-INCOMPLETE').unlink()
    print('Prepared verified candidate inputs at', output)

if __name__ == '__main__':
    main()
