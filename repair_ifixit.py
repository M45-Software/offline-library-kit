#!/usr/bin/env python3
"""Create a separate iFixit ZIM with repaired links and explicit coverage notices.

Uses an audit_ifixit.py report and optional libzim. Never downloads content or
changes the input ZIM. Empty source pages remain incomplete; the new browse index
lists only guides with archived step sections.
"""
import argparse
from collections import defaultdict
import csv
from html import escape, unescape
import json
from pathlib import Path
import posixpath
import re
import sys
from urllib.parse import quote

from audit_ifixit import target_for


def plan_links(z, missing):
    numeric = defaultdict(list)
    folded = defaultdict(list)
    for entry in z.iter_by_path():
        path = entry.path
        if path.startswith('Guide/'):
            numeric[('Guide', path.rsplit('/', 1)[-1])].append(path)
        elif path.startswith('User/'):
            numeric[('User', path.split('/')[1])].append(path)
        if path.startswith(('Device/', 'Info/')):
            folded[path.casefold().replace('\u00a0', ' ')].append(path)
    repairs = {}
    for row in missing:
        target = row['target']
        bits = target.split('/')
        options = []
        if bits[0] in {'Guide', 'User'}:
            ident = bits[-1] if bits[0] == 'Guide' else bits[1]
            options = numeric[(bits[0], ident)]
        if not options:
            options = folded[target.casefold().replace('\u00a0', ' ')]
        if not options and '#' in target:
            base = target.split('#')[0]
            if z.has_entry_by_path(base):
                options = [target]
        if len(options) == 1:
            repairs[target] = options[0]
    return repairs


def relative_link(source, target):
    base, marker, fragment = target.partition('#')
    relative = posixpath.relpath(base, posixpath.dirname(source) or '.')
    return quote(relative, safe='/+') + ('#' + quote(fragment) if marker else '')


def rewrite_links(text, source, repairs):
    changes = 0

    def replace(match):
        nonlocal changes
        old = target_for(source, unescape(match[2]))
        if old not in repairs:
            return match[0]
        changes += 1
        return 'href=' + match[1] + escape(relative_link(source, repairs[old]), quote=True) + match[1]

    text = re.sub(r'''href\s*=\s*(["'])(.*?)\1''', replace, text, flags=re.I)
    return text, changes


def document(title, body):
    return ('<!doctype html><html lang="en"><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width,initial-scale=1">'
            f'<title>{escape(title)}</title><style>'
            'body{font:18px/1.6 system-ui;max-width:65rem;margin:auto;padding:1rem}'
            'a{color:#145d9b}li{margin:.5rem 0}nav a{margin-right:1rem}'
            '</style><body>' + body + '</body></html>')


def build(source, audit_dir, destination):
    from libzim.reader import Archive
    from libzim.writer import Creator, Hint, Item, StringProvider

    if destination.exists() or destination.with_suffix(destination.suffix + '.part').exists():
        raise ValueError('Output or partial already exists; inspect it before retrying')
    report = json.loads((audit_dir / 'audit.json').read_text())
    z = Archive(str(source))
    if report['zim_checksum'] != z.checksum or report['archive_bytes'] != source.stat().st_size:
        raise ValueError('Audit does not match source archive')
    with (audit_dir / 'devices.csv').open() as f:
        empty = {r['path'] for r in csv.DictReader(f) if r['status'] == 'empty_device'}
    no_steps = {r['path'] for r in report['guides_without_steps']}
    repairs = plan_links(z, report['links']['missing'])
    groups = defaultdict(list)
    for entry in z.iter_by_path():
        if entry.path.startswith('Guide/') and not entry.is_redirect and entry.path not in no_steps:
            letter = entry.title[:1].upper()
            key = letter if letter in 'ABCDEFGHIJKLMNOPQRSTUVWXYZ' else '0'
            groups[key].append((entry.title, entry.path))
    new_pages = {}
    for key, entries in groups.items():
        path = f'offline-quality/{key}.html'
        body = '<a href="index.html">Browse index</a><h1>Repair guides: ' + escape(key) + '</h1><ul>'
        body += ''.join(f'<li><a href="{escape(relative_link(path, p), quote=True)}">{escape(t)}</a></li>'
                        for t, p in sorted(entries, key=lambda row: row[0].casefold()))
        new_pages[path] = document('iFixit guides: ' + key, body + '</ul>')
    total = sum(map(len, groups.values()))
    nav = ''.join(f'<a href="{key}.html">{key}</a>' for key in sorted(groups))
    new_pages['offline-quality/index.html'] = document('iFixit offline repair guides',
        f'<h1>iFixit offline repair guides</h1><p>{total:,} guides with archived step sections.</p>'
        f'<nav>{nav}</nav><p><a href="../home/home">Original iFixit categories</a></p>'
        f'<p>This local edition repairs {len(repairs):,} broken link targets and labels '
        f'{len(empty):,} empty device pages and {len(no_steps):,} guides without steps. '
        'It does not recover missing documents, forum discussions, or videos. '
        'A guide appearing here has step markup; completeness and technical accuracy have not been established.</p>'
        '<p>Source: openZIM iFixit archive, ' + escape(report['metadata'].get('Date', 'unknown date')) + '. '
        'Local changes: repaired internal links, coverage notices, and this browse index. '
        'Content by <a href="https://www.ifixit.com/">iFixit and its contributors</a>, '
        '<a href="https://creativecommons.org/licenses/by-nc-sa/3.0/">CC BY-NC-SA 3.0</a>.</p>')

    class CopyItem(Item):
        def __init__(self, path, title, mimetype, content, front):
            self.path, self.title, self.mimetype = path, title, mimetype
            self.content, self.front = content, front

        def get_path(self): return self.path
        def get_title(self): return self.title
        def get_mimetype(self): return self.mimetype
        def get_contentprovider(self): return StringProvider(self.content)
        def get_hints(self): return {Hint.FRONT_ARTICLE: int(self.front)}

    partial = destination.with_suffix(destination.suffix + '.part')
    changed_links = changed_pages = 0
    notices = 0
    with Creator(partial).config_indexing(z.has_fulltext_index, 'eng').config_nbworkers(4) as writer:
        for key in z.metadata_keys:
            if key in {'Counter', 'Illustration_48x48@1', 'Title', 'Name', 'Description', 'Publisher'}:
                continue
            writer.add_metadata(key, z.get_metadata(key))
        writer.add_metadata('Title', 'iFixit in English — local link repairs')
        writer.add_metadata('Name', 'ifixit_en_local_repaired')
        writer.add_metadata('Description', 'Repair guides with corrected links and explicit offline coverage notices.')
        writer.add_metadata('Publisher', 'Local adaptation of openZIM')
        if z.has_illustration(48):
            writer.add_illustration(bytes(z.get_illustration_item(48).content), 48)
        writer.set_mainpath('offline-quality/index.html')
        for i, entry in enumerate(z.iter_efficient()):
            path = entry.path
            if path in new_pages:
                raise ValueError('Source already contains a local quality index')
            if entry.is_redirect:
                writer.add_redirection(path, entry.title, entry.get_redirect_entry().path, {})
                continue
            item = entry.get_item()
            content = bytes(item.content)
            if item.mimetype == 'text/html':
                text, changes = rewrite_links(content.decode('utf-8'), path, repairs)
                if changes:
                    changed_links += changes
                    changed_pages += 1
                if path in empty or path in no_steps:
                    reason = ('This archived device page has no description, repair-guide links, or child devices.'
                              if path in empty else 'This archived guide has no step-by-step sections.')
                    note = ('<aside style="background:#fff3cd;color:#332701;border:2px solid #9b7700;'
                            'padding:1rem;margin:1rem;font:18px/1.5 system-ui" role="note">'
                            '<strong>Offline coverage is incomplete.</strong> ' + reason +
                            ' This local notice does not restore missing source content. '
                            f'<a href="{escape(relative_link(path, "offline-quality/index.html"), quote=True)}">'
                            'Browse guides with archived steps</a>.</aside>')
                    text, inserted = re.subn(r'(<body\b[^>]*>)', lambda m: m[0] + note, text, count=1, flags=re.I)
                    if inserted != 1:
                        raise ValueError('Cannot insert coverage notice: ' + path)
                    notices += 1
                content = text.encode('utf-8')
            writer.add_item(CopyItem(path, entry.title, item.mimetype, content,
                item.mimetype == 'text/html' and path not in empty and path not in no_steps))
            if (i + 1) % 100000 == 0:
                print(f'Copied {i + 1:,} entries', file=sys.stderr, flush=True)
        for path, content in new_pages.items():
            writer.add_item(CopyItem(path, 'iFixit offline guide index', 'text/html', content, False))
    built = Archive(str(partial))
    if not built.check():
        raise ValueError('Built archive failed its checksum; partial retained')
    partial.rename(destination)
    result = {'source': str(source), 'destination': str(destination),
              'repaired_target_count': len(repairs), 'changed_link_occurrences': changed_links,
              'changed_link_pages': changed_pages, 'coverage_notices': notices,
              'indexed_guides_with_steps': total, 'checksum': built.checksum,
              'missing_content_recovered': False}
    destination.with_suffix('.repair.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('source', type=Path)
    p.add_argument('--audit', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path)
    args = p.parse_args()
    build(args.source, args.audit, args.output)


if __name__ == '__main__':
    main()
