import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest

from audit_ifixit import audit, target_for
from repair_ifixit import build, rewrite_links

try:
    from libzim.writer import Creator, Item, StringProvider
except ImportError:
    Creator = None


class Paths(unittest.TestCase):
    def test_browser_paths_preserve_plus_and_decode_unicode(self):
        self.assertEqual(target_for('Device/Laptop', '../Guide/Battery+%C3%A9/1#step1'),
                         'Guide/Battery+é/1')
        self.assertIsNone(target_for('Device/Laptop', '#Section_Guides'))
        self.assertIsNone(target_for('Device/Laptop', 'https://example.org/manual'))

    def test_repair_encodes_spaces_but_restores_fragment_separator(self):
        text, count = rewrite_links(
            '<a href="../User/1/A+B">author</a><a href="../Device/Laptop%23Specs">specs</a>',
            'Device/Laptop', {'User/1/A+B': 'User/1/A B', 'Device/Laptop#Specs': 'Device/Laptop#Specs'})
        self.assertEqual(count, 2)
        self.assertIn('href="../User/1/A%20B"', text)
        self.assertIn('href="Laptop#Specs"', text)


@unittest.skipIf(Creator is None, 'optional libzim dependency is not installed')
class ArchiveAudit(unittest.TestCase):
    def test_content_and_broken_links_in_real_zim(self):
        class Fixture(Item):
            def __init__(self, path, text):
                self.path, self.text = path, text

            def get_path(self): return self.path
            def get_title(self): return self.path
            def get_mimetype(self): return 'text/html'
            def get_contentprovider(self): return StringProvider('<html><body>' + self.text + '</body></html>')
            def get_hints(self): return {}

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            zim = root / 'fixture.zim'
            with Creator(zim).config_indexing(False, 'eng') as writer:
                writer.set_mainpath('Device/Empty')
                writer.add_item(Fixture('Device/Empty', '<h1>Empty</h1>'))
                writer.add_item(Fixture('Device/Description',
                    '<div id="wikiRenderedText">Model identification information.</div>'))
                writer.add_item(Fixture('Device/Navigation', '<a href="Empty">Child</a>'))
                writer.add_item(Fixture('Device/Media',
                    '<div id="wikiRenderedText"><img src="../images/missing.jpg"></div>'))
                writer.add_item(Fixture('Device/Useful',
                    '<a href="../Guide/Battery+%C3%A9/1">Repair</a>'
                    '<a href="../Answers/Missing">Missing</a>'
                    '<a href="../Answers/Stub">Stub</a>'
                    '<a href="../User/1/A+B">Author</a>'
                    '<img src="../images/missing.jpg">'))
                writer.add_item(Fixture('User/1/A B', '<h1>Author A B</h1>'))
                writer.add_item(Fixture('Guide/Battery+é/1',
                    '<div id="mainBody"><li id="s1234" class="step js-step">'
                    'Disconnect the battery.</li></div>'))
                writer.add_item(Fixture('Guide/NoSteps/2',
                    '<div id="mainBody">Introduction without instructions.</div>'))
                writer.add_item(Fixture('home/not_available', '<p>Not archived</p>'))
                writer.add_redirection('Answers/Stub', 'Stub', 'home/not_available', {})
            with contextlib.redirect_stdout(io.StringIO()):
                audit(zim, root / 'report')
            data = json.loads((root / 'report/audit.json').read_text())
            self.assertEqual(data['device_count'], 5)
            for status in ['empty_device', 'description_only', 'navigation_only', 'has_guide_links', 'media_only']:
                self.assertEqual(data['counts'][status], 1)
            self.assertEqual(data['counts']['guides_with_steps'], 1)
            self.assertEqual(len(data['guides_without_steps']), 1)
            self.assertEqual({r['target'] for r in data['links']['missing']}, {'Answers/Missing', 'User/1/A+B'})
            self.assertEqual([r['target'] for r in data['links']['placeholders']], ['Answers/Stub'])
            self.assertEqual(len(data['resources']['missing']), 1)
            from libzim.reader import Archive
            fixed = root / 'fixed.zim'
            with contextlib.redirect_stdout(io.StringIO()):
                build(zim, root / 'report', fixed)
            repaired = Archive(str(fixed))
            self.assertTrue(repaired.check())
            self.assertEqual(repaired.main_entry.get_item().path, 'offline-quality/index.html')
            page = bytes(repaired.get_entry_by_path('Device/Empty').get_item().content).decode()
            self.assertIn('Offline coverage is incomplete.', page)
            page = bytes(repaired.get_entry_by_path('Guide/Battery+é/1').get_item().content).decode()
            self.assertIn('Disconnect the battery.', page)
            self.assertNotIn('Offline coverage is incomplete.', page)
            page = bytes(repaired.get_entry_by_path('Device/Media').get_item().content).decode()
            self.assertNotIn('Offline coverage is incomplete.', page)
            page = bytes(repaired.get_entry_by_path('Device/Useful').get_item().content).decode()
            self.assertIn('../User/1/A%20B', page)


if __name__ == '__main__':
    unittest.main()
