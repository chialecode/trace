"""Structural limits of the code base (docs/design/code-structure-refactor.md §8).

Runs the checks that formatters and Clippy do not cover:
- the IPC layer holds no SQL;
- Rust lines over 150 characters are only unbreakable literals (scripts/lint_lines.py);
- non-test Rust files stay within 800 lines, adapters within 600 (scripts/check_file_sizes.py);
- generated TypeScript types and UI command names match the Rust contracts
  (scripts/check_generated_types.py).

Function length (<= 120 lines) is enforced by `clippy::too_many_lines` in the workspace lints.

Usage: python scripts/check_structure.py
"""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
IPC_DIR = ROOT / "crates" / "core" / "src" / "ipc"
SQL_MARKERS = re.compile(
    r"rusqlite|\.execute\(|query_row|\.prepare\(|SELECT\s|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM"
)


def ipc_sql() -> list[str]:
    hits = []
    for path in sorted(IPC_DIR.rglob("*.rs")):
        for number, line in enumerate(path.read_text(encoding="utf-8").split("\n"), 1):
            if SQL_MARKERS.search(line):
                hits.append(f"{path.relative_to(ROOT).as_posix()}:{number}: {line.strip()[:100]}")
    return hits


def run_script(name: str) -> int:
    return subprocess.run([sys.executable, str(ROOT / "scripts" / name)], cwd=ROOT).returncode


def main() -> int:
    failed = False
    hits = ipc_sql()
    if hits:
        failed = True
        print("IPC layer must not hold SQL:")
        print("\n".join(hits))
    for script in ("lint_lines.py", "check_file_sizes.py", "check_generated_types.py"):
        if run_script(script) != 0:
            failed = True
    if failed:
        return 1
    print("structure checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
