import importlib.util
import io
from unittest.mock import patch
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('ci_fetch_fixtures', ROOT / 'scripts/ci-fetch-fixtures.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FixtureDownloads(unittest.TestCase):
    def test_existing_file_is_never_removed_or_overwritten(self):
        with tempfile.TemporaryDirectory() as temp:
            destination = Path(temp) / 'existing'
            destination.write_bytes(b'keep')
            with self.assertRaises(FileExistsError):
                module.get_with_fallback(['https://example.invalid/'], destination, 4)
            self.assertEqual(destination.read_bytes(), b'keep')


    def test_oversized_input_is_removed_and_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            destination = Path(temp) / 'input'
            with patch.object(module.urllib.request, 'urlopen', return_value=io.BytesIO(b'too long')):
                with self.assertRaisesRegex(RuntimeError, 'exceeds'):
                    module.get_with_fallback(['https://example.invalid/'], destination, 3)
            self.assertFalse(destination.exists())


if __name__ == '__main__':
    unittest.main()
