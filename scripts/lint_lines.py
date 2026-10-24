"""Report Rust source lines that are too long to read.

The refactor acceptance (docs/design/code-structure-refactor.md §8) allows lines over 150
characters only when they are an unbreakable literal. A line counts as breakable when it is
still longer than the limit after every string literal (also one spanning several lines) is
shortened to a placeholder.

Usage: python scripts/lint_lines.py [--limit 150] [paths...]
"""

import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_ROOTS = ["crates", "adapters", "tests", "apps"]
SKIP_PARTS = {"target", "node_modules", "dist", "gen"}


def collapsed_lines(text: str) -> list[str]:
    """Each source line with string literals replaced by a short placeholder."""
    out: list[str] = []
    line: list[str] = []
    i, n = 0, len(text)

    def newline() -> None:
        out.append("".join(line))
        line.clear()

    while i < n:
        c = text[i]
        if c == "\n":
            newline()
            i += 1
        elif text.startswith("//", i):
            j = text.find("\n", i)
            j = n if j < 0 else j
            line.append(text[i:j])
            i = j
        elif text.startswith("/*", i):
            depth, j = 1, i + 2
            while j < n and depth:
                if text.startswith("/*", j):
                    depth, j = depth + 1, j + 2
                elif text.startswith("*/", j):
                    depth, j = depth - 1, j + 2
                else:
                    if text[j] == "\n":
                        line.append(text[i:j])
                        newline()
                        i = j + 1
                    j += 1
            line.append(text[i:j])
            i = j
        elif c == "r" and _raw_start(text, i):
            hashes = 0
            j = i + 1
            while text[j] == "#":
                hashes += 1
                j += 1
            closing = '"' + "#" * hashes
            end = text.find(closing, j + 1)
            end = n if end < 0 else end + len(closing)
            line.append('"S"')
            for ch in text[i:end]:
                if ch == "\n":
                    newline()
            i = end
        elif c == '"':
            line.append('"S"')
            j = i + 1
            while j < n and text[j] != '"':
                if text[j] == "\\":
                    j += 1
                if j < n and text[j] == "\n":
                    newline()
                j += 1
            i = j + 1
        elif c == "'":
            end = _char_literal_end(text, i)
            line.append(text[i:end])
            i = end
        else:
            line.append(c)
            i += 1
    newline()
    return out


def _raw_start(text: str, i: int) -> bool:
    if i > 0 and (text[i - 1].isalnum() or text[i - 1] == "_"):
        return False
    j = i + 1
    while j < len(text) and text[j] == "#":
        j += 1
    return j < len(text) and text[j] == '"'


def _char_literal_end(text: str, i: int) -> int:
    """End of a char literal at i, or i + 1 for a lifetime."""
    if text.startswith("'\\", i):
        j = text.find("'", i + 3)
        return j + 1 if 0 < j - i <= 12 else i + 1
    if i + 2 < len(text) and text[i + 2] == "'":
        return i + 3
    return i + 1


def rust_files(roots):
    for root in roots:
        base = ROOT / root
        if base.is_file():
            yield base
            continue
        if not base.exists():
            continue
        for path in sorted(base.rglob("*.rs")):
            if SKIP_PARTS.isdisjoint(path.parts):
                yield path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--limit", type=int, default=150)
    parser.add_argument("paths", nargs="*", default=DEFAULT_ROOTS)
    args = parser.parse_args()
    found = 0
    for path in rust_files(args.paths):
        text = path.read_text(encoding="utf-8")
        lines = text.split("\n")
        flat = collapsed_lines(text)
        for number, line in enumerate(lines, 1):
            if len(line) > args.limit and len(flat[number - 1]) > args.limit:
                found += 1
                print(f"{path.relative_to(ROOT).as_posix()}:{number}: {len(line)} chars")
    print(f"{found} breakable long lines", file=sys.stderr)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
