import importlib.util
import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('package_desktop', ROOT / 'scripts/package-desktop.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def elf(machine=62):
    data = bytearray(64)
    data[:4] = b'\x7fELF'
    data[4:6] = b'\x02\x01'
    data[18:20] = struct.pack('<H', machine)
    return data


class DesktopPackage(unittest.TestCase):
    def test_architecture_validation(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / 'reader'
            p.write_bytes(elf())
            module.check_binary(p, 'linux', 'amd64')
            with self.assertRaises(ValueError):
                module.check_binary(p, 'linux', 'arm64')
            p.write_bytes(b'not an executable')
            with self.assertRaises(ValueError):
                module.check_binary(p, 'linux', 'amd64')

    def test_package_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            runtime, reader, output = root / 'runtime', root / 'reader', root / 'app'
            runtime.mkdir(); reader.mkdir()
            (runtime / 'version').write_text(module.VERSION)
            (runtime / 'electron').write_bytes(elf())
            (runtime / 'LICENSE').write_text('license')
            (runtime / 'LICENSES.chromium.html').write_text('licenses')
            (reader / 'kiwix-serve').write_bytes(elf())
            def fake_build(args, **kwargs):
                Path(args[args.index('-o') + 1]).write_bytes(elf())
            with patch.object(module.subprocess, 'run', side_effect=fake_build):
                module.assemble('linux-amd64', runtime, reader, output)
            self.assertTrue((output / 'offline-library').is_file())
            self.assertTrue((output / 'offline-library-bin').is_file())
            self.assertTrue((output / 'START-HERE.txt').is_file())
            launcher = output / 'start-with-user-namespace.sh'
            self.assertEqual(launcher.read_bytes(), (ROOT / 'desktop/shell/start-with-user-namespace.sh').read_bytes())
            self.assertTrue(launcher.stat().st_mode & 0o111)
            self.assertTrue((output / 'resources/bin/kiwix-serve').is_file())
            self.assertFalse((output / 'BUILD-INCOMPLETE').exists())
            self.assertEqual(json.loads((output / 'build-info.json').read_text())['target'], 'linux-amd64')
            with self.assertRaises(FileExistsError):
                module.assemble('linux-amd64', runtime, reader, output)
            self.assertTrue((output / 'offline-library').is_file())
            self.assertTrue((output / 'offline-library-bin').is_file())
            self.assertTrue((output / 'START-HERE.txt').is_file())

    def test_reader_provenance_detects_modified_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            reader = Path(tmp)
            binary = reader / 'kiwix-serve'
            binary.write_bytes(elf())
            name = 'kiwix-tools_linux-x86_64-3.8.2.tar.gz'
            record = {'schema': 1, 'target': 'linux-amd64', 'archive_name': name,
                      'archive_url': 'https://download.kiwix.org/release/kiwix-tools/' + name,
                      'publisher_checksum_url': 'https://download.kiwix.org/release/kiwix-tools/' + name + '.md5',
                      'archive_size_bytes': 100, 'publisher_md5': 'a' * 32,
                      'local_sha256_baseline': 'b' * 64,
                      'reader_files_sha256': {'kiwix-serve': module.sha256(binary)}}
            (reader / 'reader-provenance.json').write_text(json.dumps(record))
            result = module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')
            self.assertEqual(result['reported']['archive_name'], name)
            self.assertIn('not an authenticity', result['note'])
            binary.write_bytes(elf() + b'changed')
            with self.assertRaisesRegex(ValueError, 'do not match'):
                module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')
            record['reader_files_sha256']['kiwix-serve'] = module.sha256(binary)
            record['archive_url'] += '?secret=do-not-copy'
            (reader / 'reader-provenance.json').write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, 'source'):
                module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')

    def test_rejects_output_inside_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            runtime = root / 'runtime'; runtime.mkdir()
            with self.assertRaisesRegex(ValueError, 'inside an input'):
                module.assemble('linux-amd64', runtime, root, runtime / 'out')
            self.assertFalse((runtime / 'out').exists())

    def test_source_build_record_checks_bytes_and_excludes_extra_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            reader = Path(tmp); binary = reader / 'kiwix-serve'
            binary.write_bytes(elf())
            record = {'schema': 1, 'kind': 'source-build', 'target': 'linux-amd64',
                      'reader_files_sha256': {'kiwix-serve': module.sha256(binary)},
                      'kiwix_build_commit': 'a' * 40, 'input_lock_sha256': 'b' * 64,
                      'build_adapter_sha256': 'c' * 64, 'warning_policy_sha256': 'd' * 64,
                      'private_path': '/do/not/copy'}
            metadata = reader / 'reader-provenance.json'
            metadata.write_text(json.dumps(record))
            result = module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')
            self.assertNotIn('private_path', result['reported'])
            self.assertIn('not independently verified', result['note'])
            runtime = reader / 'runtime'; runtime.mkdir()
            (runtime / 'version').write_text(module.VERSION)
            (runtime / 'electron').write_bytes(elf())
            (runtime / 'LICENSE').write_text('license')
            (runtime / 'LICENSES.chromium.html').write_text('licenses')
            def fake_build(args, **kwargs):
                Path(args[args.index('-o') + 1]).write_bytes(elf())
            with tempfile.TemporaryDirectory() as tmp_output, patch.object(module.subprocess, 'run', side_effect=fake_build):
                output = Path(tmp_output) / 'app'
                module.assemble('linux-amd64', runtime, reader, output)
                info = json.loads((output / 'build-info.json').read_text())
                self.assertEqual(info['reader_source']['reported']['kind'], 'source-build')
                self.assertNotIn('private_path', info['reader_source']['reported'])
            record['input_lock_sha256'] = 'not-a-hash'
            metadata.write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, 'recipe hashes'):
                module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')
            record['input_lock_sha256'] = 'b' * 64
            record['reader_files_sha256']['kiwix-serve'] = '0' * 64
            metadata.write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, 'do not match'):
                module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')

    def test_source_record_generator_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); reader = root / 'reader'; reader.mkdir()
            (reader / 'kiwix-serve').write_bytes(elf())
            for name in ('lock', 'adapter', 'policy'):
                (root / name).write_text(name)
            command = [sys.executable, str(ROOT / 'scripts/reader-rebuild/record-provenance.py'),
                       '--reader-dir', str(reader), '--input-lock', str(root / 'lock'),
                       '--build-adapter', str(root / 'adapter'), '--warning-policy', str(root / 'policy'),
                       '--kiwix-build-commit', 'a' * 40]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            metadata = reader / 'reader-provenance.json'
            original = metadata.read_text()
            self.assertNotIn(str(root), original)
            self.assertEqual(module.reader_provenance(reader, 'linux-amd64', 'kiwix-serve', 'linux')['reported']['kind'], 'source-build')
            self.assertNotEqual(subprocess.run(command, capture_output=True, text=True).returncode, 0)
            self.assertEqual(metadata.read_text(), original)


if __name__ == '__main__':
    unittest.main()
