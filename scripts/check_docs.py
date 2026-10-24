#!/usr/bin/env python3
"""Validate TRACE-owned Markdown, navigation and requirement coverage; no writes/network."""
from __future__ import annotations

import argparse
import datetime as dt
import json
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

IGNORED = {'.git', 'node_modules', 'vendor', 'target', 'dist', 'build', '.venv', '__pycache__',
           '.local', 'tmp', 'artifacts', 'profiles', 'archives', 'coverage', 'test-results', 'reference'}
DOC_STATES = {'active', 'draft', 'reference', 'superseded'}


def stage_number(value: object) -> int | None:
    if isinstance(value, str) and re.fullmatch(r'M(?:0|[1-9][0-9]*)', value):
        return int(value[1:])
    return None


def prose(raw: str) -> str:
    lines, fence = [], None
    for line in raw.splitlines():
        marker = re.match(r'^\s*(`{3,}|~{3,})', line)
        if marker:
            token = marker.group(1)
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            lines.append('')
        elif fence is None:
            lines.append(line)
    return '\n'.join(lines)


def anchors(raw: str) -> set[str]:
    body = prose(raw)
    result = set(re.findall(r'<a\s+(?:id|name)=[\"\']([^\"\']+)', body))
    counts: dict[str, int] = {}
    for title in re.findall(r'^#{1,6}\s+(.+?)\s*#*$', body, re.M):
        title = re.sub(r'\[([^\]]+)\]\([^)]*\)', r'\1', title)
        slug = re.sub(r'[^\w\-\s]', '', title.lower(), flags=re.UNICODE).replace(' ', '-')
        n = counts.get(slug, 0)
        counts[slug] = n + 1
        result.add(slug if not n else f'{slug}-{n}')
    return result


def links(raw: str) -> list[str]:
    body = re.sub(r'`+[^`\n]*`+', '', prose(raw))
    inline = re.findall(r'!?\[[^\]\n]*\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+"[^"\n]*")?\s*\)', body)
    reference = re.findall(r'^\s*\[[^\]]+\]:\s*(<[^>]+>|\S+)', body, re.M)
    return [url.strip('<>') for url in inline + reference]


def validate(root: Path) -> tuple[list[str], dict[str, int]]:
    errors: list[str] = []

    def problem(message: str) -> None:
        errors.append(message)

    def read_json(relative: str):
        try:
            return json.loads((root / relative).read_text(encoding='utf-8'))
        except (OSError, ValueError) as exc:
            problem(f'{relative}: cannot read JSON: {exc}')
            return {}

    docs: dict[str, str] = {}
    for path in sorted(root.rglob('*.md')):
        if any(part in IGNORED for part in path.relative_to(root).parts):
            continue
        rel = path.relative_to(root).as_posix()
        try:
            raw = path.read_text(encoding='utf-8')
        except (OSError, UnicodeError) as exc:
            problem(f'{rel}: cannot read UTF-8: {exc}')
            continue
        docs[rel] = raw
        if '\ufffd' in raw:
            problem(f'{rel}: Unicode replacement character')
        if re.search(r'(?i)\b[A-Z]:[\\/](?:Users|Code)[\\/]|/Users/[^/\s]+/|/home/[^/\s]+/', raw):
            problem(f'{rel}: personal absolute path')

    registry = read_json('docs/governance/document-registry.json')
    current_stage = registry.get('currentStage')
    current_number = stage_number(current_stage)
    if current_number is None:
        problem('registry: currentStage must be M followed by a nonnegative integer')
    current_plan = registry.get('currentPlan')
    if not current_plan or current_plan not in docs:
        problem('registry: currentPlan must point to an existing document')
    plans = [e for e in registry.get('documents', [])
             if e.get('kind') == 'execution-plan' and e.get('status') == 'active']
    if len(plans) != 1 or plans[0].get('path') != current_plan:
        problem('registry: exactly one active execution-plan must match currentPlan')
    seen: set[str] = set()
    for entry in registry.get('documents', []):
        path = entry.get('path', '')
        retirement = entry.get('retireAfter')
        if retirement is not None:
            retirement_number = stage_number(retirement)
            if retirement_number is None:
                problem(f'{path}: invalid retireAfter stage')
            elif current_number is not None and current_number > retirement_number and path in docs:
                problem(f'{path}: expired document must be distilled and retired')
        for key in ('path', 'kind', 'status', 'owner', 'scope', 'reviewed', 'basis'):
            if not isinstance(entry.get(key), str) or not entry[key].strip():
                problem(f'{path}: missing registry field {key}')
        if path in seen:
            problem(f'{path}: duplicate registry entry')
        seen.add(path)
        if path not in docs:
            problem(f'{path}: registered Markdown does not exist')
        if entry.get('status') not in DOC_STATES:
            problem(f'{path}: invalid document status')
        try:
            dt.date.fromisoformat(entry.get('reviewed', ''))
        except (ValueError, TypeError):
            problem(f'{path}: invalid reviewed date')
        if entry.get('status') == 'superseded' and entry.get('supersededBy') not in docs:
            problem(f'{path}: missing valid supersededBy')
    for path in sorted(set(docs) - seen):
        problem(f'{path}: Markdown not registered')

    graph: dict[str, set[str]] = {path: set() for path in docs}
    anchor_map = {path: anchors(raw) for path, raw in docs.items()}
    link_count = 0
    for source, raw in docs.items():
        for url in links(raw):
            parsed = urlsplit(url)
            if parsed.scheme or parsed.netloc:
                continue
            link_count += 1
            path = (root / source).parent / unquote(parsed.path) if parsed.path else root / source
            path = path.resolve()
            if not path.is_relative_to(root):
                problem(f'{source}: link leaves repository: {url}')
                continue
            if not path.exists():
                problem(f'{source}: broken link: {url}')
                continue
            dest = path.relative_to(root).as_posix()
            if dest in docs:
                graph[source].add(dest)
                if parsed.fragment and unquote(parsed.fragment) not in anchor_map[dest]:
                    problem(f'{source}: missing anchor: {url}')
        # Claude Code's canonical import syntax is not a Markdown link.
        if source == 'CLAUDE.md':
            if raw.strip() != '@AGENTS.md':
                problem('CLAUDE.md: expected only @AGENTS.md')
            graph[source].add('AGENTS.md')

    reachable, pending = set(), [p for p in docs if '/' not in p]
    while pending:
        source = pending.pop()
        if source not in reachable:
            reachable.add(source)
            pending.extend(graph.get(source, set()) - reachable)
    for path in sorted(set(docs) - reachable):
        problem(f'{path}: no navigation path from root entries')

    req_text = docs.get('docs/requirements.md', '')
    design_text = docs.get('docs/design.md', '')
    adr_text = docs.get('docs/decisions/architecture-decisions.md', '')
    definitions = {
        'requirements': re.findall(r'^\| ((?:FR|NFR)-[A-Z]+-\d{3}) \|', req_text, re.M),
        'acceptance': re.findall(r'^\| (AC-\d{2}) \|', req_text, re.M),
        'invariants': re.findall(r'^\| (INV-\d{2}) \|', design_text, re.M),
        'decisions': re.findall(r'^\| (ADR-\d{3}) \|', adr_text, re.M),
    }
    baseline = read_json('docs/governance/compatibility-baseline.json')
    for kind, ids in definitions.items():
        if len(ids) != len(set(ids)):
            problem(f'{kind}: duplicate definition IDs')
        for missing in sorted(set(baseline.get(kind, [])) - set(ids)):
            problem(f'{kind}: removed stable definition {missing}')
    for path, ids in baseline.get('anchors', {}).items():
        for fragment in ids:
            if fragment not in anchor_map.get(path, set()):
                problem(f'{path}: removed stable anchor {fragment}')

    statuses = {m[1]: m[2].strip() for m in re.finditer(r'^\| (FR-[A-Z]+-\d{3}) \| ([^|]+) \|', req_text, re.M)}

    def status_of(rid: str) -> str:
        text = statuses.get(rid, '已实现')  # non-functional rows are always in force
        return 'current' if text.startswith('本阶段') else 'pending' if text.startswith('待实现') else 'implemented'

    mapping = read_json('docs/delivery/requirements-map.json')
    mapped: set[str] = set()
    for row in mapping.get('requirements', []):
        rid = row.get('id')
        if rid in mapped:
            problem(f'{rid}: duplicate requirement mapping')
        mapped.add(rid)
        if rid not in definitions['requirements']:
            problem(f'{rid}: mapping without requirement definition')
        expected = status_of(rid)
        if row.get('status') != expected:
            problem(f'{rid}: map status {row.get("status")!r} differs from requirements.md ({expected!r})')
        scope = row.get('current') or {}
        if scope.get('plan') != mapping.get('currentPlan'):
            problem(f'{rid}: current scope is not bound to the current plan')
        if scope.get('applicable') != (expected != 'pending'):
            problem(f'{rid}: applicability disagrees with status')
        if scope.get('applicable'):
            if not scope.get('implementation') or not scope.get('testCases') or not scope.get('workPackages') or not scope.get('caseGroups'):
                problem(f'{rid}: applicable requirement lacks implementation, tests, work packages or case groups')
            for path in scope.get('implementation', []):
                if not (root / path).is_file():
                    problem(f'{rid}: implementation file does not exist: {path}')
        elif any(scope.get(key) for key in ('implementation', 'testCases', 'workPackages', 'caseGroups')):
            problem(f'{rid}: a pending requirement cannot claim implementation or tests')
        if row.get('designAnchor') not in anchor_map.get('docs/design.md', set()):
            problem(f'{rid}: invalid design anchor')
        if not row.get('acceptance') or not set(row['acceptance']) <= set(definitions['acceptance']):
            problem(f'{rid}: missing or unknown acceptance IDs')
        if not row.get('scopeNote'):
            problem(f'{rid}: missing scope note')
    for rid in sorted(set(definitions['requirements']) - mapped):
        problem(f'{rid}: requirement missing from delivery map')

    return errors, {'documents': len(docs), 'local_links': link_count,
                    'requirements': len(definitions['requirements']),
                    'acceptance': len(definitions['acceptance']),
                    'invariants': len(definitions['invariants']),
                    'decisions': len(definitions['decisions'])}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    errors, counts = validate(args.root.resolve())
    for error in errors:
        print(f'ERROR: {error}', file=sys.stderr)
    print(json.dumps({'result': 'failed' if errors else 'passed', **counts, 'errors': len(errors)}, ensure_ascii=False))
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
