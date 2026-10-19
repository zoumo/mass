#!/usr/bin/env python3
"""Check links in the public source documentation without private evidence trees."""
import json
from pathlib import Path
import re
from urllib.parse import unquote

PROJECT = Path(__file__).resolve().parents[2]


def anchors(text):
    result = set()
    counts = {}
    for heading in re.findall(r'^#{1,6}\s+(.+?)\s*#*$', text, re.M):
        heading = re.sub(r'[^\w\-\s]', '', heading.lower()).replace(' ', '-')
        count = counts.get(heading, 0)
        counts[heading] = count + 1
        result.add(heading + (f'-{count}' if count else ''))
    result.update(re.findall(r'(?:id|name)=["\x27]([^"\x27]+)', text))
    return result


def inspect():
    files = [PROJECT / name for name in ['README.md', 'AGENTS.md', 'GLOSSARY.md', 'CLAUDE.md',
                                        'skills/README.md', 'mass-rs/README.md', 'mass-go/README.md']]
    for root in ['harness', 'mass-go/docs', 'mass-go/examples', 'mass-go/tests']:
        files += sorted(path for path in (PROJECT / root).rglob('*.md')
                        if 'node_modules' not in path.parts)
    assert not (PROJECT / 'docs').exists(), 'Root docs/ was retired; use harness/'
    assert (PROJECT / 'harness/index.md').is_file(), 'Missing unified development entry point'
    failures = []
    checked = 0
    for path in files:
        text = path.read_text()
        for href in re.findall(r'\]\(([^\s)]+)\)', text):
            if '://' in href or href.startswith(('mailto:', '/')):
                continue
            local, _, fragment = unquote(href).partition('#')
            target = (path.parent / local).resolve() if local else path
            checked += 1
            if not target.is_relative_to(PROJECT):
                failures.append(f'{path.relative_to(PROJECT)}: link outside source {href}')
            elif any(part in ('.scratch', '.gsd') for part in target.relative_to(PROJECT).parts):
                failures.append(f'{path.relative_to(PROJECT)}: private evidence link {href}')
            elif not target.exists():
                failures.append(f'{path.relative_to(PROJECT)}: missing {href}')
            elif fragment and target.suffix == '.md' and fragment not in anchors(target.read_text()):
                failures.append(f'{path.relative_to(PROJECT)}: missing anchor {href}')
    assert not failures, '\n'.join(failures)
    return {'currentDocuments': len(files), 'localLinks': checked}


if __name__ == '__main__':
    print(json.dumps(inspect(), indent=2))
