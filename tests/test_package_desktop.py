import importlib.util
import json
from pathlib import Path
import struct
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


if __name__ == '__main__':
    unittest.main()
