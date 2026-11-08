#!/usr/bin/env python3
"""Build a local, unsigned desktop preview without downloading library content."""
import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('linux-amd64', 'windows-amd64', 'darwin-arm64', 'darwin-amd64')


def native_target(system=None, machine=None):
    system = (system or platform.system()).lower()
    machine = (machine or platform.machine()).lower()
    systems = {'linux': 'linux', 'windows': 'windows', 'darwin': 'darwin'}
    machines = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}
    candidate = systems.get(system, '') + '-' + machines.get(machine, '')
    return candidate if candidate in TARGETS else None


def build(target, output, windows_runtime=None, reader_dir=None, electron_dir=None):
    output = output.expanduser().resolve()
    if output.exists():
        raise FileExistsError(f'Output already exists: {output}')
    if reader_dir is not None:
        reader_dir = reader_dir.expanduser().resolve(strict=True)
        if not reader_dir.is_dir():
            raise ValueError('Reader input must be a directory')
    if electron_dir is not None:
        electron_dir = electron_dir.expanduser().resolve(strict=True)
        if not electron_dir.is_dir():
            raise ValueError('Electron input must be a directory')
    for executable in (('go', 'node', 'npm') if electron_dir is None else ('go',)):
        if not shutil.which(executable):
            raise RuntimeError(f'Build prerequisite not found: {executable}')
    if target == 'windows-amd64' and windows_runtime is None:
        if native_target() != target or not os.environ.get('SystemRoot'):
            raise ValueError('For a Windows build, supply --windows-runtime-dir')
        windows_runtime = Path(os.environ['SystemRoot']) / 'System32'
    # Inputs are temporary build tools; the resulting app has no Node or Go
    # installation requirement and contains no ZIM archives.
    with tempfile.TemporaryDirectory(prefix='offline-library-build-') as tmp:
        work = Path(tmp)
        if electron_dir is None:
            shell = work / 'shell'
            shell.mkdir()
            for name in ('package.json', 'package-lock.json'):
                shutil.copy2(ROOT / 'desktop' / 'shell' / name, shell / name)
            print('Installing temporary build tools...', flush=True)
            npm = shutil.which('npm')
            subprocess.run([npm, 'ci', '--ignore-scripts', '--no-audit', '--no-fund'], cwd=shell, check=True)
            env = dict(os.environ)
            # Keep extraction in the temporary tree and use Electron's packaged
            # checksum list, even if the caller has an unrelated Electron setup.
            for name in ('ELECTRON_OVERRIDE_DIST_PATH', 'ELECTRON_INSTALL_PLATFORM',
                         'ELECTRON_INSTALL_ARCH', 'electron_use_remote_checksums',
                         'npm_config_electron_use_remote_checksums'):
                env.pop(name, None)
            env['npm_config_platform'], env['npm_config_arch'] = {
                'linux-amd64': ('linux', 'x64'), 'windows-amd64': ('win32', 'x64'),
                'darwin-arm64': ('darwin', 'arm64'), 'darwin-amd64': ('darwin', 'x64'),
            }[target]
            subprocess.run([shutil.which('node'), 'node_modules/electron/install.js'], cwd=shell, env=env, check=True)
            electron_dir = shell / 'node_modules/electron/dist'
        if reader_dir is None:
            reader = work / 'reader-input'
            print('Fetching the checked Kiwix reader...', flush=True)
            subprocess.run([sys.executable, str(ROOT / 'scripts/ci-fetch-fixtures.py'), '--target', target,
                            '--output', str(reader), '--reader-only'], check=True)
            reader_dir = reader / 'reader'
        command = [sys.executable, str(ROOT / 'scripts/package-desktop.py'), '--target', target,
                   '--electron-dir', str(electron_dir),
                   '--reader-dir', str(reader_dir), '--output', str(output)]
        if windows_runtime is not None:
            command.extend(('--windows-runtime-dir', str(windows_runtime)))
        print('Assembling the development app...', flush=True)
        subprocess.run(command, check=True)
    return output


def record_reader_version(target, output):
    """Record the bundled reader's own version report when it can run here."""
    info_path = output / 'build-info.json'
    info = json.loads(info_path.read_text(encoding='utf-8'))
    if native_target() != target:
        report = {'status': 'not run on cross-target build'}
    else:
        binary = (output / 'Offline Library.app/Contents/Resources/bin/kiwix-serve'
                  if target.startswith('darwin-') else
                  output / 'resources/bin' / ('kiwix-serve.exe' if target.startswith('windows-') else 'kiwix-serve'))
        try:
            result = subprocess.run([str(binary), '--version'], check=True, capture_output=True,
                                    text=True, encoding='utf-8', errors='replace', timeout=15)
            text = result.stdout.strip()
            if not text or len(text) > 16384:
                raise ValueError('empty or oversized version report')
            report = {'status': 'reported by bundled executable', 'text': text,
                      'note': 'Self-reported versions are not proof of exact source or build inputs.'}
        except (OSError, ValueError, subprocess.SubprocessError):
            report = {'status': 'version report unavailable'}
    info['reader_version_report'] = report
    info_path.write_text(json.dumps(info, indent=2) + '\n', encoding='utf-8')
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', choices=TARGETS, default=native_target())
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--windows-runtime-dir', type=Path)
    parser.add_argument('--reader-dir', type=Path, help='use a supplied, platform-matched reader directory')
    parser.add_argument('--electron-dir', type=Path, help='use an extracted, platform-matched Electron distribution')
    args = parser.parse_args()
    if args.target is None:
        parser.error('Host platform is unsupported; supply --target')
    try:
        result = build(args.target, args.output, args.windows_runtime_dir, args.reader_dir, args.electron_dir)
        record_reader_version(args.target, result)
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as exc:
        print(f'Build failed: {exc}', file=sys.stderr)
        return 1
    print(f'Local development preview: {result}\nUnsigned; review THIRD-PARTY.md before redistribution.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
