"""Check public working files AND staged blobs; never print matched secrets."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import re
import subprocess
from urllib.parse import urlsplit

PRIVATE_DIRS = {'reference', '.local', 'profiles', 'archives', 'artifacts', 'tmp'}
TEXT_SUFFIXES = {'.md', '.json', '.yaml', '.yml', '.toml', '.py', '.ts', '.vue', '.css', '.rs', '.txt', '.html', '.lock'}
RULES = {
    'personal-absolute-path': re.compile(r'(?i)\b[A-Z]:[\\/](?:Users|Code)[\\/]|/(?:Users|home)/[\w.-]+/'),
    'private-key': re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
    'github-token': re.compile(r'\bgh[pousr]_[A-Za-z0-9]{30,}\b'),
}


#: Path words of profile links that name a page kind, not an account.
LINK_WORDS = {'profile', 'channel', 'videos', 'shorts', 'status', 'explore', 'people', 'photo', 'video', 'space', 'detail'}


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL)


def _strings(value):
    if isinstance(value, dict):
        for item in value.values():
            yield from _strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from _strings(item)
    elif isinstance(value, (str, int)) and not isinstance(value, bool):
        yield str(value)


def private_targets(root: Path) -> re.Pattern | None:
    """The user's real test targets (accounts, handles, ids), read only from the ignored local
    target files, so a test or document cannot quietly carry them. Absent files mean no rule."""
    names = set()
    for path in sorted((root / '.local' / 'site-check').glob('*targets*.json')):
        try:
            data = json.loads(path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            continue
        for value in _strings(data.get('targets') if isinstance(data, dict) else None):
            parts = urlsplit(value).path.split('/') if value.startswith(('http://', 'https://')) else [value]
            names.update(p.lstrip('@') for p in parts if p.lstrip('@').lower() not in LINK_WORDS)
    names = sorted(n for n in names if len(n) >= 6 and re.fullmatch(r'[\w.-]+', n))
    if not names:
        return None
    return re.compile(r'(?i)(?<![\w])(?:' + '|'.join(map(re.escape, names)) + r')(?![\w])')


def inspect(path: str, raw: bytes, targets: re.Pattern | None = None) -> list[str]:
    p = Path(path)
    reasons = []
    if any(part in PRIVATE_DIRS for part in p.parts) or (p.name.startswith('.env') and p.name != '.env.example') or p.suffix in {'.db', '.sqlite', '.sqlite3'}:
        reasons.append('private-file')
    if p.suffix in TEXT_SUFFIXES or p.name in {'AGENTS.md', 'README.md'}:
        text = raw.decode('utf-8', errors='replace')
        reasons.extend(name for name, regex in RULES.items() if regex.search(text))
        if targets is not None and targets.search(text):
            reasons.append('private-target')
    return reasons


def validate(root: Path) -> list[str]:
    problems = []
    targets = private_targets(root)
    tracked = set(git(root, 'ls-files', '-z').decode().split('\0')) - {''}
    public = set(git(root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard').decode().split('\0')) - {''}
    for path in sorted(public):
        file = root / path
        if file.is_file() and not file.is_symlink():
            for rule in inspect(path, file.read_bytes(), targets):
                problems.append(f'worktree:{path}: {rule}')
        if path in tracked:
            try:
                staged = git(root, 'show', ':' + path)
            except subprocess.CalledProcessError:
                continue
            for rule in inspect(path, staged, targets):
                problems.append(f'index:{path}: {rule}')
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    args = ap.parse_args()
    try:
        problems = validate(args.root)
    except subprocess.CalledProcessError:
        print('failed: Git index unavailable; public-content check not completed')
        return 1
    for p in problems:
        print(p)
    print(f'{"failed" if problems else "passed"}: {len(problems)} public-content findings (heuristic; images need review)')
    return 1 if problems else 0


if __name__ == '__main__':
    raise SystemExit(main())
