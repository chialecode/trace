#!/usr/bin/env python3
"""Prepare a bounded authentication probe, with zero network or browser access.

Only the selected site's COOKIE_FILE key is read from .env.local. The secret
stays in its user-managed file and is never copied into JSON, stdout or argv.
This tool cannot execute a site request or grant authorization.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]


class PreparationError(ValueError):
    pass


def unlinked(path: Path) -> None:
    for part in (path, *path.parents):
        if part.is_symlink() or (hasattr(part, 'is_junction') and part.is_junction()):
            raise PreparationError('linked paths are not accepted')


def prepare(root: Path, site: str, output: str, package: str) -> dict:
    if site not in ('bilibili', 'douyin'):
        raise PreparationError('unsupported site')
    key = f'TRACE_TEST_{site.upper()}_COOKIE_FILE'
    config = root / '.env.local'
    unlinked(config)
    if not config.is_file():
        raise PreparationError('missing .env.local; follow USER-ACTIONS ACT-002')
    if config.stat().st_size > 65536:
        raise PreparationError('local configuration exceeds budget')
    values = []
    for line in config.read_text(encoding='utf-8-sig').splitlines():
        name, sep, value = line.strip().partition('=')
        if sep and name.strip() == key:
            value = value.strip()
            if len(value) >= 2 and value[0] in ('"', "'") and value[-1] == value[0]:
                value = value[1:-1]
            values.append(value)
    if len(values) != 1 or not values[0]:
        raise PreparationError(f'missing or duplicate {key}; follow USER-ACTIONS ACT-002')
    cookie = root / values[0]
    unlinked(cookie)
    if not cookie.is_file():
        raise PreparationError('missing site cookie file; follow USER-ACTIONS ACT-002')
    if not 0 < cookie.stat().st_size <= 16384:
        raise PreparationError('empty or oversized cookie file; follow USER-ACTIONS ACT-002')
    secret = cookie.read_text(encoding='utf-8-sig').strip()
    if not secret or '\r' in secret or '\n' in secret or secret.lower().startswith(('cookie:', 'curl ')) or '=' not in secret:
        raise PreparationError('expected one Cookie header value, without the Cookie: prefix')
    # Validate the explicit local locations before making any output directory.
    target = root / output
    unlinked(target)
    target = target.resolve()
    if not target.is_relative_to((root / '.local/site-check').resolve()) or target.exists():
        raise PreparationError('choose a new directory below .local/site-check')
    binaries = root / package
    unlinked(binaries)
    if not (binaries / 'trace-daemon.exe').is_file() or not (binaries / f'adapters/trace-adapter-{site}.exe').is_file():
        raise PreparationError('the selected package does not contain the required programs')
    # The existing daemon takes only a credential path. No secret in either file.
    spec = {'platform': site, 'max_http_requests': 3, 'deadline_seconds': 30,
            'min_interval_ms': 2000, 'credential_file': str(cookie.resolve()),
            'requests': [{'method': 'probe_auth', 'params': {}}]}
    profile = {'archive_id': f'local-site-check-{site}', 'data_root': str(target / 'data'),
               'archive_root': str(target / 'archive'), 'vault_dir': str(target / 'vault'),
               'adapter_dir': str(binaries.resolve() / 'adapters'),
               'test_allowed_hosts': [], 'extra_media_hosts': [], 'allow_media_path': False}
    target.mkdir(parents=True, exist_ok=False)
    for name, content in [('request.json', spec), ('profile.json', profile)]:
        (target / name).write_text(json.dumps(content, ensure_ascii=False, indent=2) + '\n', encoding='utf8')
    return {'status': 'prepared', 'site': site, 'directory': target.relative_to(root.resolve()).as_posix(),
            'networkRequests': 0, 'executionAuthorized': False, 'loginVerified': False}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--site', required=True, choices=['bilibili', 'douyin'])
    parser.add_argument('--out', required=True, help='new directory below .local/site-check')
    parser.add_argument('--package', required=True, help='explicit local portable package')
    args = parser.parse_args()
    try:
        result = prepare(ROOT, args.site, args.out, args.package)
    except PreparationError as exc:
        print(json.dumps({'status': 'blocked', 'reason': str(exc), 'networkRequests': 0, 'action': 'USER-ACTIONS ACT-002'}))
        return 2
    except (OSError, UnicodeError, ValueError):
        # Filesystem/decoder exceptions can contain private paths or input bytes.
        print(json.dumps({'status': 'blocked', 'reason': 'local input could not be read or prepared', 'networkRequests': 0, 'action': 'USER-ACTIONS ACT-002'}))
        return 2
    print(json.dumps(result))
    return 0


if __name__ == '__main__':
    sys.exit(main())
