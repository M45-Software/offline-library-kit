#!/usr/bin/env python3
"""Audit an existing iFixit ZIM without downloading or modifying its content.

Requires the optional libzim Python package. Reports structural content coverage,
not the technical accuracy or completeness of the repair instructions.
"""
import argparse
from collections import Counter, defaultdict
import csv
from datetime import datetime, timezone
from html.parser import HTMLParser
import json
from pathlib import Path
import posixpath
import re
import sys
from urllib.parse import unquote, urlsplit


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack = []
        self.links = []
        self.resources = []
        self.text = defaultdict(list)
        self.steps = 0
        self.wiki_media = 0

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag not in {'area', 'base', 'br', 'col', 'embed', 'hr', 'img',
                       'input', 'link', 'meta', 'param', 'source', 'track', 'wbr'}:
            self.stack.append((tag, attrs.get('id', '')))
        if tag == 'a' and attrs.get('href'):
            self.links.append(attrs['href'])
        if tag in {'img', 'script', 'source'} and attrs.get('src'):
            self.resources.append(attrs['src'])
        if tag in {'img', 'video', 'iframe'} and any(ident == 'wikiRenderedText' for _, ident in self.stack):
            self.wiki_media += 1
        if tag == 'link' and attrs.get('href') and 'stylesheet' in attrs.get('rel', ''):
            self.resources.append(attrs['href'])
        if tag == 'li' and 'js-step' in attrs.get('class', '').split():
            self.steps += 1

    def handle_startendtag(self, tag, attrs):
        before = len(self.stack)
        self.handle_starttag(tag, attrs)
        del self.stack[before:]

    def handle_endtag(self, tag):
        for i in range(len(self.stack) - 1, -1, -1):
            if self.stack[i][0] == tag:
                del self.stack[i:]
                break

    def handle_data(self, data):
        if any(t in {'script', 'style', 'svg'} for t, _ in self.stack):
            return
        for _, ident in self.stack:
            if ident in {'wikiRenderedText', 'banner', 'mainBody'}:
                self.text[ident].append(data)

    def chars(self, ident):
        return len(' '.join(' '.join(self.text[ident]).split()))


def target_for(page, href):
    """Resolve like a browser, retaining literal plus signs in ZIM paths."""
    url = urlsplit(href)
    if url.scheme or url.netloc or not url.path:
        return None
    path = unquote(url.path)
    if path.startswith('/'):
        return path.lstrip('/')
    return posixpath.normpath(posixpath.join(posixpath.dirname(page), path))


def audit(archive, output):
    from libzim.reader import Archive
    z = Archive(str(archive))
    output.mkdir(parents=True, exist_ok=False)
    counts = Counter()
    links = Counter()
    resources = Counter()
    examples = defaultdict(list)
    devices = []
    guides_without_steps = []
    redirects = Counter()
    failures = []
    for entry in z.iter_by_path():
        path = entry.path
        if entry.is_redirect:
            redirects[entry.get_redirect_entry().path.split('?')[0]] += 1
            continue
        if not path.startswith(('Device/', 'Guide/', 'Info/', 'home/')):
            continue
        item = entry.get_item()
        if item.mimetype != 'text/html':
            continue
        counts['html_pages'] += 1
        page = Page()
        try:
            page.feed(bytes(item.content).decode('utf-8'))
        except (UnicodeError, ValueError) as exc:
            failures.append({'path': path, 'error': str(exc)})
            continue
        for values, counter in [(page.links, links), (page.resources, resources)]:
            for href in values:
                target = target_for(path, href)
                if target:
                    counter[target] += 1
                    if len(examples[target]) < 3:
                        examples[target].append(path)
        if path.startswith('Device/'):
            guide_links = sum((target_for(path, h) or '').startswith('Guide/') for h in page.links)
            child_links = sum((target_for(path, h) or '').startswith('Device/') for h in page.links)
            # The banner includes the title. Description is measured separately below.
            raw = bytes(item.content).decode('utf-8')
            match = re.search(r'<span[^>]*itemprop="description"[^>]*>(.*?)</span>', raw, re.S)
            description = re.sub(r'<[^>]+>', '', match[1]).strip() if match else ''
            wiki_chars = page.chars('wikiRenderedText')
            if guide_links:
                status = 'has_guide_links'
            elif wiki_chars or description:
                status = 'description_only'
            elif page.wiki_media:
                status = 'media_only'
            elif child_links:
                status = 'navigation_only'
            else:
                status = 'empty_device'
            counts[status] += 1
            devices.append({'path': path, 'title': entry.title, 'status': status,
                            'guide_links': guide_links, 'child_links': child_links,
                            'wiki_chars': wiki_chars, 'description_chars': len(description),
                            'wiki_media': page.wiki_media})
        elif path.startswith('Guide/'):
            counts['guide_pages'] += 1
            if page.steps:
                counts['guides_with_steps'] += 1
            else:
                guides_without_steps.append({'path': path, 'title': entry.title,
                                             'body_chars': page.chars('mainBody')})
        if counts['html_pages'] % 10000 == 0:
            print(f"Audited {counts['html_pages']:,} HTML pages", file=sys.stderr, flush=True)

    def check_targets(counter):
        missing, placeholders, redirected = [], [], []
        for target, occurrences in counter.items():
            # Queries are browser parameters, not part of an entry path.
            clean = target.split('?')[0]
            if not z.has_entry_by_path(clean):
                missing.append({'target': target, 'occurrences': occurrences,
                                'examples': examples[target]})
                continue
            entry = z.get_entry_by_path(clean)
            final = entry.get_item().path if entry.is_redirect else clean
            row = {'target': target, 'final': final, 'occurrences': occurrences,
                   'examples': examples[target]}
            if final.startswith(('home/not_', 'home/missing', 'home/error', 'home/unavailable')):
                placeholders.append(row)
            elif entry.is_redirect:
                redirected.append(row)
        return {'missing': missing, 'placeholders': placeholders, 'redirected': redirected}

    result = {
        'created_utc': datetime.now(timezone.utc).isoformat(),
        'archive': str(archive.resolve()), 'archive_bytes': archive.stat().st_size,
        'zim_checksum': z.checksum,
        'metadata': {k: z.get_metadata(k).decode('utf-8', errors='replace')
                     for k in z.metadata_keys if k not in {'Counter', 'Illustration_48x48@1'}},
        'counts': dict(counts), 'device_count': len(devices),
        'guides_without_steps': guides_without_steps, 'parse_failures': failures,
        'links': check_targets(links), 'resources': check_targets(resources),
        'redirect_targets': dict(redirects),
        'scope': 'All non-redirect Device, Guide, Info and home HTML entries. '
                 'Step markers and links indicate structural content only. '
                 'Does not establish repair completeness, image clarity, or accuracy.'}
    (output / 'audit.json').write_text(json.dumps(result, indent=2) + '\n')
    with (output / 'devices.csv').open('w', newline='') as f:
        writer = csv.DictWriter(f, fieldnames=list(devices[0]) if devices else ['path'], lineterminator='\n')
        writer.writeheader()
        writer.writerows(devices)
    print(json.dumps({'counts': result['counts'], 'devices': len(devices),
                      'missing_links': len(result['links']['missing']),
                      'placeholder_links': len(result['links']['placeholders']),
                      'missing_resources': len(result['resources']['missing']),
                      'output': str(output)}, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--output', required=True, type=Path,
                        help='New report directory; existing directories are refused')
    args = parser.parse_args()
    audit(args.archive, args.output)


if __name__ == '__main__':
    main()
