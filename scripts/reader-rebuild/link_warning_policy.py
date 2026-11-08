"""Narrow warning policy for the experimental static Kiwix tools build."""
import re
from pathlib import Path

_ANSI = re.compile(r'\x1b\[[0-9;]*m')
_WARNING = re.compile(r'\bwarning:', re.IGNORECASE)
_ALLOWED = {
    "warning: Using '" + name + "' in statically linked applications requires at runtime the shared libraries from the glibc version used for linking"
    for name in ('dlopen', 'getaddrinfo', 'gethostbyname', 'getpwuid_r')
}

def check_text(text):
    warnings = []
    for raw in text.splitlines():
        line = _ANSI.sub('', raw)
        match = _WARNING.search(line)
        if match:
            warning = line[match.start():].strip()
            if warning not in _ALLOWED:
                raise ValueError('Unexpected compiler or linker warning: ' + warning)
            warnings.append(warning)
    return warnings

def check_log(path):
    # A missing/unreadable log is a failure, not evidence of no warnings.
    return check_text(Path(path).read_text(encoding='utf-8', errors='strict'))
