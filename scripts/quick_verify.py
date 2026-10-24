#!/usr/bin/env python3
"""Risk-layered quick checks. This entry never substitutes for stage evidence.

It runs seconds-to-minutes checks for the affected area, writes a clearly marked summary under
artifacts/quick-verify/, and never starts a resource benchmark, a real-site run or a package build.
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable

ROOT = Path(__file__).resolve().parent.parent
PNPM = [shutil.which("corepack") or "corepack.cmd", "pnpm"]


def suite_commands(suite: str, rust_tests: list[str]) -> list[list[str]]:
    ui = [
        [*PNPM, "-C", "apps/desktop", "run", "format:check"],
        [*PNPM, "-C", "apps/desktop", "run", "typecheck"],
        [*PNPM, "-C", "apps/desktop", "test"],
    ]
    evidence = [[sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-v"]]
    structure = [
        [sys.executable, "scripts/check_structure.py"],
        [sys.executable, "scripts/check_generated_types.py"],
        [sys.executable, "scripts/lint_lines.py"],
    ]
    backend = [
        ["cargo", "fmt", "--all", "--", "--check"],
        [
            "cargo",
            "clippy",
            "-p",
            "trace-core",
            "-p",
            "trace-contracts",
            "-p",
            "trace-site-support",
            "--locked",
            "--all-targets",
            "--",
            "-D",
            "warnings",
        ],
    ]
    if rust_tests:
        backend.extend(
            ["cargo", "test", "-p", "trace-core", "--locked", pattern, "--", "--nocapture"]
            for pattern in rust_tests
        )
    else:
        backend.append(
            [
                "cargo",
                "check",
                "-p",
                "trace-core",
                "-p",
                "trace-contracts",
                "-p",
                "trace-site-support",
                "--locked",
            ]
        )
    groups = {
        "ui": ui,
        "backend": backend,
        "evidence": evidence,
        "structure": structure,
    }
    if suite == "all":
        return [command for name in ("ui", "backend", "evidence", "structure") for command in groups[name]]
    return groups[suite]


def execute(
    commands: list[list[str]],
    runner: Callable[..., subprocess.CompletedProcess] = subprocess.run,
) -> tuple[int, list[dict]]:
    results = []
    for command in commands:
        started = time.monotonic()
        completed = runner(
            command,
            cwd=ROOT,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        elapsed = round(time.monotonic() - started, 3)
        output = f"{completed.stdout or ''}{completed.stderr or ''}"
        result = {
            "command": command,
            "exitCode": completed.returncode,
            "seconds": elapsed,
            "outputTail": output[-2000:],
        }
        results.append(result)
        if completed.returncode:
            return completed.returncode, results
    return 0, results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--suite", choices=["ui", "backend", "evidence", "structure", "all"], required=True)
    parser.add_argument(
        "--rust-test",
        action="append",
        default=[],
        help="Capability-named trace-core test filter; repeat for more than one.",
    )
    args = parser.parse_args(argv)
    commands = suite_commands(args.suite, args.rust_test)
    run = (
        ROOT
        / "artifacts"
        / "quick-verify"
        / f"{time.strftime('%Y%m%d-%H%M%S')}-{time.time_ns() % 1_000_000_000:09d}"
    )
    run.mkdir(parents=True, exist_ok=False)
    code, results = execute(commands)
    summary = {
        "marker": "QUICK ONLY",
        "completeStageEvidence": False,
        "longBenchmark": False,
        "realSiteRequests": 0,
        "suite": args.suite,
        "rustTests": args.rust_test,
        "exitCode": code,
        "results": results,
    }
    path = run / "summary.json"
    path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf8")
    print(
        json.dumps(
            {
                "marker": "QUICK ONLY",
                "suite": args.suite,
                "exitCode": code,
                "summary": path.relative_to(ROOT).as_posix(),
                "completeStageEvidence": False,
            },
            ensure_ascii=False,
        )
    )
    if code:
        for result in results:
            if result["exitCode"]:
                print("\n".join(result["command"]), file=sys.stderr)
                print(result["outputTail"], file=sys.stderr)
                break
    return 1 if code else 0


if __name__ == "__main__":
    raise SystemExit(main())
