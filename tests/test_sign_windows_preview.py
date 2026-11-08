import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('sign_windows_preview', ROOT / 'scripts/sign-windows-preview.py')
signer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(signer)


class WindowsSigning(unittest.TestCase):
    def setup_preview(self, root):
        source = root / 'source'
        bindir = source / 'resources/bin'
        bindir.mkdir(parents=True)
        (source / 'Offline Library.exe').write_bytes(b'window')
        (bindir / 'offline-library-core.exe').write_bytes(b'core')
        (bindir / 'kiwix-serve.exe').write_bytes(b'reader')
        (bindir / 'msvcp140.dll').write_bytes(b'runtime')
        info = {'target': 'windows-amd64', 'shell_executable_sha256': signer.digest(source / 'Offline Library.exe'),
                'binary_sha256': {p.name: signer.digest(p) for p in bindir.iterdir()}}
        (source / 'build-info.json').write_text(json.dumps(info))
        tool = root / 'signtool.exe'; tool.write_bytes(b'test tool')
        return source, tool, info

    def test_signs_copy_and_retains_reader(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source, tool, before = self.setup_preview(root); output = root / 'signed'
            def fake_run(command, **_):
                if command[1] == 'sign':
                    file = Path(command[-1]); file.write_bytes(file.read_bytes() + b'-signed')
            with patch.object(signer.platform, 'system', return_value='Windows'), patch.object(signer.subprocess, 'run', side_effect=fake_run) as run:
                signer.sign_copy(source, output, tool, 'a' * 40)
            after = json.loads((output / 'build-info.json').read_text())
            self.assertEqual((source / 'Offline Library.exe').read_bytes(), b'window')
            self.assertEqual((output / 'Offline Library.exe').read_bytes(), b'window-signed')
            self.assertEqual((output / 'resources/bin/kiwix-serve.exe').read_bytes(), b'reader')
            self.assertEqual(after['pre_signing_binary_sha256'], before['binary_sha256'])
            self.assertEqual(after['pre_signing_shell_sha256'], before['shell_executable_sha256'])
            self.assertFalse(after['signing']['timestamped'])
            self.assertFalse((output / 'SIGNING-INCOMPLETE').exists())
            self.assertEqual(run.call_count, 4)
            self.assertTrue(all('/tr' not in call.args[0] for call in run.call_args_list))

    def test_rejects_existing_output_and_modified_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source, tool, _ = self.setup_preview(root); output = root / 'existing'
            output.mkdir(); (output / 'keep').write_text('untouched')
            with patch.object(signer.platform, 'system', return_value='Windows'):
                with self.assertRaises(FileExistsError): signer.sign_copy(source, output, tool, 'a' * 40)
                self.assertEqual((output / 'keep').read_text(), 'untouched')
                (source / 'resources/bin/kiwix-serve.exe').write_bytes(b'modified')
                with self.assertRaisesRegex(ValueError, 'differs'): signer.sign_copy(source, root / 'new', tool, 'a' * 40)
                self.assertFalse((root / 'new').exists())

    def test_timeout_preserves_original_and_marks_copy_incomplete(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source, tool, _ = self.setup_preview(root); output = root / 'signed'
            timeout = signer.subprocess.TimeoutExpired('signtool', 120)
            with patch.object(signer.platform, 'system', return_value='Windows'), patch.object(signer.subprocess, 'run', side_effect=timeout):
                with self.assertRaises(signer.subprocess.TimeoutExpired):
                    signer.sign_copy(source, output, tool, 'a' * 40)
            self.assertEqual((source / 'Offline Library.exe').read_bytes(), b'window')
            self.assertTrue((output / 'SIGNING-INCOMPLETE').is_file())

    def test_refuses_unsafe_timestamp_service(self):
        with patch.object(signer.platform, 'system', return_value='Windows'):
            with self.assertRaisesRegex(ValueError, 'Timestamp URL'):
                signer.sign_copy(Path('unused'), Path('unused2'), Path('unused3'), 'a' * 40, 'http://example.com')


if __name__ == '__main__':
    unittest.main()
