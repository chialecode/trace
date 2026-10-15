#!/usr/bin/env python3
"""测试清单与断言数：重构前后对账用。

静态扫描 Rust（`#[test]`/`#[tokio::test]`）、前端（Vitest `it`/`test`）和 Python
（`unittest` 的 `test_*`）测试，统计每个测试体内的断言宏/调用数。

用法：
  python scripts/test_inventory.py --out inventory.json
  python scripts/test_inventory.py --compare baseline.json [--map renames.json] [--retired retired.json]

`--compare` 比较当前扫描与基线：按名称（或映射后的名称）逐条核对，缺失或断言数下降即失败；
新增测试只在摘要里列出。映射文件是 {"旧名": "新名"}，键与值都用 `包名::测试名`；退役文件是 {"旧名": "理由"}。
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SKIP_DIRS = {"target", "node_modules", "dist", "artifacts", ".local", ".git", "gen", "__pycache__"}
RUST_ATTR = re.compile(r"#\[\s*(?:tokio::test|test)\b[^\]]*\]")
RUST_FN = re.compile(r"(?:pub\s+)?(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)")
RUST_ASSERT = re.compile(r"\b(?:debug_)?assert(?:_eq|_ne)?!|\bassert_matches!")
TS_CASE = re.compile(r"\b(?:it|test)(?:\.each\([^)]*\))?\(\s*(['\"`])((?:\\.|(?!\1).)*)\1")
TS_EXPECT = re.compile(r"\bexpect\s*[(.]")
PY_ASSERT = re.compile(r"\bself\.assert\w*\(|\bassert\s")


def files(suffixes: tuple[str, ...]):
    for path in sorted(ROOT.rglob("*")):
        if path.is_file() and path.suffix in suffixes and not (set(path.relative_to(ROOT).parts) & SKIP_DIRS):
            yield path


def crate_of(path: Path) -> str:
    """最近的带 Cargo.toml 的目录名作为包名（包名取目录名，足以对账）。"""
    for parent in path.parents:
        if (parent / "Cargo.toml").exists():
            return parent.name
        if parent == ROOT:
            break
    return "workspace"


def skip_rust_token(src: str, i: int) -> int | None:
    """若 i 处是注释/字符串/字符字面量，返回其结束位置，否则 None。"""
    n = len(src)
    if src.startswith("//", i):
        j = src.find("\n", i)
        return n if j < 0 else j
    if src.startswith("/*", i):
        depth, j = 1, i + 2
        while j < n and depth:
            if src.startswith("/*", j):
                depth, j = depth + 1, j + 2
            elif src.startswith("*/", j):
                depth, j = depth - 1, j + 2
            else:
                j += 1
        return j
    m = re.compile(r'b?r(#*)"').match(src, i)
    if m and (i == 0 or not (src[i - 1].isalnum() or src[i - 1] == "_")):
        end = src.find('"' + m.group(1), m.end())
        return n if end < 0 else end + 1 + len(m.group(1))
    if src[i] == '"':
        j = i + 1
        while j < n and src[j] != '"':
            j += 2 if src[j] == "\\" else 1
        return j + 1
    if src[i] == "'":
        m = re.compile(r"'(?:\\(?:x[0-9a-fA-F]{2}|u\{[0-9a-fA-F]+\}|.)|[^\\'])'").match(src, i)
        if m:
            return m.end()
    return None


def rust_body(src: str, start: int) -> tuple[int, int] | None:
    """从 start 起找到函数体 `{...}` 的区间。"""
    i, n = start, len(src)
    while i < n:
        skipped = skip_rust_token(src, i)
        if skipped is not None:
            i = skipped
            continue
        if src[i] == "{":
            depth, j = 1, i + 1
            while j < n and depth:
                skipped = skip_rust_token(src, j)
                if skipped is not None:
                    j = skipped
                    continue
                depth += (src[j] == "{") - (src[j] == "}")
                j += 1
            return i, j
        if src[i] == ";":
            return None
        i += 1
    return None


def strip_rust_noise(body: str) -> str:
    out, i, n = [], 0, len(body)
    while i < n:
        skipped = skip_rust_token(body, i)
        if skipped is not None:
            out.append(" ")
            i = skipped
        else:
            out.append(body[i])
            i += 1
    return "".join(out)


def scan_rust() -> dict:
    tests: dict[str, dict] = {}
    for path in files((".rs",)):
        src = path.read_text(encoding="utf-8", errors="replace")
        rel = path.relative_to(ROOT).as_posix()
        crate = crate_of(path)
        for attr in RUST_ATTR.finditer(src):
            m = RUST_FN.search(src, attr.end())
            if not m:
                continue
            span = rust_body(src, m.end())
            if not span:
                continue
            body = strip_rust_noise(src[span[0]:span[1]])
            key = f"{crate}::{m.group(1)}"
            if key in tests:
                key = f"{key}@{rel}"
            tests[key] = {"file": rel, "assertions": len(RUST_ASSERT.findall(body))}
    return tests


def scan_ts() -> dict:
    tests: dict[str, dict] = {}
    for path in files((".ts", ".tsx")):
        if ".spec." not in path.name and ".test." not in path.name:
            continue
        src = path.read_text(encoding="utf-8", errors="replace")
        rel = path.relative_to(ROOT).as_posix()
        cases = list(TS_CASE.finditer(src))
        for index, case in enumerate(cases):
            end = cases[index + 1].start() if index + 1 < len(cases) else len(src)
            key = f"{path.stem}::{case.group(2)}"
            if key in tests:
                key = f"{key}@{rel}"
            tests[key] = {"file": rel, "assertions": len(TS_EXPECT.findall(src[case.end():end]))}
    return tests


def scan_py() -> dict:
    tests: dict[str, dict] = {}
    for path in files((".py",)):
        if not path.name.startswith("test_"):
            continue
        src = path.read_text(encoding="utf-8", errors="replace")
        rel = path.relative_to(ROOT).as_posix()
        defs = list(re.finditer(r"^\s*def (test_\w+)\(", src, re.M))
        for index, d in enumerate(defs):
            end = defs[index + 1].start() if index + 1 < len(defs) else len(src)
            key = f"{path.stem}::{d.group(1)}"
            tests[key] = {"file": rel, "assertions": len(PY_ASSERT.findall(src[d.end():end]))}
    return tests


def scan() -> dict:
    groups = {"rust": scan_rust(), "frontend": scan_ts(), "python": scan_py()}
    summary = {g: {"tests": len(t), "assertions": sum(v["assertions"] for v in t.values())} for g, t in groups.items()}
    summary["total"] = {k: sum(v[k] for v in summary.values()) for k in ("tests", "assertions")}
    return {"summary": summary, **groups}


def compare(base: dict, cur: dict, mapping: dict[str, str], retired: dict[str, str] | None = None) -> list[str]:
    """Every baseline test must still exist (renamed or not) with no fewer assertions.

    `retired` lists baseline tests whose subject was deliberately removed, each with its reason;
    it can only name tests that are in the baseline and no longer run.
    """
    errors: list[str] = []
    retired = retired or {}
    known = {name for group in ("rust", "frontend", "python") for name in base[group]}
    for old, reason in retired.items():
        if old not in known:
            errors.append(f"退役条目不在基线中：{old}")
        elif not str(reason).strip():
            errors.append(f"退役条目缺少理由：{old}")
    for group in ("rust", "frontend", "python"):
        now = cur[group]
        for old, info in base[group].items():
            if old in retired:
                if mapping.get(old, old) in now:
                    errors.append(f"[{group}] 已退役的测试仍在运行 {old}")
                continue
            new = mapping.get(old, old)
            if new not in now:
                errors.append(f"[{group}] 缺失 {old}" + (f"（映射到 {new}）" if new != old else ""))
            elif now[new]["assertions"] < info["assertions"]:
                errors.append(f"[{group}] 断言数下降 {old}: {info['assertions']} -> {now[new]['assertions']}")
    return errors


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path)
    ap.add_argument("--compare", type=Path)
    ap.add_argument("--map", type=Path)
    ap.add_argument("--retired", type=Path, help='{"旧名": "退役理由"}：被有意移除功能的基线测试')
    args = ap.parse_args()
    cur = scan()
    print(json.dumps(cur["summary"], ensure_ascii=False))
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(cur, ensure_ascii=False, indent=1, sort_keys=True), encoding="utf-8")
    if args.compare:
        base = json.loads(args.compare.read_text(encoding="utf-8"))
        mapping = json.loads(args.map.read_text(encoding="utf-8")) if args.map else {}
        retired = json.loads(args.retired.read_text(encoding="utf-8")) if args.retired else {}
        errors = compare(base, cur, mapping, retired)
        added = sum(len(cur[g]) for g in ("rust", "frontend", "python")) - sum(len(base[g]) for g in ("rust", "frontend", "python"))
        print(f"基线 {base['summary']['total']} -> 当前 {cur['summary']['total']}（净新增测试 {added}，退役 {len(retired)}）")
        for e in errors:
            print("[FAIL]", e)
        return 1 if errors else 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
