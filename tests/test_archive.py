import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import archive

class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.addCleanup(self.temp.cleanup)
        self.stage = self.root/'stage'; self.stage.mkdir()
        self.target = self.root/'target'; self.target.mkdir()
        self.data = b'example archive bytes'
        self.item = {'name':'example.zim','url':'https://example.org/example.zim','bytes':len(self.data),'sha256':hashlib.sha256(self.data).hexdigest()}
        (self.stage/self.item['name']).write_bytes(self.data)

    def test_manifest_rejects_paths_duplicates_and_invalid_hash(self):
        p = self.root/'manifest.json'
        for items in [[dict(self.item, name='../escape')], [self.item,self.item], [dict(self.item,sha256='bad')], [dict(self.item,url='http://example.org/file')]]:
            p.write_text(json.dumps({'items':items}))
            with self.assertRaises(ValueError): archive.load_manifest(p)

    def test_download_publishes_only_a_matching_partial(self):
        (self.stage/'example.zim').unlink()
        def fake_download(command, check):
            Path(command[command.index('--output')+1]).write_bytes(self.data)
        with patch.object(archive.subprocess, 'run', side_effect=fake_download):
            archive.download_one(self.item, self.stage, 0)
        self.assertEqual((self.stage/'example.zim').read_bytes(), self.data)
        (self.stage/'example.zim').unlink()
        def bad_download(command, check):
            Path(command[command.index('--output')+1]).write_bytes(b'bad')
        with patch.object(archive.subprocess, 'run', side_effect=bad_download), self.assertRaises(RuntimeError):
            archive.download_one(self.item, self.stage, 0)
        self.assertFalse((self.stage/'example.zim').exists())
        self.assertEqual((self.stage/'example.zim.part').read_bytes(), b'bad')

    def test_publish_checks_and_copies_one_file(self):
        with patch.object(archive,'target_ok'):
            archive.publish([self.item],self.stage,self.target,'test-id',0)
        self.assertEqual((self.target/'content/zim/example.zim').read_bytes(),self.data)
        self.assertFalse((self.target/'content/zim/example.zim.part').exists())

    def test_publish_rejects_corrupt_existing_destination(self):
        dest=self.target/'content/zim'; dest.mkdir(parents=True)
        (dest/'example.zim').write_bytes(b'wrong')
        with patch.object(archive,'target_ok'), self.assertRaises(RuntimeError):
            archive.publish([self.item],self.stage,self.target,'test-id',0)
        self.assertEqual((dest/'example.zim').read_bytes(),b'wrong')

    def test_partial_is_preserved_and_wrong_source_is_rejected(self):
        dest=self.target/'content/zim'; dest.mkdir(parents=True)
        part=dest/'example.zim.part'; part.write_bytes(b'partial')
        with patch.object(archive,'target_ok'), self.assertRaises(RuntimeError):
            archive.publish([self.item],self.stage,self.target,'test-id',0)
        self.assertEqual(part.read_bytes(),b'partial')
        part.unlink(); (self.stage/'example.zim').write_bytes(b'corrupt')
        with patch.object(archive,'target_ok'), self.assertRaises(RuntimeError):
            archive.publish([self.item],self.stage,self.target,'test-id',0)
        self.assertFalse((dest/'example.zim').exists())

    def test_target_id_must_match(self):
        marker=self.target/archive.MARKER
        marker.write_text(json.dumps({'id':'one','purpose':'offline-library-target'}))
        with patch.object(Path,'is_mount',return_value=True):
            archive.target_ok(self.target,'one')
            with self.assertRaises(ValueError): archive.target_ok(self.target,'other')

if __name__=='__main__': unittest.main()
