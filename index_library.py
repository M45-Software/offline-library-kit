#!/usr/bin/env python3
"""Build a portable Kiwix catalog so the server need not rescan every ZIM at boot.

Read ZIM metadata from a source directory (which may contain symlinks to staged
files), and save portable paths relative to the eventual catalog directory.
"""
import argparse
from pathlib import Path, PurePosixPath
import os
import subprocess
import tempfile
import xml.etree.ElementTree as ET


def build(source, output, prefix, manager):
    relative = PurePosixPath(prefix)
    if relative.is_absolute() or '..' in relative.parts:
        raise ValueError('The stored ZIM path prefix must be relative and cannot contain ..')
    archives = sorted(source.glob('*.zim'))
    if not archives:
        raise ValueError('No ZIM files found')
    if not all(path.is_file() for path in archives):
        raise ValueError('Every selected .zim must be a readable file')
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='kiwix-index-') as temporary:
        catalog = Path(temporary) / 'library.xml'
        for archive in archives:
            # kiwix-manage interprets custom paths relative to its working
            # directory, then stores them relative to the XML file.
            stored_path = str(catalog.parent / relative / archive.name)
            subprocess.run([manager, str(catalog), 'add',
                            '--zimPathToSave=' + stored_path, str(archive.absolute())], check=True)
        tree = ET.parse(catalog)
        books = tree.getroot().findall('book')
        expected = {str(relative / path.name) for path in archives}
        actual = {book.attrib['path'] for book in books}
        if actual != expected:
            raise ValueError('Catalog entries differ from the selected archives (duplicate ZIM IDs may be present)')
        data = catalog.read_bytes()
    pending = output.with_name(output.name + '.part')
    with pending.open('xb') as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    pending.replace(output)
    print(f'Indexed {len(archives)} ZIM files in {output}')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('source', type=Path)
    p.add_argument('--output', required=True, type=Path)
    p.add_argument('--path-prefix', default='content/zim')
    p.add_argument('--manager', default='kiwix-manage')
    args = p.parse_args()
    build(args.source, args.output, args.path_prefix, args.manager)


if __name__ == '__main__':
    main()
