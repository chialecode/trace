"""Generated TypeScript types and UI command names stay in step with the Rust contracts.

Two checks:
1. `apps/desktop/src/lib/generated/*.ts` is exactly what `cargo test -p trace-contracts`
   (ts-rs export) produces from the Rust DTOs. The directory is emptied, regenerated and compared
   with the committed files; any difference (changed, missing or stale file) fails and leaves the
   fresh output in place for review.
2. Every command name the desktop API wrapper sends (`"namespace.verb"` literals in
   `apps/desktop/src/lib/api.ts`) is in `UI_COMMANDS`, the whitelist the desktop process forwards.

Usage: python scripts/check_generated_types.py [--skip-regenerate]
"""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GENERATED = ROOT / "apps" / "desktop" / "src" / "lib" / "generated"
API = ROOT / "apps" / "desktop" / "src" / "lib" / "api.ts"
CONTROL = ROOT / "crates" / "contracts" / "src" / "control" / "commands.rs"
COMMAND_LITERAL = re.compile(r'"([a-z][a-z_]*\.[a-z][a-z_.]*)"')


def snapshot() -> dict[str, bytes]:
    return {p.name: p.read_bytes() for p in sorted(GENERATED.glob("*.ts"))}


def regenerate() -> list[str]:
    before = snapshot()
    for name in before:
        (GENERATED / name).unlink()
    proc = subprocess.run(
        ["cargo", "test", "-p", "trace-contracts", "--locked", "--quiet"],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        for name, data in before.items():
            (GENERATED / name).write_bytes(data)
        return ["cargo test -p trace-contracts failed:\n" + (proc.stdout + proc.stderr)[-2000:]]
    after = snapshot()
    problems = []
    for name in sorted(before.keys() - after.keys()):
        problems.append(f"stale generated file (no Rust DTO exports it): {name}")
    for name in sorted(after.keys() - before.keys()):
        problems.append(f"generated file not committed: {name}")
    for name in sorted(before.keys() & after.keys()):
        if before[name] != after[name]:
            problems.append(f"generated file out of date: {name}")
    return problems


def ui_commands() -> set[str]:
    text = CONTROL.read_text(encoding="utf-8")
    names = dict(re.findall(r'pub const ([A-Z0-9_]+): &str = "([^"]+)";', text))
    block = re.search(r"pub const UI_COMMANDS: &\[&str\] = &\[(.*?)\];", text, re.S)
    if not block:
        raise SystemExit("UI_COMMANDS not found in crates/contracts/src/control/commands.rs")
    return {names[c] for c in re.findall(r"\b([A-Z][A-Z0-9_]+),", block.group(1))}


def unknown_commands() -> list[str]:
    allowed = ui_commands()
    sent = set(COMMAND_LITERAL.findall(API.read_text(encoding="utf-8")))
    return [f"api.ts sends a command outside UI_COMMANDS: {c}" for c in sorted(sent - allowed)]


def main() -> int:
    problems = unknown_commands()
    if "--skip-regenerate" not in sys.argv:
        problems += regenerate()
    if problems:
        print("\n".join(problems))
        print("fix: cargo test -p trace-contracts, review and commit apps/desktop/src/lib/generated")
        return 1
    print("generated types and UI commands match the Rust contracts")
    return 0


if __name__ == "__main__":
    sys.exit(main())
