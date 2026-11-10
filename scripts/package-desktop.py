#!/usr/bin/env python3
"""Assemble a local desktop app folder from supplied Electron and Kiwix runtimes."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import struct
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
VERSION = '45.2.3'
TARGETS = {
    'linux-amd64': ('linux', 'amd64', 'electron', 'kiwix-serve'),
    'linux-arm64': ('linux', 'arm64', 'electron', 'kiwix-serve'),
    'darwin-amd64': ('darwin', 'amd64', 'Electron.app/Contents/MacOS/Electron', 'kiwix-serve'),
    'darwin-arm64': ('darwin', 'arm64', 'Electron.app/Contents/MacOS/Electron', 'kiwix-serve'),
    'windows-amd64': ('windows', 'amd64', 'electron.exe', 'kiwix-serve.exe'),
}
WINDOWS_RUNTIME = ('msvcp140.dll', 'vcruntime140.dll', 'vcruntime140_1.dll')


def binary_architecture(path):
    with path.open('rb') as f:
        h = f.read(4096)
    if h[:4] == b'\x7fELF' and len(h) >= 20:
        endian = '<' if h[5] == 1 else '>'
        return ('linux', {62: 'amd64', 183: 'arm64'}.get(struct.unpack(endian + 'H', h[18:20])[0]))
    if h[:2] == b'MZ' and len(h) >= 64:
        pos = struct.unpack('<I', h[60:64])[0]
        if pos + 6 <= len(h) and h[pos:pos+4] == b'PE\0\0':
            return ('windows', {0x8664: 'amd64', 0xaa64: 'arm64'}.get(struct.unpack('<H', h[pos+4:pos+6])[0]))
    if h[:4] == b'\xcf\xfa\xed\xfe' and len(h) >= 8:
        return ('darwin', {0x1000007: 'amd64', 0x100000c: 'arm64'}.get(struct.unpack('<I', h[4:8])[0]))
    return (None, None)


def check_binary(path, system, arch):
    if not path.is_file() or binary_architecture(path) != (system, arch):
        raise ValueError(f'{path.name} is not a {system}/{arch} executable')


def check_runtime_symlinks(root):
    for item in root.rglob('*'):
        if item.is_symlink() and not item.resolve().is_relative_to(root):
            raise ValueError('Electron runtime contains a link outside its folder: ' + item.name)


def sha256(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def reader_provenance(reader, target, reader_exe, system):
    """Check supplied provenance against reader bytes; do not infer authenticity."""
    metadata = reader / 'reader-provenance.json'
    if not metadata.exists():
        return {'status': 'not supplied'}
    if metadata.is_symlink() or not metadata.is_file() or metadata.stat().st_size > 65536:
        raise ValueError('Invalid reader provenance')
    try:
        data = json.loads(metadata.read_text(encoding='utf-8'))
        expected = data['reader_files_sha256']
        if data['schema'] != 1 or data['target'] != target or not isinstance(expected, dict):
            raise ValueError('Invalid reader provenance target')
        selected = {p.name: p for p in reader.iterdir() if p.name == reader_exe or
                    (system == 'windows' and p.is_file() and p.suffix.lower() == '.dll')}
        if reader_exe not in selected or set(expected) != set(selected):
            raise ValueError('Reader provenance does not cover supplied reader files')
        if any(not isinstance(expected[name], str) or sha256(file) != expected[name] for name, file in selected.items()):
            raise ValueError('Reader bytes do not match supplied provenance')
        if data.get('kind') is not None:
            raise ValueError('Invalid reader provenance kind')
        # Copy only recognized fields, never arbitrary paths from input metadata.
        fields = ('schema', 'target', 'archive_name', 'archive_url', 'publisher_checksum_url',
                  'archive_size_bytes', 'publisher_md5', 'local_sha256_baseline', 'reader_files_sha256', 'note')
        result = {key: data[key] for key in fields if key != 'note'}
        if not all(isinstance(result[key], str) for key in fields if key not in ('schema', 'archive_size_bytes', 'reader_files_sha256', 'note')):
            raise ValueError('Invalid reader provenance fields')
        name = result['archive_name']
        if (not re.fullmatch(r'[a-zA-Z0-9_.-]+', name) or
            result['archive_url'] not in ('https://download.kiwix.org/release/kiwix-tools/' + name,
                                         'https://ny.mirror.driftle.ss/kiwix/release/kiwix-tools/' + name) or
            result['publisher_checksum_url'] != 'https://download.kiwix.org/release/kiwix-tools/' + name + '.md5' or
            not re.fullmatch(r'[0-9a-f]{32}', result['publisher_md5']) or
            not re.fullmatch(r'[0-9a-f]{64}', result['local_sha256_baseline'])):
            raise ValueError('Invalid reader provenance source')
        if not isinstance(result['archive_size_bytes'], int) or result['archive_size_bytes'] <= 0:
            raise ValueError('Invalid reader provenance size')
        return {'status': 'supplied and file hashes checked', 'reported': result,
                'note': 'Source metadata is supplied by the input and is not an authenticity attestation; MD5 is not strong authenticity verification.'}
    except (KeyError, TypeError, json.JSONDecodeError) as exc:
        raise ValueError('Invalid reader provenance') from exc


def assemble(target, electron, reader, output, windows_runtime=None):
    system, arch, runtime_exe, reader_exe = TARGETS[target]
    electron, reader, output = electron.resolve(), reader.resolve(), output.resolve()
    if output.is_relative_to(electron) or output.is_relative_to(reader):
        raise ValueError('Output cannot be inside an input runtime directory')
    if not (electron / 'version').is_file() or (electron / 'version').read_text().strip() != VERSION:
        raise ValueError(f'Expected an extracted Electron {VERSION} distribution')
    check_binary(electron / runtime_exe, system, arch)
    check_binary(reader / reader_exe, system, arch)
    provenance = reader_provenance(reader, target, reader_exe, system)
    check_runtime_symlinks(electron)
    if not (electron / 'LICENSE').is_file() or not (electron / 'LICENSES.chromium.html').is_file():
        raise ValueError('Electron distribution is missing its license notices')
    runtime_files = []
    if system == 'windows':
        if windows_runtime is None:
            raise ValueError('Windows packaging requires the Microsoft C++ runtime DLLs')
        directory = windows_runtime.resolve()
        by_name = {p.name.lower(): p for p in directory.iterdir() if p.is_file()}
        missing = [name for name in WINDOWS_RUNTIME if name not in by_name]
        if missing:
            raise ValueError('Missing Microsoft runtime files: ' + ', '.join(missing))
        runtime_files = [by_name[name] for name in WINDOWS_RUNTIME]
        for f in runtime_files:
            check_binary(f, system, arch)
    output.mkdir(parents=True, exist_ok=False)
    marker = output / 'BUILD-INCOMPLETE'
    marker.write_text('Remove this directory and rebuild if assembly fails.\n')
    # Copy the supplied runtime without modifying the input or a prior build.
    shutil.copytree(electron, output, dirs_exist_ok=True, symlinks=True)
    if system == 'darwin':
        src = output / 'Electron.app'
        app = output / 'Offline Library.app'
        src.rename(app)
        resources = app / 'Contents/Resources'
        info = app / 'Contents/Info.plist'
        with info.open('rb') as f:
            plist = plistlib.load(f)
        plist['CFBundleName'] = 'Offline Library'
        plist['CFBundleDisplayName'] = 'Offline Library'
        plist['CFBundleIdentifier'] = 'org.offlinelibrarykit.desktop'
        with info.open('wb') as f:
            plistlib.dump(plist, f)
    else:
        resources = output / 'resources'
        (output / runtime_exe).rename(output / ('Offline Library.exe' if system == 'windows' else 'offline-library-bin'))
    if system == 'linux':
        launcher = output / 'start-with-user-namespace.sh'
        shutil.copy2(ROOT / 'desktop/shell/start-with-user-namespace.sh', launcher)
        launcher.chmod(launcher.stat().st_mode | 0o111)
    appdir = resources / 'app'
    if appdir.exists():
        raise ValueError('Electron distribution already contains an app folder')
    appdir.mkdir(parents=True)
    for name in ('package.json', 'main.cjs', 'preload.cjs', 'policy.cjs', 'library-selection.cjs'):
        shutil.copy2(ROOT / 'desktop/shell' / name, appdir / name)
    bindir = resources / 'bin'
    bindir.mkdir()
    shutil.copy2(reader / reader_exe, bindir / reader_exe)
    if system == 'windows':
        for f in reader.iterdir():
            if f.is_file() and f.suffix.lower() == '.dll':
                check_binary(f, system, arch)
                shutil.copy2(f, bindir / f.name)
        existing = {f.name.lower() for f in bindir.iterdir()}
        for f in runtime_files:
            if f.name.lower() in existing:
                raise ValueError('Reader and Microsoft runtime contain the same DLL: ' + f.name)
            shutil.copy2(f, bindir / f.name)
    core = bindir / ('offline-library-core.exe' if system == 'windows' else 'offline-library-core')
    env = {**os.environ, 'GOOS': system, 'GOARCH': arch, 'CGO_ENABLED': '0'}
    subprocess.run(['go', 'build', '-trimpath', '-o', str(core), '.'], cwd=ROOT / 'desktop', env=env, check=True)
    check_binary(core, system, arch)
    if system == 'linux':
        entry = output / 'offline-library'
        subprocess.run(['go', 'build', '-trimpath', '-o', str(entry), './cmd/linux-launcher'], cwd=ROOT / 'desktop', env=env, check=True)
        check_binary(entry, system, arch)
        entry.chmod(entry.stat().st_mode | 0o111)
        (output / 'offline-library-bin').chmod((output / 'offline-library-bin').stat().st_mode | 0o111)
        (output / 'START-HERE.txt').write_text('Open offline-library to start the app. Keep this entire folder together.\nThe launcher uses native sandboxing and can select a compatible, already-installed system sandbox helper.\nNo root permissions or changes to system sandbox settings are requested.\n')
    if system != 'windows':
        core.chmod(core.stat().st_mode | 0o111)
        (bindir / reader_exe).chmod((bindir / reader_exe).stat().st_mode | 0o111)
    hashes = {p.name: sha256(p) for p in sorted(bindir.iterdir()) if p.is_file()}
    if provenance['status'] != 'not supplied' and any(hashes.get(name) != digest for name, digest in provenance['reported']['reader_files_sha256'].items()):
        raise ValueError('Packaged reader differs from supplied provenance')
    build_info = {'target': target, 'electron': VERSION, 'reader_source': provenance, 'binary_sha256': hashes,
        'windows_runtime_source': 'supplied separately; origin and licensing not verified' if system == 'windows' else None,
        'note': 'Hashes are local build records, not publisher signatures.'}
    if system == 'windows':
        build_info['shell_executable_sha256'] = sha256(output / 'Offline Library.exe')
    (output / 'build-info.json').write_text(json.dumps(build_info, indent=2) + '\n')
    shutil.copy2(ROOT / 'LICENSE', output / 'LICENSE-offline-library.txt')
    shutil.copy2(ROOT / 'desktop/THIRD-PARTY.md', output / 'THIRD-PARTY.md')
    shutil.copytree(ROOT / 'desktop/licenses', output / 'THIRD-PARTY-LICENSES')
    if (ROOT / 'search-indexes').is_dir():
        shutil.copytree(ROOT / 'search-indexes', resources / 'search')
        indexes = {}
        for name in ('catalog', 'custom'):
            manifest = json.loads((resources / 'search' / name / 'manifest.json').read_text())
            indexes[name] = {'binary_bytes': manifest['Stats']['IndexBytes'],
                             'documents': manifest['Documents'],
                             'logical_sha256': manifest['IndexSHA256'],
                             'parts': len(manifest.get('Parts', []))}
        build_info['search_indexes'] = indexes
        (output / 'build-info.json').write_text(json.dumps(build_info, indent=2) + '\n')

    marker.unlink()
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', choices=TARGETS, required=True)
    parser.add_argument('--electron-dir', type=Path, required=True)
    parser.add_argument('--reader-dir', type=Path, required=True)
    parser.add_argument('--windows-runtime-dir', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    try:
        print(assemble(args.target, args.electron_dir, args.reader_dir, args.output, args.windows_runtime_dir))
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f'Package failed: {exc}', file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
