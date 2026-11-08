import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/reader-rebuild/prepare-inputs.py'
spec = importlib.util.spec_from_file_location('prepare_reader_rebuild', SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
policy_spec = importlib.util.spec_from_file_location('link_warning_policy', SCRIPT.parent / 'link_warning_policy.py')
policy = importlib.util.module_from_spec(policy_spec)
policy_spec.loader.exec_module(policy)
capture_spec = importlib.util.spec_from_file_location('capture_reader_inputs', SCRIPT.parent / 'capture-inputs.py')
capture = importlib.util.module_from_spec(capture_spec)
capture_spec.loader.exec_module(capture)


def inputs(root, unsafe=False):
    (root / 'git-sources').mkdir(parents=True)
    (root / 'kiwix-archives').mkdir()
    for index, name in enumerate(module.GIT_INPUTS):
        with tarfile.open(root / 'git-sources' / name, 'w') as archive:
            member = tarfile.TarInfo('../escape' if unsafe and index == 0 else 'fixture.txt')
            member.size = 2
            archive.addfile(member, io.BytesIO(b'ok'))
    for name in module.ARCHIVES:
        (root / 'kiwix-archives' / name).write_bytes(b'archive fixture')
    for name in module.TEST_INPUTS:
        file = root / 'test-subprojects' / name
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_bytes(b'test fixture')
    files = {str(file.relative_to(root)): {'sha256': hashlib.sha256(file.read_bytes()).hexdigest(), 'size': file.stat().st_size}
             for file in sorted(root.rglob('*')) if file.is_file()}
    with (root / 'SHA256SUMS').open('w') as manifest:
        for name, record in files.items():
            manifest.write(record['sha256'] + '  ' + name + '\n')
    tool = root.parent / 'test-tool'
    tool.mkdir()
    for name in ('prepare-inputs.py', 'offline-builder.py', 'link_warning_policy.py', 'optional-qmake'):
        shutil.copy2(SCRIPT.parent / name, tool / name)
    (tool / 'input-lock.json').write_text(json.dumps({'schema': 1, 'files': files}))


def prepare(candidate, output):
    return subprocess.run([sys.executable, str(candidate.parent / 'test-tool/prepare-inputs.py'), str(candidate), str(output)], capture_output=True, text=True)


class PrepareReaderRebuild(unittest.TestCase):
    def test_capture_download_checks_hash_size_and_https(self):
        class Opener:
            def open(self, request, timeout):
                return io.BytesIO(b'bad' if request.full_url.endswith('/bad') else b'correct')
        record = {'size': 7, 'sha256': hashlib.sha256(b'correct').hexdigest(),
                  'urls': ['https://example.org/bad', 'https://example.org/good']}
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / 'archive'
            capture.download(record, target, Opener())
            self.assertEqual(target.read_bytes(), b'correct')
            self.assertFalse((target.parent / 'archive.part').exists())
            with self.assertRaises(ValueError):
                capture.https_url('http://example.org/source')
            with self.assertRaises(ValueError):
                capture.https_url('https://user:pass@example.org/source')
            with self.assertRaises(ValueError):
                capture.HTTPSRedirects().redirect_request(None, None, 302, '', {}, 'http://example.org/source')

    def test_capture_refuses_existing_output_without_network(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            marker = root / 'keep'
            marker.write_text('preserved')
            with patch.object(capture.urllib.request, 'build_opener', side_effect=AssertionError('network should not start')):
                with patch.object(sys, 'argv', [str(SCRIPT.parent / 'capture-inputs.py'), str(root)]):
                    with self.assertRaises(FileExistsError):
                        capture.main()
            self.assertEqual(marker.read_text(), 'preserved')

    @unittest.skipUnless(sys.platform.startswith('linux'), 'Linux-only runner')
    def test_runner_refuses_existing_build_and_unsupported_path(self):
        runner = SCRIPT.parent / 'run-isolated.sh'
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ('PREPARED.txt', 'offline-builder.py', 'link_warning_policy.py', 'optional-qmake'):
                (root / name).write_text('fixture')
            (root / 'optional-qmake').chmod(0o755)
            log = root / 'build-run.log'
            log.write_text('preserve this log')
            result = subprocess.run([str(runner), str(root), sys.executable], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('prior run', result.stderr)
            self.assertEqual(log.read_text(), 'preserve this log')
            bad = root / 'path with spaces'
            bad.mkdir()
            for name in ('PREPARED.txt', 'offline-builder.py', 'link_warning_policy.py', 'optional-qmake'):
                (bad / name).write_text('fixture')
            (bad / 'optional-qmake').chmod(0o755)
            result = subprocess.run([str(runner), str(bad), sys.executable], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('paths must use only ASCII', result.stderr)
            self.assertFalse((bad / 'build-run.log').exists())

    def test_warning_policy_rejects_unexpected_diagnostics(self):
        known = "warning: Using 'dlopen' in statically linked applications requires at runtime the shared libraries from the glibc version used for linking"
        self.assertEqual(policy.check_text('/usr/bin/ld: ' + known), [known])
        with self.assertRaises(ValueError):
            policy.check_text('/usr/bin/ld: warning: unexpected problem')
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(OSError):
                policy.check_log(Path(tmp) / 'missing.log')

    def test_prepares_inputs_without_overwriting(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / 'inputs'; output = root / 'out'
            inputs(source)
            result = prepare(source, output)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((output / 'SOURCE/libzim_release/fixture.txt').read_bytes(), b'ok')
            self.assertEqual((output / 'ARCHIVE' / module.ARCHIVES[0]).read_bytes(), b'archive fixture')
            self.assertTrue((output / 'link_warning_policy.py').is_file())
            self.assertFalse((output / 'BUILD-INCOMPLETE').exists())
            self.assertNotEqual(prepare(source, output).returncode, 0)
            self.assertEqual((output / 'SOURCE/libzim_release/fixture.txt').read_bytes(), b'ok')

    def test_changed_input_and_unsafe_tar_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / 'inputs'; output = root / 'out'
            inputs(source)
            (source / 'kiwix-archives' / module.ARCHIVES[0]).write_bytes(b'changed')
            self.assertNotEqual(prepare(source, output).returncode, 0)
            self.assertFalse(output.exists())
            # Replacing both an input and its accompanying checksum still
            # cannot override the separately recorded project lock.
            name = 'kiwix-archives/' + module.ARCHIVES[0]
            manifest = source / 'SHA256SUMS'
            lines = manifest.read_text().splitlines()
            manifest.write_text('\n'.join((hashlib.sha256(b'changed').hexdigest() + '  ' + name) if line.endswith('  ' + name) else line for line in lines) + '\n')
            self.assertNotEqual(prepare(source, output).returncode, 0)
            self.assertFalse(output.exists())
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / 'inputs'; output = root / 'out'
            inputs(source, unsafe=True)
            self.assertNotEqual(prepare(source, output).returncode, 0)
            self.assertFalse((root / 'escape').exists())
            self.assertTrue((output / 'BUILD-INCOMPLETE').is_file())


if __name__ == '__main__':
    unittest.main()
