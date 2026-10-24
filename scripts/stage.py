#!/usr/bin/env python3
"""Stage evidence through one entry: test | package | bench | report | check.

Every command writes its receipt and raw log under artifacts/evidence-runs/<run>/ (ignored by Git;
failures are kept). `report` validates the run against the current product inputs, the plan in
docs/delivery/requirements-map.json and the repository's schema version, and only a passing report
is copied to docs/evidence/gate/report.json, the one public summary. Documents never enter the
input fingerprint, so editing them does not make a package or a test run stale.

  python scripts/stage.py test [--run NAME]
  python scripts/stage.py package [--run NAME]
  python scripts/stage.py bench [--run NAME] [--idle S] [--seconds S] [--entries N]
  python scripts/stage.py report [--run NAME]
  python scripts/stage.py check [REPORT]

`package`, `bench` and `report` default to the newest run; `test` starts a new one.
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import sys
import time
from pathlib import Path

import evidence

ROOT = evidence.ROOT
RUNS = ROOT / "artifacts" / "evidence-runs"
REPORT = ROOT / "docs" / "evidence" / "gate" / "report.json"
PY = sys.executable
PNPM = [shutil.which("corepack") or "corepack.cmd", "pnpm"]

TEST_COMMANDS = {
    # The end-to-end tests start the adapter and daemon executables from target/debug, and
    # `cargo test` alone does not rebuild those; a stale executable would be tested instead.
    "rust-build": ["cargo", "build", "--workspace", "--locked"],
    "rust": ["cargo", "test", "--workspace", "--locked"],
    "frontend": [*PNPM, "-C", "apps/desktop", "run", "verify"],
    "scripts": [PY, "-m", "unittest", "discover", "-s", "scripts/tests", "-v"],
}
REPORT_COMMANDS = {
    "docs": [PY, "scripts/check_docs.py"],
    "publication": [PY, "scripts/check_publication.py"],
}


def new_run(name: str | None) -> Path:
    run = RUNS / (name or time.strftime("%Y%m%d-%H%M%S"))
    (run / "receipts").mkdir(parents=True, exist_ok=True)
    return run


def existing_run(name: str | None) -> Path:
    if name:
        run = RUNS / name
    else:
        candidates = [p for p in RUNS.glob("*") if (p / "receipts").is_dir()]
        if not candidates:
            raise SystemExit("no evidence run yet; start one with `stage.py test`")
        run = max(candidates, key=lambda p: p.stat().st_mtime)
    if not (run / "receipts").is_dir():
        raise SystemExit(f"{run} is not an evidence run")
    return run


def receipt(run: Path, kind: str, name: str, command: list[str]) -> int:
    out = run / "receipts" / f"{kind}-{name}.json"
    print(f"== {kind}/{name}: {' '.join(command)}", flush=True)
    code = evidence.run_receipt(kind, f"{kind}-{name}", out, command)
    print(f"   exit {code}", flush=True)
    return code


def test(args) -> int:
    run = new_run(args.run)
    print(f"run: {run.name}")
    failed = [name for name, command in TEST_COMMANDS.items() if receipt(run, "test", name, command)]
    return 1 if failed else 0


def package(args) -> int:
    run = existing_run(args.run)
    code = receipt(run, "package", "portable", [PY, "scripts/package_windows.py"])
    if code:
        return code
    log = (run / "receipts" / "package-portable.log").read_text(encoding="utf8", errors="replace")
    found = re.findall(r"\[PASS\] (dist[/\\][^（\r\n]+)", log)
    if not found:
        print("package folder not reported by package_windows.py")
        return 1
    folder = found[-1].strip().replace("\\", "/")
    (run / "package-dir.json").write_text(json.dumps({"directory": folder}), encoding="utf8")
    return receipt(run, "package", "check", [PY, "scripts/check_package.py", folder])


def bench(args) -> int:
    run = existing_run(args.run)
    package_dir = json.loads((run / "package-dir.json").read_text(encoding="utf8"))["directory"]
    command = [
        PY, "scripts/measure.py", str(run / "bench"), "--package", package_dir,
        "--idle", str(args.idle), "--seconds", str(args.seconds), "--entries", str(args.entries),
    ]
    return receipt(run, "bench", "resources", command)


def report(args) -> int:
    run = existing_run(args.run)
    for name, command in REPORT_COMMANDS.items():
        receipt(run, "report", name, command)
    receipts = [evidence.read(p) for p in sorted((run / "receipts").glob("*.json"))]
    package_dir = ROOT / json.loads((run / "package-dir.json").read_text(encoding="utf8"))["directory"]
    benchmark = evidence.read(run / "bench" / "benchmark.json")
    out = run / "report.json"
    errors = evidence.assemble(receipts, package_dir, benchmark, out)
    for error in errors:
        print("[FAIL]", error)
    if errors:
        print(f"report kept at {out}; not published")
        return 1
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(out, REPORT)
    print(f"[PASS] {REPORT.relative_to(ROOT).as_posix()} (automated evidence only; product acceptance not-run)")
    return 0


def check(args) -> int:
    errors = evidence.validate(evidence.read(Path(args.report)))
    for error in errors:
        print("[FAIL]", error)
    if not errors:
        print("automated evidence passed; A review pending; product acceptance not-run")
    return 1 if errors else 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="action", required=True)
    for name in ("test", "package", "bench", "report"):
        step = sub.add_parser(name)
        step.add_argument("--run", help="run folder name under artifacts/evidence-runs")
        if name == "bench":
            step.add_argument("--idle", type=int, default=1030)
            step.add_argument("--seconds", type=int, default=3600)
            step.add_argument("--entries", type=int, default=100000)
    sub.add_parser("check").add_argument("report", nargs="?", default=str(REPORT))
    args = parser.parse_args()
    return {"test": test, "package": package, "bench": bench, "report": report, "check": check}[args.action](args)


if __name__ == "__main__":
    sys.exit(main())
