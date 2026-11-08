#!/usr/bin/env python3
"""Download pinned experimental reader build inputs to a new local folder.

The lock records observed hashes, not publisher signatures or a complete
corresponding-source package. Downloads require network access.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import urllib.parse
import urllib.request

TOOLS = Path(__file__).resolve().parent
ROOT = TOOLS.parent.parent
spec = importlib.util.spec_from_file_location('prepare_reader_inputs', TOOLS / 'prepare-inputs.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


def https_url(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or parsed.port:
        raise ValueError('Only ordinary HTTPS source URLs are allowed')
    return url


class HTTPSRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return super().redirect_request(req, fp, code, msg, headers, https_url(newurl))


def file_hash(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def check_file(path, record):
    if path.stat().st_size != record['size'] or file_hash(path) != record['sha256']:
        raise ValueError('Input differs from the recorded size or SHA-256 baseline')


def download(record, destination, opener):
    errors = []
    for url in record['urls']:
        temporary = destination.with_name(destination.name + '.part')
        try:
            request = urllib.request.Request(https_url(url), headers={'User-Agent': 'Offline-Library-Kit source capture', 'Accept-Encoding': 'identity'})
            with opener.open(request, timeout=60) as response, temporary.open('xb') as out:
                count = 0
                while True:
                    chunk = response.read(min(1024 * 1024, record['size'] - count + 1))
                    if not chunk:
                        break
                    count += len(chunk)
                    if count > record['size']:
                        raise ValueError('Download exceeds recorded size')
                    out.write(chunk)
            check_file(temporary, record)
            temporary.rename(destination)
            return
        except (OSError, ValueError) as exc:
            temporary.unlink(missing_ok=True)
            errors.append(type(exc).__name__ + ': ' + str(exc))
    raise RuntimeError('All source URLs failed: ' + '; '.join(errors))


def capture_git(record, destination, scratch):
    repo = scratch / 'repo'
    repo.mkdir()
    env = {**os.environ, 'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_TERMINAL_PROMPT': '0'}
    def git(*args):
        try:
            return subprocess.run(['git', '-c', 'core.hooksPath=' + os.devnull, '-c', 'http.followRedirects=false', '-C', str(repo), *args], env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
        except subprocess.CalledProcessError as exc:
            raise RuntimeError('Git source capture failed: ' + exc.stderr.decode('utf-8', errors='replace')[-1000:]) from exc
    git('init', '-q')
    git('fetch', '--no-tags', '--depth=1', https_url(record['git']), record['commit'])
    actual = git('rev-parse', 'FETCH_HEAD^{commit}').stdout.decode('ascii').strip()
    if actual != record['commit']:
        raise ValueError('Git fetched a different commit')
    temporary = destination.with_name(destination.name + '.part')
    git('archive', '--format=tar', '--output=' + str(temporary), actual)
    check_file(temporary, record)
    temporary.rename(destination)
    shutil.rmtree(repo)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output', type=Path, help='new output folder outside the repository')
    args = parser.parse_args()
    if args.output.is_symlink():
        raise ValueError('Output cannot be a symlink')
    output = args.output.resolve()
    if output.is_relative_to(ROOT) or ROOT.is_relative_to(output):
        raise ValueError('Output must be separate from the repository')
    lock = json.loads((TOOLS / 'input-lock.json').read_text(encoding='utf-8'))
    required = {'git-sources/' + name for name in prepare.GIT_INPUTS}
    required.update('kiwix-archives/' + name for name in prepare.ARCHIVES)
    required.update('test-subprojects/' + name for name in prepare.TEST_INPUTS)
    if lock.get('schema') != 1 or set(lock.get('files', {})) != required:
        raise ValueError('Invalid recorded input lock')
    output.mkdir(parents=True, exist_ok=False)
    marker = output / 'CAPTURE-INCOMPLETE'
    marker.write_text('Input capture did not complete; inspect before removal.\n')
    opener = urllib.request.build_opener(HTTPSRedirects())
    with tempfile.TemporaryDirectory(prefix='git-capture-', dir=output) as temporary:
        scratch = Path(temporary)
        for name, record in sorted(lock['files'].items()):
            path = output / name
            path.parent.mkdir(parents=True, exist_ok=True)
            print('Capturing', name, flush=True)
            if 'git' in record:
                capture_git(record, path, scratch)
            else:
                download(record, path, opener)
    (output / 'SHA256SUMS').write_text(''.join(record['sha256'] + '  ' + name + '\n' for name, record in sorted(lock['files'].items())), encoding='utf-8')
    (output / 'CAPTURED.txt').write_text('Captured pinned inputs; hashes are local baselines, not publisher signatures.\nHost toolchain, system libraries and complete license review are not included.\n')
    marker.unlink()
    print('Captured inputs at', output)


if __name__ == '__main__':
    main()
