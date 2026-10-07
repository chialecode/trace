"""Check public working files AND staged blobs; never print matched secrets."""
from __future__ import annotations
import argparse
from pathlib import Path
import re
import subprocess

PRIVATE_DIRS = {'reference', '.local', 'profiles', 'archives', 'artifacts', 'tmp'}
TEXT_SUFFIXES = {'.md', '.json', '.yaml', '.yml', '.toml', '.py', '.ts', '.vue', '.css', '.rs', '.txt', '.html', '.lock'}
RULES = {
    'personal-absolute-path': re.compile(r'(?i)\b[A-Z]:[\\/](?:Users|Code)[\\/]|/(?:Users|home)/[\w.-]+/'),
    'private-key': re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
    'github-token': re.compile(r'\bgh[pousr]_[A-Za-z0-9]{30,}\b'),
}


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL)


def inspect(path: str, raw: bytes) -> list[str]:
    p = Path(path)
    reasons = []
    if any(part in PRIVATE_DIRS for part in p.parts) or (p.name.startswith('.env') and p.name != '.env.example') or p.suffix in {'.db', '.sqlite', '.sqlite3'}:
        reasons.append('private-file')
    if p.suffix in TEXT_SUFFIXES or p.name in {'AGENTS.md', 'README.md'}:
        text = raw.decode('utf-8', errors='replace')
        reasons.extend(name for name, regex in RULES.items() if regex.search(text))
    return reasons


def validate(root: Path) -> list[str]:
    problems = []
    tracked = set(git(root, 'ls-files', '-z').decode().split('\0')) - {''}
    public = set(git(root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard').decode().split('\0')) - {''}
    for path in sorted(public):
        file = root / path
        if file.is_file() and not file.is_symlink():
            for rule in inspect(path, file.read_bytes()):
                problems.append(f'worktree:{path}: {rule}')
        if path in tracked:
            try:
                staged = git(root, 'show', ':' + path)
            except subprocess.CalledProcessError:
                continue
            for rule in inspect(path, staged):
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
