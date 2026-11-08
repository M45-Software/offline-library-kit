import importlib.util
import json
from types import SimpleNamespace
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('build_preview', ROOT / 'scripts/build-desktop-preview.py')
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class BuildPreview(unittest.TestCase):
    def test_detects_supported_host_and_rejects_others(self):
        self.assertEqual(builder.native_target('Windows', 'AMD64'), 'windows-amd64')
        self.assertEqual(builder.native_target('Darwin', 'arm64'), 'darwin-arm64')
        self.assertEqual(builder.native_target('Linux', 'x86_64'), 'linux-amd64')
        self.assertIsNone(builder.native_target('Linux', 'aarch64'))

    def test_does_not_replace_existing_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / 'existing'
            out.mkdir()
            (out / 'keep').write_text('unchanged')
            with self.assertRaises(FileExistsError):
                builder.build('linux-amd64', out)
            self.assertEqual((out / 'keep').read_text(), 'unchanged')

    def test_records_native_reader_report_and_skips_cross_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            info = out / 'build-info.json'
            info.write_text(json.dumps({'target': 'linux-amd64'}))
            with patch.object(builder, 'native_target', return_value='linux-amd64'), \
                 patch.object(builder.subprocess, 'run', return_value=SimpleNamespace(stdout='kiwix-tools 3.8.2\nlibzim 9.5.0\n')) as run:
                report = builder.record_reader_version('linux-amd64', out)
                self.assertIn('libzim 9.5.0', report['text'])
                self.assertEqual(json.loads(info.read_text())['reader_version_report'], report)
                self.assertEqual(run.call_args.args[0][-1], '--version')
                report = builder.record_reader_version('darwin-arm64', out)
                self.assertEqual(report['status'], 'not run on cross-target build')
                self.assertEqual(run.call_count, 1)

    def test_uses_reader_only_and_targeted_runtime(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch.object(builder.shutil, 'which', side_effect=lambda name: name), \
             patch.object(builder.subprocess, 'run') as run:
            out = Path(tmp) / 'new'
            builder.build('windows-amd64', out, Path(tmp) / 'runtime')
            calls = [call.args[0] for call in run.call_args_list]
            self.assertTrue(any('--reader-only' in call for call in calls))
            self.assertTrue(any('--windows-runtime-dir' in call and 'windows-amd64' in call for call in calls))
            self.assertTrue(any(call[:2] == ['npm', 'ci'] for call in calls))
            install = next(call for call in run.call_args_list if 'node_modules/electron/install.js' in call.args[0])
            self.assertEqual(install.kwargs['env']['npm_config_platform'], 'win32')
            self.assertEqual(install.kwargs['env']['npm_config_arch'], 'x64')

    def test_supplied_reader_and_electron_skip_fetch_and_npm(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch.object(builder.shutil, 'which', side_effect=lambda name: name if name == 'go' else None), \
             patch.object(builder.subprocess, 'run') as run:
            root = Path(tmp)
            reader, electron = root / 'reader', root / 'electron'
            reader.mkdir(); electron.mkdir()
            builder.build('linux-amd64', root / 'out', reader_dir=reader, electron_dir=electron)
            self.assertEqual(run.call_count, 1)
            call = run.call_args.args[0]
            self.assertEqual(call[call.index('--reader-dir') + 1], str(reader))
            self.assertEqual(call[call.index('--electron-dir') + 1], str(electron))
            self.assertNotIn('--reader-only', call)

    def test_rejects_missing_supplied_inputs_before_downloading(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(builder.subprocess, 'run') as run:
            with self.assertRaises(FileNotFoundError):
                builder.build('linux-amd64', Path(tmp) / 'out', reader_dir=Path(tmp) / 'missing')
            run.assert_not_called()

    def test_each_supplied_input_independently_skips_its_fetch(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch.object(builder.shutil, 'which', side_effect=lambda name: name), \
             patch.object(builder.subprocess, 'run') as run:
            root = Path(tmp)
            reader, electron = root / 'reader', root / 'electron'
            reader.mkdir(); electron.mkdir()
            builder.build('linux-amd64', root / 'out1', reader_dir=reader)
            calls = [call.args[0] for call in run.call_args_list]
            self.assertTrue(any(call[:2] == ['npm', 'ci'] for call in calls))
            self.assertFalse(any('--reader-only' in call for call in calls))
            run.reset_mock()
            builder.build('linux-amd64', root / 'out2', electron_dir=electron)
            calls = [call.args[0] for call in run.call_args_list]
            self.assertFalse(any(call[:2] == ['npm', 'ci'] for call in calls))
            self.assertTrue(any('--reader-only' in call for call in calls))


if __name__ == '__main__':
    unittest.main()
