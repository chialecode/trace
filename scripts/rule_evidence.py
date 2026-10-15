#!/usr/bin/env python3
"""The public record of what was checked on real sites (docs/evidence/rule-evidence.json).

A site rule is `implemented` only when the checks listed here passed. The adapters declare the
same status and date in code; `rule_status_matches_recorded_real_evidence` (tests/e2e) compares the
two, and `stage.py report` refuses a stage whose record is missing, stale or over its budget.
Raw receipts stay in the ignored `.local/site-check/` folder; only counts and outcomes are kept here.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
FILE = "docs/evidence/rule-evidence.json"
STATUSES = {"implemented", "partial", "unimplemented"}
OUTCOMES = {"passed", "failed", "not-run", "stopped"}
ENTRIES = {"subscription", "preview", "link"}
# What a rule must have shown on the real site before it may say `implemented`.
REQUIRED = {
    # A subscription is its updates: both incremental checks (automatic download off and on).
    "subscription": {"account-recognition", "list-two-pages", "incremental-auto-off", "incremental-auto-on"},
    "preview": {"list-two-pages", "preview-select-download"},
    "link": {"single-link-download"},
}
SITE_NAMES = {
    "微博": "weibo", "小红书": "xiaohongshu", "Instagram": "instagram", "X": "x",
    "TikTok": "tiktok", "YouTube": "youtube", "哔哩哔哩": "bilibili", "抖音": "douyin",
}
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")


def authorized_budgets(root: Path = ROOT) -> dict[str, dict]:
    """The user-authorized ceilings, read from USER-ACTIONS.md, never from a receipt's own claim."""
    text = (root / "USER-ACTIONS.md").read_text(encoding="utf8")
    markers = re.findall(r"<!-- ACT-007-AUTHORIZED-BUDGETS (\{[^\r\n]+\}) -->", text)
    if len(markers) != 1:
        raise ValueError("missing or ambiguous ACT-007 budget")
    values = json.loads(markers[0])
    for budget in values.values():
        if not isinstance(budget, dict) or set(budget) != {"metadata_requests", "media_bytes"} or any(type(n) is not int or n <= 0 for n in budget.values()):
            raise ValueError("invalid ACT-007 budget")
    return values


def prior_requests(root: Path = ROOT) -> dict[str, int]:
    """Requests already spent before the implementation run (A's one-time probe table)."""
    text = (root / "USER-ACTIONS.md").read_text(encoding="utf8")
    section = text[text.index("A 的一次登录与目标探测"):]
    section = section[: section.index("格式检查")]
    found = {SITE_NAMES[name]: int(count) for name, count in re.findall(r"^\| (\S+) \| (\d+) \|", section, re.M) if name in SITE_NAMES}
    if set(found) != set(SITE_NAMES.values()):
        raise ValueError("ACT-007 prior usage table is incomplete")
    return found


def check_data(data: dict, root: Path = ROOT, sites: list[str] | None = None, plan: str | None = None) -> list[str]:
    import evidence

    errors: list[str] = []
    sites = evidence.adapter_sites(root) if sites is None else sites
    plan = evidence.current_plan(root) if plan is None else plan
    try:
        budgets = authorized_budgets(root)
        prior = prior_requests(root)
    except (ValueError, OSError, KeyError) as error:
        return [f"authorization record unreadable: {error}"]
    if data.get("schemaVersion") != 1:
        errors.append("rule evidence: unknown schema version")
    if data.get("plan") != plan:
        errors.append("rule evidence: wrong plan")
    recorded = data.get("sites")
    if not isinstance(recorded, dict) or sorted(recorded) != sorted(sites):
        return errors + ["rule evidence: sites differ from the adapters in the workspace"]
    for site, entry in recorded.items():
        where = f"rule evidence {site}"
        run = entry.get("run") if isinstance(entry, dict) else None
        if not isinstance(run, dict):
            errors.append(f"{where}: missing run summary")
            continue
        if not DATE.match(str(run.get("date", ""))):
            errors.append(f"{where}: run date")
        used, media = run.get("metadataRequests"), run.get("mediaBytes")
        if type(used) is not int or type(media) is not int or used < 0 or media < 0:
            errors.append(f"{where}: counters must be non-negative integers")
        elif site not in budgets:
            errors.append(f"{where}: no authorized budget")
        else:
            if prior.get(site, 0) + used > budgets[site]["metadata_requests"]:
                errors.append(f"{where}: metadata requests exceed the authorized budget")
            if media > budgets[site]["media_bytes"]:
                errors.append(f"{where}: media bytes exceed the authorized budget")
        if run.get("stoppedBy") not in (None, "challenge", "rate_limit", "login", "ban", "unclear_permission", "budget", "refused", "error"):
            errors.append(f"{where}: unknown stop reason")
        rules = entry.get("rules")
        if not isinstance(rules, dict) or not rules:
            errors.append(f"{where}: no rules")
            continue
        for rule_id, rule in rules.items():
            errors.extend(check_rule(site, rule_id, rule))
    return errors


def check_rule(site: str, rule_id: str, rule: dict) -> list[str]:
    where = f"rule evidence {rule_id}"
    errors: list[str] = []
    if not rule_id.startswith(site + ".") or not isinstance(rule, dict):
        return [f"{where}: id does not belong to the site"]
    status, entry = rule.get("status"), rule.get("entry")
    if status not in STATUSES:
        errors.append(f"{where}: status")
    if entry not in ENTRIES:
        errors.append(f"{where}: entry kind")
    checks = rule.get("checks")
    if not isinstance(checks, list):
        return errors + [f"{where}: checks"]
    names = {}
    for check in checks:
        if not isinstance(check, dict) or not check.get("name") or check.get("outcome") not in OUTCOMES:
            errors.append(f"{where}: malformed check")
        else:
            names[check["name"]] = check["outcome"]
    note = rule.get("note")
    if status == "implemented":
        if not DATE.match(str(rule.get("verifiedAt", ""))) or not str(rule.get("scope", "")).strip():
            errors.append(f"{where}: implemented needs the verification date and scope")
        missing = REQUIRED.get(entry, set()) - {n for n, o in names.items() if o == "passed"}
        if missing:
            errors.append(f"{where}: implemented without passed checks {sorted(missing)}")
        if "failed" in names.values() or "stopped" in names.values():
            errors.append(f"{where}: implemented with a failed or stopped check")
    elif status in ("partial", "unimplemented"):
        if not isinstance(note, str) or not note.strip():
            errors.append(f"{where}: {status} must say what is missing")
        if rule.get("verifiedAt") and not DATE.match(str(rule["verifiedAt"])):
            errors.append(f"{where}: verification date")
    return errors


def check_file(path: Path, root: Path = ROOT) -> list[str]:
    try:
        data = json.loads(path.read_text(encoding="utf8"))
    except (OSError, ValueError) as error:
        return [f"rule evidence unreadable: {error}"]
    try:
        return check_data(data, root)
    except (AttributeError, KeyError, TypeError, ValueError) as error:
        return [f"rule evidence malformed: {error}"]


def declared_rules(adapter_dir: Path, sites: list[str]) -> dict[str, list[dict]]:
    """Ask each built adapter what it declares at `hello` (the same answer Core catalogs)."""
    import core_client

    result = {}
    for site in sites:
        hello, proc, _ = core_client.adapter_hello(adapter_dir / f"trace-adapter-{site}.exe")
        try:
            result[site] = hello["result"]["rules"]
        finally:
            proc.kill()
            proc.wait(timeout=10)
            proc.stdin.close()
            proc.stdout.close()
    return result


def sync(adapter_dir: Path, root: Path = ROOT, today: str | None = None) -> dict:
    """Bring the record in line with what the adapters declare, keeping the checks already recorded.

    Status, date and scope always come from the declaration; the checks and notes are only ever
    written by the real-site driver or by hand with the evidence in hand.
    """
    import evidence
    import time

    path = root / FILE
    old = json.loads(path.read_text(encoding="utf8")) if path.is_file() else {}
    sites = evidence.adapter_sites(root)
    declared = declared_rules(adapter_dir, sites)
    today = today or time.strftime("%Y-%m-%d")
    out = {"schemaVersion": 1, "plan": evidence.current_plan(root), "sites": {}}
    for site in sites:
        before = old.get("sites", {}).get(site, {})
        entry = {"run": before.get("run") or {"date": today, "metadataRequests": 0, "mediaBytes": 0, "stoppedBy": None}, "rules": {}}
        for rule in declared[site]:
            kept = before.get("rules", {}).get(rule["id"], {})
            item = {"entry": rule["entry"], "status": rule["status"]}
            if rule.get("verified_at"):
                item["verifiedAt"] = rule["verified_at"]
            if rule.get("verified_scope"):
                item["scope"] = rule["verified_scope"]
            item["checks"] = kept.get("checks", [])
            note = kept.get("note") or ("；".join(rule.get("limits", [])) if rule["status"] != "implemented" else "")
            if note:
                item["note"] = note
            entry["rules"][rule["id"]] = item
        out["sites"][site] = entry
    return out


def main() -> int:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    sub.add_parser("check")
    sync_parser = sub.add_parser("sync")
    sync_parser.add_argument("--adapters", type=Path, default=ROOT / "target" / "debug")
    args = parser.parse_args()
    if args.action == "sync":
        data = sync(args.adapters)
        # Bytes, not text: the record stays LF on Windows, where text mode would write CRLF.
        (ROOT / FILE).write_bytes((json.dumps(data, ensure_ascii=False, indent=2) + chr(10)).encode("utf8"))
        print(f"wrote {FILE}")
    errors = check_file(ROOT / FILE)
    for error in errors:
        print("[FAIL]", error)
    if not errors:
        print("rule evidence consistent")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
