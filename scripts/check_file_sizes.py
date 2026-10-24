"""Report source files that outgrew the structure limits.

docs/design/code-structure-refactor.md §8: a non-test Rust file stays within 800 lines
(adapters within 600). Test code does not count: whole test files, and the inline test
module that starts at the first top-level `#[cfg(test)]`.

Usage: python scripts/check_file_sizes.py [--limit 800] [--adapter-limit 600]
"""

import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ROOTS = ["crates", "adapters", "apps"]
SKIP_PARTS = {"target", "node_modules", "dist"}


def is_test_file(path: Path) -> bool:
    name = path.name
    return (
        "tests" in path.parts
        or name.endswith("_tests.rs")
        or name == "tests.rs"
        or name.startswith("test_")
    )


def production_lines(path: Path) -> int:
    count = 0
    for line in path.read_text(encoding="utf-8").split("\n"):
        if line.startswith("#[cfg(test)]"):
            break
        count += 1
    return count


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--limit", type=int, default=800)
    parser.add_argument("--adapter-limit", type=int, default=600)
    args = parser.parse_args()
    found = 0
    for root in ROOTS:
        for path in sorted((ROOT / root).rglob("*.rs")):
            if not SKIP_PARTS.isdisjoint(path.parts) or is_test_file(path):
                continue
            limit = args.adapter_limit if "adapters" in path.parts else args.limit
            lines = production_lines(path)
            if lines > limit:
                found += 1
                print(f"{path.relative_to(ROOT).as_posix()}: {lines} lines (limit {limit})")
    print(f"{found} files over the size limit", file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
