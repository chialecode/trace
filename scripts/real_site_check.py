#!/usr/bin/env python3
"""Bounded, read-only real-site checks through the product's own control interface.

The driver starts the packaged daemon on an isolated Profile, imports the site's Cookie into that
Profile's DPAPI vault (adapters never see it), and exercises account recognition, two list pages,
a preview-and-select download, single-link downloads and a subscription with automatic download
off and on. Targets, Cookie files and raw receipts live only under the ignored `.local/` folder;
what leaves it is counts and outcomes (`record` -> docs/evidence/rule-evidence.json).

  python scripts/real_site_check.py prepare [--site S ...]      offline; exit 2 when an input is missing
  python scripts/real_site_check.py status                      budget ledger per site; offline
  python scripts/real_site_check.py run --site S --package DIR [--steps a,b,...]   real requests
  python scripts/real_site_check.py record [--site S ...]       merge receipts into the public record

Every request is counted by Core against a per-run ceiling (`acquisition_limits`) that is the
user-authorized budget (USER-ACTIONS ACT-007) minus what was already spent. A challenge, rate limit,
login failure, ban or unclear permission stops the site at once; nothing retries or bypasses it.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import time
from collections import Counter
from datetime import datetime, timedelta, timezone
from pathlib import Path

import rule_evidence

ROOT = Path(__file__).resolve().parents[1]
LOCAL = Path(".local") / "site-check"
TARGETS = LOCAL / "m2-targets.json"
LEDGER = LOCAL / "ledger.json"
OVERSEAS = {"instagram", "x", "youtube", "tiktok"}
NO_LOGIN = {"youtube"}
# The rules each kind of check speaks for. Only what the run actually exercised is claimed; the
# other lanes (shorts, posts, dynamics, ...) stay as they are until they get their own checks.
RULES = {
    "weibo": {"sub": ["weibo.user.posts"], "preview": ["weibo.user.preview"], "video": ["weibo.post.link"], "post": ["weibo.post.link"]},
    "xiaohongshu": {"sub": ["xiaohongshu.user.notes"], "preview": ["xiaohongshu.user.preview"], "video": ["xiaohongshu.note.link"], "post": ["xiaohongshu.note.link"]},
    "instagram": {"sub": ["instagram.user.posts"], "preview": ["instagram.user.preview"], "video": ["instagram.post.link"], "post": ["instagram.post.link"]},
    "x": {"sub": ["x.user.posts"], "preview": ["x.user.preview"], "video": ["x.post.link"], "post": ["x.post.link"]},
    "tiktok": {"sub": ["tiktok.user.posts"], "preview": ["tiktok.user.preview"], "video": ["tiktok.post.link"], "post": ["tiktok.post.link"]},
    "youtube": {"sub": ["youtube.channel.videos"], "preview": ["youtube.channel.preview_videos"], "video": ["youtube.video.link"], "post": ["youtube.post.link"]},
    "bilibili": {"sub": ["bilibili.user.videos"], "preview": ["bilibili.user.preview_videos"], "video": ["bilibili.video.link"], "post": []},
    "douyin": {"sub": ["douyin.user.videos"], "preview": ["douyin.user.preview_videos"], "video": ["douyin.video.link"], "post": []},
}
KEY_FIELD = {"weibo": "uid", "xiaohongshu": "user_id", "instagram": "username", "x": "screen_name",
             "tiktok": "unique_id", "youtube": "handle", "douyin": "sec_uid", "bilibili": "mid"}
STOP_CLASSES = {"rate_limited", "auth_required", "permission_denied", "capability_lost"}
STOP_WORDS = {
    "rate_limited": "rate_limit", "auth_required": "login", "permission_denied": "unclear_permission",
    "capability_lost": "login", "challenge": "challenge",
}
MAX_COOKIE = 16384
INTERVAL_MS = 2500


class PreparationError(ValueError):
    """An input the check needs is missing or malformed; no request was made."""


def package_problems(package: Path) -> list[str]:
    """Validate the package's own manifest, checksum list and every shipped member."""
    package = Path(package)
    if not package.is_dir():
        return ["package directory is missing"]
    manifest_path = package / "manifest.json"
    sums_path = package / "SHA256SUMS.txt"
    if not manifest_path.is_file():
        return ["package manifest is missing"]
    if not sums_path.is_file():
        return ["package SHA256SUMS.txt is missing"]
    errors: list[str] = []
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf8"))
    except (OSError, ValueError):
        return ["package manifest is not readable JSON"]
    if not isinstance(manifest, dict):
        return ["package manifest is not an object"]
    sources = manifest.get("source_files_sha256")
    if not isinstance(sources, dict) or manifest.get("sourceInputs") != sources:
        errors.append("package manifest source inventories differ")
    else:
        expected_tree = hashlib.sha256(
            json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()
        if manifest.get("source_tree_sha256") != expected_tree:
            errors.append("package source tree digest does not match its source inventory")
    commands = manifest.get("build_commands")
    if not isinstance(commands, list) or not commands:
        errors.append("package build commands are missing")
    elif any(
        not isinstance(command, dict)
        or command.get("exitCode") != 0
        or command.get("sourceInputs") != sources
        or command.get("sourceUnchanged") is not True
        for command in commands
    ):
        errors.append("package has a failed or stale build command")

    listed: dict[str, str] = {}
    for line in sums_path.read_text(encoding="utf8").splitlines():
        if not line:
            continue
        digest, separator, name = line.partition("  ")
        if not separator or not name or not re.fullmatch(r"[0-9a-fA-F]{64}", digest):
            errors.append("package checksum line is malformed")
            continue
        try:
            member = _package_member(package, name)
        except ValueError:
            errors.append("package checksum names an unsafe member")
            continue
        if name in listed:
            errors.append(f"package checksum is duplicated: {name}")
            continue
        listed[name] = digest.lower()
        if not member.is_file():
            errors.append(f"package member is missing: {name}")

    actual: dict[str, str] = {}
    try:
        for path in sorted(package.rglob("*")):
            rel = path.relative_to(package).as_posix()
            if path.is_symlink() or (hasattr(path, "is_junction") and path.is_junction()):
                errors.append(f"package contains a link: {rel}")
                continue
            if path.is_file() and rel != "SHA256SUMS.txt":
                actual[rel] = _sha256(path)
    except OSError:
        errors.append("package inventory cannot be read")
        return errors
    missing = sorted(set(listed) - set(actual))
    extra = sorted(set(actual) - set(listed))
    errors.extend(f"package member is missing: {name}" for name in missing)
    errors.extend(f"package has an unlisted member: {name}" for name in extra)
    errors.extend(
        f"package member digest differs: {name}"
        for name in sorted(set(listed) & set(actual))
        if listed[name] != actual[name]
    )
    return errors


def _package_member(package: Path, name: str) -> Path:
    if not isinstance(name, str) or not name or "\\" in name or ":" in name:
        raise ValueError("package member")
    member = Path(name)
    if (
        member.is_absolute()
        or any(part in ("", ".", "..") for part in name.split("/"))
        or not member.parts
    ):
        raise ValueError("package member")
    current = package.resolve()
    for part in member.parts:
        current = current / part
        if current.is_symlink() or (hasattr(current, "is_junction") and current.is_junction()):
            raise ValueError("linked package member")
    if not current.resolve().is_relative_to(package.resolve()):
        raise ValueError("package member escapes package")
    return current


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def package_identity(package: Path) -> dict:
    """What a receipt must name so it can never be rebound to another package (F-084)."""
    package = Path(package)
    errors = package_problems(package)
    if errors:
        raise PreparationError("package is not verifiable: " + "; ".join(errors[:6]))
    manifest = json.loads((package / "manifest.json").read_text(encoding="utf8"))
    sums = package / "SHA256SUMS.txt"
    digest = _sha256(sums)
    return {
        "directory": package.name,
        "sha256sums_sha256": digest,
        "git_commit": manifest.get("git_commit"),
        "source_tree_sha256": manifest.get("source_tree_sha256"),
        "schema_version": manifest.get("schema_version"),
        "control_protocol": manifest.get("control_protocol"),
    }


def verify_package_identity(root: Path, receipt: dict) -> list[str]:
    """A new receipt names a package digest and source tree; old receipts keep their attribution."""
    identity = receipt.get("package")
    if identity is None:
        return []
    if not isinstance(identity, dict):
        return ["receipt package identity is not an object"]
    required = {"directory", "sha256sums_sha256", "git_commit", "source_tree_sha256"}
    missing = sorted(required - set(identity))
    if missing:
        return [f"receipt package identity misses {', '.join(missing)}"]
    directory = str(identity["directory"])
    if not directory or directory != Path(directory).name or "\\" in directory:
        return ["receipt package directory is unsafe"]
    package = root / "dist" / directory
    if not package.is_dir():
        return ["receipt package directory is missing"]
    errors = package_problems(package)
    if errors:
        return errors
    try:
        actual = package_identity(package)
    except PreparationError as error:
        return [str(error)]
    for key in required | {"schema_version", "control_protocol"}:
        if actual.get(key) != identity.get(key):
            errors.append(f"receipt package identity differs at {key}")
    return errors





class Stop(Exception):
    """The site pushed back; the run for this site ends here."""

    def __init__(self, reason: str, detail: str):
        super().__init__(f"{reason}: {detail}")
        self.reason = reason
        self.detail = detail


def unlinked(path: Path) -> None:
    for part in (path, *path.parents):
        if part.is_symlink() or (hasattr(part, "is_junction") and part.is_junction()):
            raise PreparationError("linked paths are not accepted")


def read_env(root: Path) -> dict[str, str]:
    config = root / ".env.local"
    unlinked(config)
    if not config.is_file():
        raise PreparationError("missing .env.local; follow USER-ACTIONS ACT-007")
    if config.stat().st_size > 65536:
        raise PreparationError("local configuration exceeds budget")
    values: dict[str, list[str]] = {}
    for line in config.read_text(encoding="utf-8-sig").splitlines():
        name, sep, value = line.strip().partition("=")
        if sep and name.strip().startswith("TRACE_TEST_"):
            value = value.strip()
            if len(value) >= 2 and value[0] in ('"', "'") and value[-1] == value[0]:
                value = value[1:-1]
            values.setdefault(name.strip(), []).append(value)
    duplicated = [name for name, found in values.items() if len(found) != 1]
    if duplicated:
        raise PreparationError(f"duplicate {duplicated[0]} in .env.local")
    return {name: found[0] for name, found in values.items()}


def read_cookie(root: Path, env: dict[str, str], site: str) -> str | None:
    """The site's Cookie header value, or None for a site that needs no login. Never echoed."""
    if site in NO_LOGIN:
        return None
    key = f"TRACE_TEST_{site.upper()}_COOKIE_FILE"
    if not env.get(key):
        raise PreparationError(f"missing {key}; follow USER-ACTIONS ACT-007")
    path = root / env[key]
    unlinked(path)
    if not path.is_file():
        raise PreparationError(f"missing Cookie file for {site}; follow USER-ACTIONS ACT-007")
    if not 0 < path.stat().st_size <= MAX_COOKIE:
        raise PreparationError(f"empty or oversized Cookie file for {site}")
    secret = path.read_text(encoding="utf-8-sig").strip()
    if not secret or "\r" in secret or "\n" in secret or secret.lower().startswith(("cookie:", "curl ")) or "=" not in secret:
        raise PreparationError(f"{site}: expected one Cookie header value, without the Cookie: prefix")
    return secret


def read_targets(root: Path, site: str) -> dict:
    path = root / TARGETS
    unlinked(path)
    if not path.is_file():
        raise PreparationError("missing .local/site-check/m2-targets.json; follow USER-ACTIONS ACT-007")
    data = json.loads(path.read_text(encoding="utf-8"))
    target = (data.get("targets") or {}).get(site)
    if not isinstance(target, dict) or not str(target.get("profile", "")).startswith("https://"):
        raise PreparationError(f"{site}: no https profile target in m2-targets.json")
    return target


def proxy_from_env(env: dict[str, str]) -> str | None:
    value = env.get("TRACE_TEST_PROXY") or os.environ.get("TRACE_TEST_PROXY")
    if value and not re.fullmatch(r"https?://[^/@\s]+:\d+/?", value):
        raise PreparationError("TRACE_TEST_PROXY must be http(s)://host:port without credentials")
    return value


class Ledger:
    """What each site has already spent in this stage, kept next to the targets (ignored by Git)."""

    def __init__(self, root: Path):
        self.path = root / LEDGER
        self.data = json.loads(self.path.read_text(encoding="utf-8")) if self.path.is_file() else {}

    def used(self, site: str) -> dict:
        return self.data.get(site, {"metadata": 0, "mediaBytes": 0})

    def add(self, site: str, metadata: int, media: int) -> None:
        used = self.used(site)
        self.data[site] = {"metadata": used["metadata"] + metadata, "mediaBytes": used["mediaBytes"] + media}
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.path.write_text(json.dumps(self.data, indent=2), encoding="utf-8")

    def remaining(self, root: Path, site: str) -> dict:
        budget = rule_evidence.authorized_budgets(root)[site]
        prior = rule_evidence.prior_requests(root).get(site, 0)
        used = self.used(site)
        return {"metadata_requests": max(0, budget["metadata_requests"] - prior - used["metadata"]),
                "media_bytes": max(0, budget["media_bytes"] - used["mediaBytes"])}


def prepare(root: Path, sites: list[str]) -> dict:
    """Zero-network readiness: targets, Cookie format, proxy and remaining budgets."""
    env = read_env(root)
    ledger = Ledger(root)
    report = {"networkRequests": 0, "sites": {}}
    for site in sites:
        target = read_targets(root, site)
        read_cookie(root, env, site)
        report["sites"][site] = {"profileTarget": bool(target), "cookie": "ok" if site not in NO_LOGIN else "not needed",
                                 "remaining": ledger.remaining(root, site), "viaProxy": bool(proxy_from_env(env))}
    return report


def classify(error: Exception) -> Stop | None:
    """Map a Core error to a stop reason; anything unknown is reported, never retried."""
    text = str(error).lower()
    for needle, reason in STOP_WORDS.items():
        if needle in text:
            return Stop(reason, needle)
    if any(word in text for word in ("captcha", "verify", "verification", "challenge", "banned", "suspended")):
        return Stop("challenge", "verification or ban wording in the answer")
    if "budget" in text:
        return Stop("budget", "request or media budget reached")
    return None


class SiteRun:
    """One site, one isolated Profile, one ordered list of checks with their counters."""

    def __init__(self, root: Path, site: str, package: Path, run_dir: Path):
        import core_client

        self.root, self.site, self.package, self.dir = root, site, Path(package), run_dir
        errors = package_problems(self.package)
        if errors:
            raise PreparationError("package is not verifiable: " + "; ".join(errors[:6]))
        self.env = read_env(root)
        self.secret = read_cookie(root, self.env, site)
        self.target = read_targets(root, site)
        self.ledger = Ledger(root)
        self.remaining = self.ledger.remaining(root, site)
        if self.remaining["metadata_requests"] <= 0:
            raise PreparationError(f"{site}: no metadata budget left")
        self.checks: list[dict] = []
        self.abandoned: str | None = None
        self.posts: list[dict] | None = None
        self.halted: tuple[bool, dict] = (False, {})
        self.stopped: Stop | None = None
        self.credential_id: str | None = None
        self.core = None
        self.core_client = core_client

    def profile(self) -> Path:
        base = self.dir
        base.mkdir(parents=True, exist_ok=True)
        example = json.loads((self.package / "profile.example.json").read_text(encoding="utf8"))
        example.update(
            archive_id=f"check-{self.site}-{int(time.time())}", data_root=str(base / "data"), archive_root=str(base / "archive"),
            vault_dir=str(base / "vault"), adapter_dir=str(self.package / "adapters"), media_dir=str(self.package / "media"),
            acquisition_limits={"metadata_requests": self.remaining["metadata_requests"], "media_bytes": self.remaining["media_bytes"], "interval_ms": INTERVAL_MS},
        )
        if example.get("test_allowed_hosts") or example.get("extra_media_hosts"):
            raise PreparationError("the package profile must not carry test endpoints")
        path = base / "profile.json"
        path.write_text(json.dumps(example), encoding="utf8")
        return path

    def __enter__(self) -> "SiteRun":
        self.fault_log = self.dir / "adapter-faults.jsonl"
        self.core = self.core_client.Core(self.package / "trace-daemon.exe", self.profile(), env={**os.environ, "TRACE_ADAPTER_FAULT_LOG": str(self.fault_log)}, client="site-check")
        self.core.__enter__()
        proxy = proxy_from_env(self.env)
        self.route = "proxy · 全局设置" if proxy else "direct"
        if proxy:
            # DEC-035/R3-2: the user's machine route is a global manual proxy, for every site.
            self.core.call("network.set", {"scope": "global", "mode": "manual", "proxy_url": proxy, "username": None, "password": None, "keep_login": False})
        if self.secret is not None:
            imported = self.core.call("credential.import", {"platform": self.site, "subject": None, "secret": self.secret})
            self.credential_id = imported["credential_id"]
            self.await_login_probe()
        return self

    def await_login_probe(self) -> None:
        """Core probes a new credential once; a site that does not see a login ends here, before any check."""
        verdict: dict = {}
        for _ in range(20):
            rows = [r for r in self.core.call("credential.list", {"platform": self.site})["items"] if r["credential_id"] == self.credential_id]
            verdict = rows[0] if rows else {}
            if verdict.get("capability"):
                break
            time.sleep(2)
        if verdict.get("status") == "valid":
            return
        capability = verdict.get("capability") or {}
        said = capability.get("detail") or capability.get("source") or capability.get("error") or "no answer in time"
        self.stopped = Stop("login", f"the login probe says {verdict.get('status', 'nothing')} ({said})"[:300])
        self.checks.append({"name": "login-probe", "rules": [], "outcome": "stopped", "note": self.stopped.detail, "metadataRequests": 0, "mediaBytes": 0})

    def __exit__(self, *exc) -> None:
        metadata = media = 0
        try:
            status = self.core.call("daemon.status", {}, timeout=20)["acquisition_budget"]
            metadata, media = int(status["metadata_request_count"]), int(status["media_received_bytes"])
        except Exception:
            pass
        finally:
            self.core.__exit__(*exc)
            self.ledger.add(self.site, metadata, media)
        self.metadata, self.media = metadata, media

    def counters(self) -> tuple[int, int]:
        status = self.core.call("daemon.status", {}, timeout=20)["acquisition_budget"]
        self.halted = (bool(status.get("stopped")), (status.get("requests") or [{}])[-1])
        return int(status["metadata_request_count"]), int(status["media_received_bytes"])

    def stopped_by_core(self) -> Stop | None:
        """Core ends a site's requests itself after a refusal (HTTP 4xx/5xx) or at the budget; that is a stop."""
        halted, last = self.halted
        if not halted:
            return None
        status = last.get("status")
        if isinstance(status, int) and status >= 400:
            return Stop("refused", f"HTTP {status} from a {'metadata' if last.get('metadata') else 'media'} address; Core stopped the site")
        return Stop("budget", "Core stopped the site at its request or media budget")

    def step(self, name: str, rules: list[str], action) -> dict | None:
        """Run one check; a stop condition ends the whole site and is recorded, not retried."""
        if self.stopped:
            self.checks.append({"name": name, "rules": rules, "outcome": "not-run", "note": "site stopped earlier"})
            return None
        before = self.counters()
        fault_mark = self.fault_log.stat().st_size if self.fault_log.exists() else 0
        record = {"name": name, "rules": rules, "outcome": "failed", "note": ""}
        result = None
        try:
            result = action()
            record["outcome"] = result.pop("outcome", None) or ("passed" if result.pop("ok", True) else "failed")
            record["note"] = result.pop("note", "")
            record.update(result)
        except self.core_client.CoreError as error:
            stop = classify(error)
            record["note"] = str(error.error)[:300] if error.error else "failed"
            if stop:
                record["outcome"], self.stopped = "stopped", stop
        except Stop as stop:
            record["outcome"], record["note"], self.stopped = "stopped", stop.detail, stop
        except Exception as error:  # a driver or transport fault is a failure of the check, not of the site
            record["note"] = f"{type(error).__name__}: {error}"[:300]
        after = self.counters()
        record["metadataRequests"], record["mediaBytes"] = after[0] - before[0], after[1] - before[1]
        if self.stopped is None and (halt := self.stopped_by_core()):
            self.stopped = halt
            if record["outcome"] != "passed":
                record["outcome"], record["note"] = "stopped", f"{halt.detail}; {record['note']}"[:300]
        if record["outcome"] != "passed":
            record["faults"] = self.faults_since(fault_mark)
        self.checks.append(record)
        return result

    def faults_since(self, mark: int) -> list[str]:
        """What the adapters said about their failures during one check (local receipt only)."""
        if not self.fault_log.exists():
            return []
        with open(self.fault_log, "rb") as handle:
            handle.seek(mark)
            lines = handle.read().decode("utf8", "replace").splitlines()
        seen: list[str] = []
        for line in lines:
            try:
                fault = json.loads(line)
            except ValueError:
                continue
            text = f"{fault.get('method')}: {fault.get('class')}: {fault.get('message')}"
            if text not in seen:
                seen.append(text)
        return seen[:6]

    def receipt(self) -> dict:
        status = self.core.call("daemon.status", {}, timeout=20)["acquisition_budget"]
        return {
            "site": self.site, "date": datetime.now(timezone.utc).strftime("%Y-%m-%d"),
            "route": getattr(self, "route", "direct"),
            "package": package_identity(self.package),
            "remainingBefore": self.remaining, "stopped": bool(status.get("stopped")),
            "stopReason": self.stopped.reason if self.stopped else None,
            "stopDetail": self.stopped.detail if self.stopped else None,
            "metadataRequests": status["metadata_request_count"], "mediaBytes": status["media_received_bytes"],
            "endpoints": [{k: v for k, v in item.items() if k in ("method", "endpoint", "status", "metadata", "started_at")} for item in status.get("requests", [])],
            "checks": self.checks,
        }


# ---------------------------------------------------------------------------------------------
# Checks. Each takes a SiteRun and returns {"ok": bool, "note": str, ...}; they only read.
# ---------------------------------------------------------------------------------------------


def check_account(run: SiteRun) -> dict:
    resolved = run.core.call("account.resolve", {"namespace": run.site, "input": run.target["profile"], "credential_id": run.credential_id})
    key = resolved.get("native_account_key") or ""
    run.account_key, run.account_id = key, resolved.get("account_id")
    expected = str(run.target.get(KEY_FIELD[run.site], ""))
    return {"ok": bool(key and resolved.get("display_name")), "note": "stable key matches the target id" if key == expected else "stable key differs from the handle (sites keyed by a numeric id)"}


def check_list(run: SiteRun, rule_id: str | None = None, pages_wanted: int = 2) -> dict:
    first = run.core.call("source.profile.preview", {"url": run.target["profile"], "preview_id": None, "rule_id": rule_id, "credential_id": run.credential_id})
    run.preview = first
    pages, items = first["pages_loaded"], len(first["items"])
    if first["has_more"] and pages < pages_wanted:
        second = run.core.call("source.profile.preview", {"url": None, "preview_id": first["preview_id"], "rule_id": None, "credential_id": run.credential_id})
        run.preview, pages, items = second, second["pages_loaded"], len(second["items"])
    ok = items > 0 and (pages >= pages_wanted or not run.preview["has_more"])
    listed = run.preview["items"]
    kinds = {k: sum(k in (i.get("content_kinds") or []) for i in listed) for k in ("video", "image", "text", "article", "live")}
    shape = ", ".join(f"{n} {k}" for k, n in kinds.items() if n)
    dated = sum(bool(i.get("published_at")) for i in listed)
    return {"ok": ok, "note": f"{pages} page(s), {items} entries ({shape or 'no kinds given'}; {dated} dated), more={run.preview['has_more']}, limited={run.preview['limited']}"}


LONGEST_VIDEO_MS = 900_000
# An incremental check picks among the newest few dated videos: every lane of the subscription
# reads the list down to the chosen work, and each page is a request against the site's budget.
RECENT_FOR_INCREMENTAL = 4
ASSUMED_VIDEO_MS = 180_000


def pick(items: list[dict], want: str, limit_ms: int = LONGEST_VIDEO_MS, skip: frozenset = frozenset(), dated: bool = False, rank: int = 0, recent: int = 0) -> dict | None:
    """A small, ordinary candidate of one kind from a preview page (never a pinned or live one).

    A video is the shortest one that is not longer than `limit_ms` (`rank` 1 is the next shortest);
    pictures are the work with the fewest, since every picture is a request of its own against the
    site's budget. With `recent`, a video is chosen among that many of the newest dated ones: a
    check of the newest works reads one page of the list, not the pages down to an old work.
    """
    videos: list[dict] = []
    pictures: list[dict] = []
    for item in items:
        kinds = set(item.get("content_kinds") or [])
        if item.get("in_library") or item.get("pinned") or "live" in kinds or item["native_key"] in skip:
            continue
        if dated and not item.get("published_at"):
            continue
        # A count of 0 is a list that does not say (a single work is one video or a few pictures).
        if want == "video" and "video" in kinds and item["video_count"] <= 1 and (item.get("duration_ms") or 0) <= limit_ms:
            videos.append(item)
        if want == "image" and "image" in kinds and "video" not in kinds and item["image_count"] <= 4:
            pictures.append(item)
        if want == "text" and kinds <= {"text", "article"}:
            return item
    if want == "video":
        if recent:
            videos = sorted((v for v in videos if v.get("published_at")), key=lambda i: i["published_at"], reverse=True)[:recent]
        videos.sort(key=lambda i: i.get("duration_ms") or ASSUMED_VIDEO_MS)
        return videos[rank] if rank < len(videos) else None
    return min(pictures, key=lambda i: i["image_count"], default=None)


def check_preview_download(run: SiteRun) -> dict:
    """Select a video and a picture (or text) work on the listed pages and deliver them to a folder."""
    items = run.preview["items"]
    unused = frozenset(run.used_keys)
    video = pick(items, "video", skip=unused)
    other = pick(items, "image", skip=unused) or pick(items, "text", skip=unused) or pick(items, "video", skip=unused, rank=1)
    chosen = [i for i in (video, other) if i]
    if not chosen:
        return {"outcome": "not-run", "note": "no unused, ordinary work of a modest size among the listed ones"}
    run.used_keys.update(i["native_key"] for i in chosen)
    folder = run.dir / "downloads"
    folder.mkdir(parents=True, exist_ok=True)
    report = run.core.call("source.profile.download", {"preview_id": run.preview["preview_id"], "native_keys": [i["native_key"] for i in chosen], "target": {"kind": "folder", "dir": str(folder)}})
    delivery = wait_delivery(run, report["delivery_id"])
    files = [p for p in folder.rglob("*") if p.is_file() and p.suffix != ".json"]
    total = sum(p.stat().st_size for p in files)
    states = sorted({item["state"] for item in delivery.get("items", [])})
    ok = delivery["state"] == "done" and bool(files) and all(p.stat().st_size > 0 for p in files) and states == ["exported"]
    return {"ok": ok, "note": f"delivery {delivery['state']}, items {states}, {len(files)} file(s), {total} bytes, accepted {report['accepted']}, skipped {len(report['skipped'])}"}


def wait_delivery(run: SiteRun, delivery_id: str, timeout: float = 600) -> dict:
    deadline = time.monotonic() + timeout
    while True:
        views = run.core.call("source.deliveries", {"delivery_id": delivery_id, "limit": 1})["items"]
        view = views[0] if views else {"state": "missing"}
        if view["state"] not in ("running", "finishing"):
            return view
        if time.monotonic() > deadline:
            return {**view, "state": "timeout"}
        time.sleep(3)


def work_url(site: str, item: dict, target: dict) -> str:
    """The public link of a listed work, built from its native key (read-only; no extra request)."""
    key = item["native_key"]
    handle = str(target.get(KEY_FIELD[site], ""))
    patterns = {
        "weibo": f"https://weibo.com/detail/{key}",
        "xiaohongshu": f"https://www.xiaohongshu.com/explore/{key}",
        "instagram": f"https://www.instagram.com/p/{key}/",
        "x": f"https://x.com/{handle}/status/{key}",
        "youtube": f"https://www.youtube.com/post/{key}" if item.get("_post") else f"https://www.youtube.com/watch?v={key}",
        "tiktok": f"https://www.tiktok.com/@{handle}/video/{key}",
        "douyin": f"https://www.douyin.com/video/{key}",
        "bilibili": f"https://www.bilibili.com/video/{key}",
    }
    return patterns[site]


def choose_offers(offers: list[dict], height: int = 720) -> list[str]:
    """One offer for every resource the download needs: a video (the tallest not above `height`), one
    audio track, and for anything else the first one listed (the best)."""
    by_resource: dict[str, list[dict]] = {}
    for offer in offers:
        if offer.get("required") or offer.get("default"):
            by_resource.setdefault(offer.get("attachment_id") or f"role:{offer.get('role') or ''}", []).append(offer)
    tall = lambda o: (o.get("spec") or {}).get("height") or 0
    chosen: list[str] = []
    for group in by_resource.values():
        role = group[0].get("role") or ""
        if len(group) > 1 and role == "video":
            fitting = [o for o in group if 0 < tall(o) <= height]
            group = [max(fitting, key=tall)] if fitting else [min(group, key=lambda o: tall(o) or 10 ** 9)]
        elif len(group) > 1:
            group = group[:1]
        chosen += [o["id"] for o in group]
    return chosen or [o["id"] for o in offers[:1]]


# Sites whose pictures and texts are a lane of their own, not part of the list the video checks use.
POST_LANE = {"youtube": "youtube.channel.preview_posts"}


def post_items(run: SiteRun) -> list[dict]:
    """The first page of the site's post lane, loaded once (one request-bearing call)."""
    if run.posts is None:
        first = run.core.call("source.profile.preview", {"url": run.target["profile"], "preview_id": None, "rule_id": POST_LANE[run.site], "credential_id": run.credential_id})
        run.posts = [dict(item, _post=True) for item in first["items"]]
    return run.posts


def check_single_link(run: SiteRun, want: str) -> dict:
    """Download one listed work through its own link; a work not used by an earlier check is preferred."""
    listed = run.preview["items"]
    item = pick(listed, want, skip=frozenset(run.used_keys)) or pick(listed, want)
    if not item and want != "video" and run.site in POST_LANE:
        posts = post_items(run)
        item = pick(posts, want, skip=frozenset(run.used_keys)) or pick(posts, want)
    if not item:
        return {"outcome": "not-run", "note": f"no {want} candidate among the listed works"}
    run.used_keys.add(item["native_key"])
    preview = run.core.call("source.preview", {"url": work_url(run.site, item, run.target), "credential_id": run.credential_id})
    offers = preview.get("offers") or []
    selected = choose_offers(offers)
    confirmed = run.core.call("source.confirm", {"preview_id": preview["preview_id"], "selected": selected, "destination": None})
    deadline = time.monotonic() + 600
    mine, state = None, "running"
    while time.monotonic() < deadline:
        found = [r for r in run.core.call("source.list", {})["items"] if r["request_id"] == confirmed["request_id"]]
        mine = found[0] if found else None
        state = mine["state"] if mine else state
        if state in ("succeeded", "needs_attention", "cancelled"):
            break
        time.sleep(3)
    for task in (mine or {}).get("tasks", []):
        stop = classify(RuntimeError(str(task.get("error") or "")))
        if stop and stop.reason != "budget":
            raise stop
    # A video or a picture that offered nothing downloaded nothing: that is not a pass.
    ok = state == "succeeded" and (want == "text" or bool(selected))
    return {"ok": ok, "note": f"request {state}, offers {len(offers)}, selected {len(selected)}"}


BUSY_JOBS = ("observe", "media", "delivery")


def stop_if_pushed_back(run: SiteRun) -> list[dict]:
    """The queued jobs; a job that ended on a limit, challenge, lost login or refusal stops the site."""
    jobs = run.core.call("job.list", {"limit": 200})["items"]
    for job in jobs:
        error = job.get("last_error") or {}
        if error.get("class") in STOP_CLASSES:
            raise Stop(STOP_WORDS[error["class"]], error["class"])
    return jobs


def wait_quiet(run: SiteRun, timeout: float = 420, kinds: tuple = BUSY_JOBS) -> bool:
    """Until the detail and download jobs have run to rest (recurring ones wait far ahead)."""
    deadline, calm = time.monotonic() + timeout, 0
    while time.monotonic() < deadline:
        now = datetime.now(timezone.utc)
        busy = False
        for job in stop_if_pushed_back(run):
            if job["job_type"] not in kinds:
                continue
            due = job.get("next_run_at")
            soon = due is None or datetime.fromisoformat(due.replace("Z", "+00:00")) <= now + timedelta(seconds=2)
            busy = busy or job["state"] in ("running", "claimed") or (job["state"] == "queued" and soon)
        calm = 0 if busy else calm + 1
        if calm >= 2:
            return True
        time.sleep(3)
    return False


def wait_still(run: SiteRun, quiet: float = 10, timeout: float = 90) -> bool:
    """Until the site has not been asked anything for `quiet` seconds (a check ran to its end)."""
    deadline = time.monotonic() + timeout
    last, since = run.counters()[0], time.monotonic()
    while time.monotonic() < deadline:
        time.sleep(2)
        now = run.counters()[0]
        if now != last:
            last, since = now, time.monotonic()
        elif time.monotonic() - since >= quiet:
            return True
        stop_if_pushed_back(run)
    return False


def subscription_lane(run: SiteRun) -> str:
    """The content lane the site's subscription rule declares; a subscription is made for one lane."""
    run.core.call("site.refresh", {})
    for site in run.core.call("site.catalog", {})["sites"]:
        for rule in site["rules"] if site["id"] == run.site else []:
            if rule["declaration"]["id"] == RULES[run.site]["sub"][0] and rule["declaration"].get("lane"):
                return rule["declaration"]["lane"]
    return "video"


def incremental_range(item: dict, now: datetime | None = None) -> dict:
    """The range whose only work is `item`: the instant it was published when the site says that
    instant exactly, else the last days up to it (a date the list only gives as "3 days ago" or as a
    day is placed by the work's own page, so an instant would not find it)."""
    stamp = item["published_at"]
    if item.get("published_precision") in (None, "", "source", "exact"):
        return {"kind": "date_range", "start": stamp, "end": stamp}
    now = now or datetime.now(timezone.utc)
    age = now - datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    return {"kind": "recent_days", "days": max(1, age.days + 2)}


def detail_date(run: SiteRun, item: dict) -> dict | None:
    """The date of one listed work, from its own page and without selecting or downloading media.

    A list that gives no date leaves the check with nothing to place the range by (contracts §5.3);
    one read-only link preview carries the detail's published time, so the first check can be aimed
    at exactly that work. The preview expires and is not confirmed, so nothing is archived here.
    """
    preview = run.core.call("source.preview", {"url": work_url(run.site, item, run.target), "credential_id": run.credential_id})
    stamp = preview.get("published_at")
    if not stamp:
        return None
    return {"published_at": stamp, "published_precision": preview.get("published_precision") or "source"}


def own_subscription(run: SiteRun, item: dict, auto: bool) -> str:
    """A subscription whose range is exactly the instant one listed work was published."""
    if getattr(run, "account_id", None) is None:
        if getattr(run, "account_key", None) is None:
            check_account(run)
        creator = run.core.call("creator.create", {"display_name": f"check-{run.site}"})
        bound = run.core.call("account.bind", {"creator_id": creator["creator_id"], "namespace": run.site,
                                               "native_account_key": run.account_key, "profile_url": run.target["profile"]})
        run.account_id = bound["account_id"]
    existing = getattr(run, "subscription_id", None)
    if existing:
        # An account has one subscription per lane: the second check moves the first one on.
        run.core.call("subscription.update", {"subscription_id": existing, "range": incremental_range(item), "auto_download": auto, "enabled": True})
        drop_deep_lane(run, existing)
        return existing
    created = run.core.call("subscription.create", {
        "account_id": run.account_id, "content_type": subscription_lane(run), "range": incremental_range(item),
        "credential_id": run.credential_id, "check_interval_seconds": 3600, "auto_download": auto})
    run.subscription_id = created["subscription_id"]
    drop_deep_lane(run, run.subscription_id)
    return run.subscription_id


def drop_deep_lane(run: SiteRun, subscription_id: str) -> None:
    """A check is about the recent and history lanes; the low-frequency full-verification lane would
    read the list a third time against the site's budget for nothing the check looks at."""
    key = f"scan:{subscription_id}:deep"
    for job in run.core.call("job.list", {"job_type": "scan", "limit": 200})["items"]:
        if job.get("job_key") == key:
            run.core.call("job.cancel", {"job_id": job["job_id"]})


def set_enabled(run: SiteRun, subscription_id: str, enabled: bool) -> None:
    """Pause a check subscription as soon as it has shown what it was made for: its history lane walks on."""
    run.core.call("subscription.update", {"subscription_id": subscription_id, "enabled": enabled})


def timeline(run: SiteRun) -> list[dict]:
    return run.core.call("archive.timeline", {"namespace": run.site, "limit": 50})["items"]


def request_snapshot(run: SiteRun) -> list[dict]:
    """All counted requests so far, with endpoint and start time (never a query or token)."""
    budget = run.core.call("daemon.status", {}, timeout=20).get("acquisition_budget") or {}
    return [dict(item) for item in budget.get("requests", [])]


def new_requests(before: list[dict], after: list[dict]) -> list[dict]:
    return after[len(before):] if after[: len(before)] == before else after


def subscription_evidence(run: SiteRun, subscription_id: str) -> dict:
    """Core's credential-free view of one subscription's discovery and execution facts."""
    return run.core.call(
        "archive.subscription.evidence",
        {"subscription_id": subscription_id, "limit": 500},
    )


def _non_negative_int(value: object) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def evidence_problem(evidence: dict, subscription_id: str) -> str | None:
    """Reject an incomplete snapshot instead of treating missing facts as zero activity."""
    if not isinstance(evidence, dict) or evidence.get("subscription_id") != subscription_id:
        return "the subscription evidence is missing or names another subscription"
    if evidence.get("truncated") is not False:
        return "the subscription evidence is truncated"
    if evidence.get("budget_available") is not True:
        return "the subscription evidence has no request budget inventory"
    if not isinstance(evidence.get("entries"), list) or not isinstance(evidence.get("requests"), list):
        return "the subscription evidence has no complete entry/request inventory"
    if not isinstance(evidence.get("evidence_media_bytes"), dict):
        return "the subscription evidence has no per-execution media bytes"
    seen_entries: set[str] = set()
    for entry in evidence["entries"]:
        if not isinstance(entry, dict) or not isinstance(entry.get("entry_id"), str):
            return "an evidence entry has no identity"
        entry_id = entry["entry_id"]
        if entry_id in seen_entries:
            return f"entry {entry_id} appears twice"
        seen_entries.add(entry_id)
        relation = entry.get("relation")
        if (
            not isinstance(relation, dict)
            or not isinstance(relation.get("first_discovered_at"), str)
            or not isinstance(relation.get("last_discovered_at"), str)
        ):
            return f"entry {entry['entry_id']} has no complete discovery relation"
        discoveries = entry.get("discovered_by_subscriptions")
        if not isinstance(discoveries, list):
            return f"entry {entry['entry_id']} has no discovery inventory"
        for discovery in discoveries:
            if (
                not isinstance(discovery, dict)
                or not isinstance(discovery.get("subscription_id"), str)
                or not isinstance(discovery.get("first_discovered_at"), str)
                or not isinstance(discovery.get("last_discovered_at"), str)
            ):
                return f"entry {entry['entry_id']} has an incomplete discovery record"
        jobs = entry.get("jobs")
        contexts = entry.get("contexts")
        observations = entry.get("observations")
        acquisitions = entry.get("acquisitions")
        for key, value in (
            ("jobs", jobs),
            ("contexts", contexts),
            ("observations", observations),
            ("acquisitions", acquisitions),
        ):
            if not isinstance(value, list):
                return f"entry {entry['entry_id']} has no {key} inventory"
        count, latest = entry.get("observation_count"), entry.get("latest_observation_seq")
        if not isinstance(count, int) or isinstance(count, bool) or count < 0:
            return f"entry {entry['entry_id']} has no observation count"
        if not isinstance(latest, int) or isinstance(latest, bool) or latest < 0:
            return f"entry {entry['entry_id']} has no latest observation sequence"
        if count == 0 and latest != 0 or count > 0 and latest == 0:
            return f"entry {entry['entry_id']} has inconsistent observation facts"
        if len(observations) != count:
            return f"entry {entry['entry_id']} has an incomplete observation inventory"
        sequences: set[int] = set()
        for observation in observations:
            if (
                not isinstance(observation, dict)
                or not _non_negative_int(observation.get("seq"))
                or observation["seq"] < 1
                or not isinstance(observation.get("context_id"), str)
                or not observation["context_id"]
                or not isinstance(observation.get("access_state"), str)
                or not isinstance(observation.get("observed_at"), str)
            ):
                return f"entry {entry['entry_id']} has an unattributed observation"
            if observation["seq"] in sequences:
                return f"entry {entry['entry_id']} has duplicate observation {observation['seq']}"
            sequences.add(observation["seq"])
        if latest != (max(sequences) if sequences else 0):
            return f"entry {entry['entry_id']} has an inconsistent latest observation sequence"
        context_ids: set[str] = set()
        for context in contexts:
            if (
                not isinstance(context, dict)
                or not isinstance(context.get("context_id"), str)
                or not context["context_id"]
                or not isinstance(context.get("started_at"), str)
                or not isinstance(context.get("auth_state"), str)
                or not isinstance(context.get("basis"), str)
            ):
                return f"entry {entry['entry_id']} has an incomplete acquisition context"
            if context["context_id"] in context_ids:
                return f"entry {entry['entry_id']} has duplicate acquisition context"
            context_ids.add(context["context_id"])
            job = context.get("job_id")
            generation = context.get("job_generation")
            if job is not None and not isinstance(job, str):
                return f"entry {entry['entry_id']} has an invalid context job"
            if generation is not None and (
                not isinstance(generation, int) or isinstance(generation, bool) or generation < 1
            ):
                return f"entry {entry['entry_id']} has an invalid context generation"
        for job in jobs:
            if (
                not isinstance(job, dict)
                or not isinstance(job.get("job_id"), str)
                or not isinstance(job.get("job_key"), str)
                or job.get("job_type") not in ("observe", "media")
                or not isinstance(job.get("state"), str)
                or not isinstance(job.get("consumer_state"), str)
                or not _non_negative_int(job.get("consumer_requested"))
                or not _non_negative_int(job.get("consumer_completed"))
                or not isinstance(job.get("job_generation"), int)
                or isinstance(job.get("job_generation"), bool)
                or job["job_generation"] < 1
            ):
                return f"entry {entry['entry_id']} has an incomplete consumer job"
            if job["consumer_requested"] <= 0:
                return f"entry {entry['entry_id']} has a consumer job with no request"
            if job["consumer_completed"] > job["consumer_requested"]:
                return f"entry {entry['entry_id']} has a consumer job beyond its request count"
        for acquisition in acquisitions:
            if (
                not isinstance(acquisition, dict)
                or not isinstance(acquisition.get("acquisition_id"), str)
                or not isinstance(acquisition.get("context_id"), str)
                or acquisition["context_id"] not in context_ids
                or not isinstance(acquisition.get("outcome"), str)
                or not isinstance(acquisition.get("finished_at"), str)
            ):
                return f"entry {entry['entry_id']} has an incomplete acquisition"
        for observation in observations:
            if observation["context_id"] not in context_ids:
                return f"entry {entry['entry_id']} has an observation outside its contexts"
    for request in evidence["requests"]:
        if (
            not isinstance(request, dict)
            or not isinstance(request.get("evidence_id"), str)
            or request["evidence_id"] not in {
                context["context_id"]
                for entry in evidence["entries"]
                for context in entry["contexts"]
            }
            or not isinstance(request.get("endpoint"), str)
            or not isinstance(request.get("method"), str)
            or not isinstance(request.get("metadata"), bool)
            or not isinstance(request.get("status"), int)
            or isinstance(request.get("status"), bool)
            or not 200 <= request["status"] < 400
            or not isinstance(request.get("started_at"), str)
        ):
            return "the subscription evidence has an incomplete or unattributed request"
    context_ids = {
        context["context_id"]
        for entry in evidence["entries"]
        for context in entry["contexts"]
    }
    for context_id, value in evidence["evidence_media_bytes"].items():
        if (
            not isinstance(context_id, str)
            or context_id not in context_ids
            or not _non_negative_int(value)
        ):
            return "the subscription evidence has media bytes outside its contexts"
    return None


def evidence_entries(evidence: dict) -> dict[str, dict]:
    return {str(entry["entry_id"]): entry for entry in evidence.get("entries", [])}


def relation_map(evidence: dict) -> dict[str, tuple[str, str]]:
    return {
        str(entry["entry_id"]): (
            str(entry["relation"]["first_discovered_at"]),
            str(entry["relation"]["last_discovered_at"]),
        )
        for entry in evidence.get("entries", [])
    }


def relation_state(evidence: dict) -> dict[str, tuple]:
    """The discovery relation and its timestamps, not just the set of entry ids."""
    return {
        str(entry["entry_id"]): (
            entry["relation"]["first_discovered_at"],
            entry["relation"]["last_discovered_at"],
            tuple(
                (
                    discovery.get("subscription_id"),
                    discovery.get("first_discovered_at"),
                    discovery.get("last_discovered_at"),
                )
                for discovery in entry.get("discovered_by_subscriptions", [])
            ),
        )
        for entry in evidence.get("entries", [])
    }


def context_requests(evidence: dict, context_id: str) -> list[dict]:
    return [
        request
        for request in evidence.get("requests", [])
        if request.get("evidence_id") == context_id
    ]


def request_identity(request: dict) -> tuple:
    return (
        request.get("evidence_id"),
        request.get("endpoint"),
        request.get("method"),
        request.get("metadata"),
        request.get("status"),
        request.get("started_at"),
    )


def request_delta(before: list[dict], after: list[dict]) -> list[dict]:
    """Requests added since the snapshot, preserving duplicate occurrences."""
    remaining = Counter(request_identity(request) for request in before)
    added = []
    for request in after:
        identity = request_identity(request)
        if remaining[identity]:
            remaining[identity] -= 1
        else:
            added.append(request)
    return added


def context_index(entry: dict) -> dict[str, dict]:
    return {
        str(context["context_id"]): context
        for context in entry.get("contexts", [])
        if isinstance(context, dict) and context.get("context_id")
    }


def new_contexts(entry: dict, baseline: dict | None) -> list[dict]:
    before = context_index(baseline or {"contexts": []})
    return [
        context
        for context in entry.get("contexts", [])
        if str(context.get("context_id")) not in before
    ]


def observation_state(entry: dict) -> tuple:
    return tuple(
        (
            observation.get("seq"),
            observation.get("access_state"),
            observation.get("observed_at"),
            observation.get("context_id"),
        )
        for observation in entry.get("observations", [])
    )


def job_state(entry: dict) -> tuple:
    return tuple(
        sorted(
            (
                job.get("job_id"),
                job.get("job_key"),
                job.get("job_type"),
                job.get("state"),
                job.get("consumer_requested"),
                job.get("consumer_completed"),
                job.get("consumer_state"),
                job.get("job_generation"),
                job.get("updated_at"),
            )
            for job in entry.get("jobs", [])
        )
    )


def job_signature(job: dict) -> tuple:
    return (
        job.get("job_key"),
        job.get("job_type"),
        job.get("state"),
        job.get("consumer_requested"),
        job.get("consumer_completed"),
        job.get("consumer_state"),
        job.get("job_generation"),
        job.get("updated_at"),
    )


def changed_jobs(entry: dict, baseline: dict | None) -> list[dict]:
    before = {
        job.get("job_id"): job_signature(job)
        for job in (baseline or {}).get("jobs", [])
        if isinstance(job, dict)
    }
    return [
        job
        for job in entry.get("jobs", [])
        if isinstance(job, dict)
        and (
            job.get("job_id") not in before
            or before.get(job.get("job_id")) != job_signature(job)
        )
    ]


def context_state(entry: dict) -> tuple:
    return tuple(
        sorted(
            (
                context.get("context_id"),
                context.get("job_id"),
                context.get("job_generation"),
                context.get("started_at"),
                context.get("auth_state"),
                context.get("basis"),
            )
            for context in entry.get("contexts", [])
        )
    )


def context_signature(context: dict) -> tuple:
    return (
        context.get("job_id"),
        context.get("job_generation"),
        context.get("started_at"),
        context.get("auth_state"),
        context.get("basis"),
    )


def changed_contexts(entry: dict, baseline: dict | None) -> list[dict]:
    before = {
        str(context.get("context_id")): context_signature(context)
        for context in (baseline or {}).get("contexts", [])
        if isinstance(context, dict) and context.get("context_id")
    }
    return [
        context
        for context in entry.get("contexts", [])
        if isinstance(context, dict)
        and context.get("context_id")
        and (
            str(context["context_id"]) not in before
            or before[str(context["context_id"])] != context_signature(context)
        )
    ]


def observation_signature(observation: dict) -> tuple:
    return (
        observation.get("access_state"),
        observation.get("observed_at"),
        observation.get("context_id"),
    )


def changed_observations(entry: dict, baseline: dict | None) -> list[dict]:
    before = {
        observation.get("seq"): observation_signature(observation)
        for observation in (baseline or {}).get("observations", [])
        if isinstance(observation, dict)
    }
    return [
        observation
        for observation in entry.get("observations", [])
        if isinstance(observation, dict)
        and (
            observation.get("seq") not in before
            or before[observation.get("seq")] != observation_signature(observation)
        )
    ]


def acquisition_state(entry: dict) -> tuple:
    return tuple(
        sorted(
            (
                acquisition.get("acquisition_id"),
                acquisition.get("context_id"),
                acquisition.get("outcome"),
                acquisition.get("finished_at"),
                json.dumps(acquisition.get("measured_spec"), sort_keys=True, separators=(",", ":")),
            )
            for acquisition in entry.get("acquisitions", [])
        )
    )


def acquisition_signature(acquisition: dict) -> tuple:
    return (
        acquisition.get("context_id"),
        acquisition.get("outcome"),
        acquisition.get("finished_at"),
        json.dumps(acquisition.get("measured_spec"), sort_keys=True, separators=(",", ":")),
    )


def changed_acquisitions(entry: dict, baseline: dict | None) -> list[dict]:
    before = {
        str(acquisition.get("acquisition_id")): acquisition_signature(acquisition)
        for acquisition in (baseline or {}).get("acquisitions", [])
        if isinstance(acquisition, dict) and acquisition.get("acquisition_id")
    }
    return [
        acquisition
        for acquisition in entry.get("acquisitions", [])
        if isinstance(acquisition, dict)
        and acquisition.get("acquisition_id")
        and (
            str(acquisition["acquisition_id"]) not in before
            or before[str(acquisition["acquisition_id"])]
            != acquisition_signature(acquisition)
        )
    ]


def media_byte_deltas(
    entry: dict,
    baseline: dict | None,
    before: dict,
    after: dict,
) -> dict[str, int]:
    """Per-context media bytes added during this check, including old contexts."""
    context_ids = set(context_index(entry)) | set(context_index(baseline or {}))
    before_bytes = before.get("evidence_media_bytes") or {}
    after_bytes = after.get("evidence_media_bytes") or {}
    return {
        context_id: int(after_bytes.get(context_id, 0) or 0)
        - int(before_bytes.get(context_id, 0) or 0)
        for context_id in context_ids
        if int(after_bytes.get(context_id, 0) or 0)
        != int(before_bytes.get(context_id, 0) or 0)
    }


def request_owner(evidence: dict) -> dict[str, str]:
    return {
        str(context["context_id"]): str(entry["entry_id"])
        for entry in evidence.get("entries", [])
        for context in entry.get("contexts", [])
        if isinstance(context, dict) and context.get("context_id")
    }


def first_round_activity(
    entry: dict,
    baseline: dict | None,
    requests: list[dict],
    media_bytes: dict[str, int],
) -> dict[str, list]:
    context_ids = set(context_index(entry)) | set(context_index(baseline or {}))
    return {
        "observations": changed_observations(entry, baseline),
        "contexts": changed_contexts(entry, baseline),
        "jobs": changed_jobs(entry, baseline),
        "acquisitions": changed_acquisitions(entry, baseline),
        "requests": [
            request for request in requests if request.get("evidence_id") in context_ids
        ],
        "mediaBytes": [
            {"context_id": context_id, "bytes": value}
            for context_id, value in media_bytes.items()
            if value
        ],
    }


def attributed_jobs(entry: dict, job_type: str, contexts: list[dict]) -> list[dict]:
    """Successful consumer jobs whose exact job generation created a new context."""
    generations = {
        (context.get("job_id"), context.get("job_generation"))
        for context in contexts
        if isinstance(context.get("job_id"), str)
        and isinstance(context.get("job_generation"), int)
    }
    return [
        job
        for job in succeeded_consumer_jobs(entry, job_type)
        if (job.get("job_id"), job.get("job_generation")) in generations
    ]


def succeeded_consumer_jobs(entry: dict, job_type: str) -> list[dict]:
    return [
        job
        for job in entry.get("jobs", [])
        if job.get("job_type") == job_type
        and job.get("state") == "succeeded"
        and job.get("consumer_state") == "succeeded"
        and _non_negative_int(job.get("consumer_requested"))
        and _non_negative_int(job.get("consumer_completed"))
        and 0 < job["consumer_requested"] <= job["consumer_completed"]
        and isinstance(job.get("job_id"), str)
        and isinstance(job.get("job_generation"), int)
    ]


def in_range(entry: dict, span: dict, now: datetime) -> bool:
    stamp = entry.get("published_at")
    if not stamp:
        return False
    at = datetime.fromisoformat(str(stamp).replace("Z", "+00:00"))
    if not span or span.get("kind") != "date_range":
        days = int((span or {}).get("days", 0))
        return now - timedelta(days=days) <= at <= now
    start = datetime.fromisoformat(str(span["start"]).replace("Z", "+00:00"))
    end = datetime.fromisoformat(str(span["end"]).replace("Z", "+00:00"))
    return start <= at <= end


def wait_found(run: SiteRun, before: set, timeout: float = 150) -> list[dict]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        found = [e for e in timeline(run) if e["entry_id"] not in before]
        if found:
            return found
        stop_if_pushed_back(run)
        time.sleep(1)
    return []


def check_incremental(run: SiteRun, auto: bool) -> dict:
    """A subscription check with automatic download off (or on): what was found, what was fetched.

    The range is the instant one listed work was published, so the check finds that work. Off: the
    work is recorded and its video stays "not downloaded"; a second check finds nothing new and
    changes nothing. On: that work is downloaded through a successful media consumer owned by this
    subscription. Both modes run the second check. The subscription is paused as soon as the first
    check has found its work, so its walk through history asks nothing more.
    """
    listed, unused = run.preview["items"], frozenset(run.used_keys)
    item = pick(listed, "video", skip=unused, dated=True, recent=RECENT_FOR_INCREMENTAL)
    if item and item.get("published_precision") not in (None, "", "source"):
        # A window of days holds every newer work too: take the newest, so the window is the smallest.
        item = pick(listed, "video", skip=unused, dated=True, recent=1) or item
    located = ""
    if not item:
        # A list without dates (contracts §5.3): the work's own page gives the instant to aim at.
        candidate = pick(listed, "video", skip=unused)
        if candidate:
            found = detail_date(run, candidate)
            if found:
                item = dict(candidate, **found)
                located = "date from the work's own page"
    if not item:
        return {"outcome": "not-run", "note": "no dated, ordinary, short video among the listed works, and no detail date for an undated one"}
    run.used_keys.add(item["native_key"])
    before_entries = timeline(run)
    before = {entry["entry_id"] for entry in before_entries}
    before_requests = request_snapshot(run)
    before_media = run.counters()[1]
    existing_subscription = getattr(run, "subscription_id", None)
    before_evidence = (
        subscription_evidence(run, existing_subscription) if existing_subscription else None
    )
    if before_evidence:
        problem = evidence_problem(before_evidence, existing_subscription)
        if problem:
            return {"ok": False, "note": f"before the check, {problem}"}
    sub = own_subscription(run, item, auto)
    span = incremental_range(item)
    baseline = before_evidence or {
        "subscription_id": sub,
        "entries": [],
        "requests": [],
        "evidence_media_bytes": {},
    }
    windowed = item.get("published_precision") not in (None, "", "source", "exact")
    try:
        found = wait_found(run, before)
        if found and windowed:
            # A window of days holds more than the one work: let the check read it through before pausing.
            wait_still(run)
    finally:
        set_enabled(run, sub, False)
    if not found:
        return {"ok": False, "note": f"the check found no entry in its range{'; ' + located if located else ''}"}
    if not wait_quiet(run):
        return {"ok": False, "note": "the detail and download jobs did not rest in time"}
    entries_after = timeline(run)
    ids = {e["entry_id"] for e in entries_after} - before
    found_entries = [e for e in entries_after if e["entry_id"] in ids]
    states = sorted({e["save_progress"] for e in found_entries})
    prefix = f"{located}; " if located else ""
    target = next((e for e in found_entries if e.get("native_key") == item["native_key"]), None)
    if target is None:
        return {"ok": False, "note": f"{prefix}the listed work was not discovered by its own subscription"}
    if not in_range(target, span, datetime.now(timezone.utc)):
        return {"ok": False, "note": f"{prefix}the discovered work is outside the requested range"}
    first_requests = new_requests(before_requests, request_snapshot(run))
    media_delta = run.counters()[1] - before_media
    after_evidence = subscription_evidence(run, sub)
    problem = evidence_problem(after_evidence, sub)
    if problem:
        return {"ok": False, "note": f"{prefix}{problem}"}
    evidence_by_id = evidence_entries(after_evidence)
    after_entries = evidence_by_id
    target_evidence = next(
        (
            entry
            for entry in after_evidence["entries"]
            if entry.get("native_key") == item["native_key"]
        ),
        None,
    )
    if target_evidence is None or target_evidence.get("entry_id") != target["entry_id"]:
        return {
            "ok": False,
            "note": f"{prefix}the listed work is not a discovery of this subscription",
            "range": span,
        }
    before_relations = relation_map(baseline)
    after_relations = relation_map(after_evidence)
    removed_relations = set(before_relations) - set(after_relations)
    changed_relations = {
        entry_id
        for entry_id, relation in after_relations.items()
        if before_relations.get(entry_id) != relation
    }
    if removed_relations:
        return {
            "ok": False,
            "note": f"{prefix}{len(removed_relations)} existing work(s) lost their subscription relation",
            "range": span,
        }
    if target["entry_id"] not in changed_relations:
        return {
            "ok": False,
            "note": f"{prefix}the target relation was not added or refreshed by this check",
            "range": span,
        }
    target_discoveries = target_evidence.get("discovered_by_subscriptions") or []
    if not any(
        item.get("subscription_id") == sub
        for item in target_discoveries
        if isinstance(item, dict)
    ):
        return {
            "ok": False,
            "note": f"{prefix}the listed work is not a discovery of this subscription",
            "range": span,
        }
    outside_relations = [
        entry_id
        for entry_id in changed_relations
        if entry_id in evidence_by_id
        and not in_range(evidence_by_id[entry_id], span, datetime.now(timezone.utc))
    ]
    if outside_relations:
        return {
            "ok": False,
            "note": f"{prefix}{len(outside_relations)} out-of-range work(s) were recorded as this subscription's discoveries",
            "range": span,
        }
    outside = [e for e in found_entries if not in_range(e, span, datetime.now(timezone.utc))]
    if outside:
        return {"ok": False, "note": f"{prefix}{len(outside)} discovered work(s) outside the requested range"}
    baseline_entries = {
        str(entry["entry_id"]): entry for entry in (baseline or {}).get("entries", [])
    }
    target_before = baseline_entries.get(target["entry_id"]) or {}
    target_observation_before = int(target_before.get("observation_count") or 0)
    if int(target_evidence.get("observation_count") or 0) <= target_observation_before:
        return {"ok": False, "note": f"{prefix}observations did not grow for the target"}
    target_latest_before = int(target_before.get("latest_observation_seq") or 0)
    if int(target_evidence.get("latest_observation_seq") or 0) <= target_latest_before:
        return {"ok": False, "note": f"{prefix}latest observation did not grow for the target"}
    list_endpoint = next(
        (
            request.get("endpoint")
            for request in first_requests
            if request.get("metadata") is not False and request.get("endpoint")
        ),
        None,
    )
    if not list_endpoint:
        return {"ok": False, "note": f"{prefix}no list endpoint is attributable to this check"}
    target_new_contexts = new_contexts(target_evidence, target_before)
    target_new_context_ids = {
        str(context["context_id"]) for context in target_new_contexts
    }
    if not target_new_context_ids:
        return {
            "ok": False,
            "note": f"{prefix}the target observation has no new acquisition context",
            "range": span,
        }
    target_changed_jobs = changed_jobs(target_evidence, target_before)
    target_changed_job_ids = {
        str(job["job_id"])
        for job in target_changed_jobs
        if isinstance(job.get("job_id"), str)
    }
    observe_jobs = attributed_jobs(
        target_evidence, "observe", target_new_contexts
    )
    observe_jobs = [
        job
        for job in observe_jobs
        if str(job.get("job_id")) in target_changed_job_ids
    ]
    if not observe_jobs:
        return {
            "ok": False,
            "note": f"{prefix}the target has no newly successful observe consumer owned by this subscription",
            "range": span,
        }
    observe_generations = {
        (job.get("job_id"), job.get("job_generation")) for job in observe_jobs
    }
    observe_contexts = [
        context
        for context in target_new_contexts
        if (context.get("job_id"), context.get("job_generation"))
        in observe_generations
    ]
    observe_context_ids = {
        str(context["context_id"]) for context in observe_contexts
    }
    new_observations = changed_observations(target_evidence, target_before)
    if not new_observations:
        return {
            "ok": False,
            "note": f"{prefix}the target has no newly recorded observation",
            "range": span,
        }
    if any(
        str(observation.get("context_id")) not in observe_context_ids
        for observation in new_observations
    ):
        return {
            "ok": False,
            "note": f"{prefix}a new target observation is not tied to its successful observe execution",
            "range": span,
        }
    first_request_delta = request_delta(
        (baseline or {}).get("requests", []), after_evidence["requests"]
    )
    detail_requests = [
        request
        for request in first_request_delta
        if request.get("metadata") is not False
        and request.get("evidence_id") in observe_context_ids
        and isinstance(request.get("status"), int)
        and 200 <= request["status"] < 400
        and request.get("endpoint") != list_endpoint
    ]
    if not detail_requests:
        return {
            "ok": False,
            "note": f"{prefix}no detail request is attributable to the target observe consumer",
            "range": span,
        }

    owners = request_owner(after_evidence)
    unattributed_requests = [
        request for request in first_requests if not request.get("evidence_id")
    ]
    if any(request.get("metadata") is False for request in unattributed_requests):
        return {
            "ok": False,
            "note": f"{prefix}a media request has no subscription execution context",
            "range": span,
        }
    if any(request.get("evidence_id") not in owners for request in first_request_delta):
        return {
            "ok": False,
            "note": f"{prefix}a first-round request has no related work",
            "range": span,
        }

    activity_by_entry: dict[str, dict[str, list]] = {}
    media_bytes_by_entry: dict[str, dict[str, int]] = {}
    for entry_id in set(after_entries) | set(baseline_entries):
        entry = after_entries.get(entry_id, {})
        baseline_entry = baseline_entries.get(entry_id)
        media_bytes = media_byte_deltas(
            entry, baseline_entry, baseline, after_evidence
        )
        media_bytes_by_entry[entry_id] = media_bytes
        activity_by_entry[entry_id] = first_round_activity(
            entry, baseline_entry, first_request_delta, media_bytes
        )
    if any(
        value < 0
        for values in media_bytes_by_entry.values()
        for value in values.values()
    ):
        return {
            "ok": False,
            "note": f"{prefix}a per-execution media counter moved backwards",
            "range": span,
        }
    for entry_id, activity in activity_by_entry.items():
        evidence_entry = after_entries.get(entry_id) or baseline_entries[entry_id]
        if in_range(evidence_entry, span, datetime.now(timezone.utc)):
            continue
        if any(activity.values()):
            changed = ", ".join(
                name for name, values in activity.items() if values
            )
            return {
                "ok": False,
                "note": f"{prefix}an out-of-range related work had first-round activity ({changed})",
                "range": span,
            }

    media_jobs_first = [
        job
        for activity in activity_by_entry.values()
        for job in activity["jobs"]
        if job.get("job_type") == "media"
    ]
    media_requests_first = [
        request for request in first_request_delta if request.get("metadata") is False
    ]
    acquired_first = [
        acquisition
        for activity in activity_by_entry.values()
        for acquisition in activity["acquisitions"]
        if acquisition.get("outcome") == "acquired"
    ]
    attributed_media_bytes = sum(
        value
        for values in media_bytes_by_entry.values()
        for value in values.values()
        if value > 0
    )
    if auto:
        for entry_id, activity in activity_by_entry.items():
            entry = after_entries[entry_id]
            changed_media_ids = {
                str(job.get("job_id"))
                for job in activity["jobs"]
                if job.get("job_type") == "media"
            }
            successful_media_jobs = [
                job
                for job in succeeded_consumer_jobs(entry, "media")
                if str(job.get("job_id")) in changed_media_ids
            ]
            backed_contexts = {
                str(context["context_id"])
                for context in activity["contexts"]
                if any(
                    context.get("job_id") == job.get("job_id")
                    and context.get("job_generation") == job.get("job_generation")
                    for job in successful_media_jobs
                )
            }
            unbacked_requests = [
                request
                for request in activity["requests"]
                if request.get("metadata") is False
                and request.get("evidence_id") not in backed_contexts
            ]
            unbacked_bytes = [
                item
                for item in activity["mediaBytes"]
                if item["bytes"] > 0 and item["context_id"] not in backed_contexts
            ]
            unbacked_acquisitions = [
                acquisition
                for acquisition in activity["acquisitions"]
                if acquisition.get("outcome") == "acquired"
                and acquisition.get("context_id") not in backed_contexts
            ]
            if unbacked_requests or unbacked_bytes or unbacked_acquisitions:
                return {
                    "ok": False,
                    "note": f"{prefix}a media request, byte count or acquisition is not tied to a newly successful media execution",
                    "range": span,
                }
        successful_media = attributed_jobs(
            target_evidence, "media", target_new_contexts
        )
        successful_media = [
            job
            for job in successful_media
            if str(job.get("job_id")) in target_changed_job_ids
        ]
        media_contexts = {
            str(context["context_id"])
            for context in target_new_contexts
            if any(
                context.get("job_id") == job.get("job_id")
                and context.get("job_generation") == job.get("job_generation")
                for job in successful_media
            )
        }
        acquired = [
            acquisition
            for acquisition in target_evidence.get("acquisitions", [])
            if acquisition.get("context_id") in media_contexts
            and acquisition.get("outcome") == "acquired"
        ]
        target_media_bytes = sum(
            max(0, media_bytes_by_entry[target["entry_id"]].get(context, 0))
            for context in media_contexts
        )
        media_requests = [
            request
            for context in media_contexts
            for request in first_request_delta
            if request.get("evidence_id") == context
            if request.get("metadata") is False
            and isinstance(request.get("status"), int)
            and 200 <= request["status"] < 400
        ]
        downloaded = (
            target.get("save_progress") in ("complete", "partial")
            and bool(successful_media)
            and bool(acquired)
            and bool(media_requests)
            and target_media_bytes > 0
        )
        if not downloaded:
            return {
                "ok": False,
                "note": f"{prefix}target download {target.get('save_progress')} is not backed by this subscription's successful media execution",
                "range": span,
            }
        attributed_media_bytes = target_media_bytes
    else:
        if not all(e["save_progress"] == "deferred" for e in found_entries):
            return {
                "ok": False,
                "note": f"{prefix}auto-download off saved media for {states}",
                "range": span,
            }
        media_requests = []
        if (
            media_jobs_first
            or media_requests_first
            or acquired_first
            or attributed_media_bytes
            or media_delta != 0
        ):
            return {
                "ok": False,
                "note": f"{prefix}auto-download off created {len(media_jobs_first)} media job(s), {len(media_requests_first)} media request(s) and {attributed_media_bytes or media_delta} media bytes",
                "range": span,
            }
    requests_before_second = request_snapshot(run)
    media_before_second = run.counters()[1]
    evidence_before_second = subscription_evidence(run, sub)
    problem = evidence_problem(evidence_before_second, sub)
    if problem:
        return {"ok": False, "note": f"before the second check, {problem}"}
    set_enabled(run, sub, True)
    run.core.call("subscription.check_now", {"subscription_id": sub})
    wait_still(run)
    set_enabled(run, sub, False)
    if not wait_quiet(run):
        return {"ok": False, "note": "the second check did not rest in time"}
    second_requests = request_delta(requests_before_second, request_snapshot(run))
    second_media_delta = run.counters()[1] - media_before_second
    entries_again = timeline(run)
    again = {e["entry_id"] for e in entries_again} - before
    same = again == ids
    evidence_after_second = subscription_evidence(run, sub)
    problem = evidence_problem(evidence_after_second, sub)
    if problem:
        return {"ok": False, "note": f"after the second check, {problem}"}
    before_second_entries = evidence_entries(evidence_before_second)
    after_second_entries = evidence_entries(evidence_after_second)
    relations_unchanged = relation_state(evidence_before_second) == relation_state(
        evidence_after_second
    )
    observations_unchanged = set(before_second_entries) == set(
        after_second_entries
    ) and all(
        observation_state(entry)
        == observation_state(before_second_entries[entry_id])
        for entry_id, entry in after_second_entries.items()
    )
    contexts_unchanged = set(before_second_entries) == set(after_second_entries) and all(
        context_state(entry) == context_state(before_second_entries[entry_id])
        for entry_id, entry in after_second_entries.items()
    )
    jobs_unchanged = set(before_second_entries) == set(after_second_entries) and all(
        job_state(entry) == job_state(before_second_entries[entry_id])
        for entry_id, entry in after_second_entries.items()
    )
    acquisitions_unchanged = set(before_second_entries) == set(
        after_second_entries
    ) and all(
        acquisition_state(entry)
        == acquisition_state(before_second_entries[entry_id])
        for entry_id, entry in after_second_entries.items()
    )
    evidence_media_unchanged = (
        evidence_before_second.get("evidence_media_bytes")
        == evidence_after_second.get("evidence_media_bytes")
    )
    repeated_jobs = sorted(
        job_id
        for entry_id, entry in after_second_entries.items()
        for job_id in (
            job.get("job_id")
            for job in changed_jobs(entry, before_second_entries.get(entry_id))
        )
        if job_id
    )
    endpoints_ok = (
        len(second_requests) == 1
        and second_requests[0].get("metadata") is not False
        and second_requests[0].get("endpoint") == list_endpoint
        and isinstance(second_requests[0].get("status"), int)
        and 200 <= second_requests[0]["status"] < 400
    )
    save_state_ok = (
        target.get("save_progress") in ("complete", "partial")
        if auto
        else all(e["save_progress"] == "deferred" for e in found_entries)
    )
    ok = (
        same
        and save_state_ok
        and relations_unchanged
        and observations_unchanged
        and contexts_unchanged
        and jobs_unchanged
        and acquisitions_unchanged
        and evidence_media_unchanged
        and not repeated_jobs
        and endpoints_ok
        and second_media_delta == 0
    )
    return {
        "ok": ok,
        "note": f"{prefix}{len(found_entries)} entry(ies), discovery {sub}, save states {states}, second check {'changed nothing' if same else 'changed the entry set'}, observations {'unchanged' if observations_unchanged else 'grew'}, detail jobs {'added' if repeated_jobs else 'none'}, endpoints {'list only' if endpoints_ok else 'unexpected'}, {second_media_delta} second-check media bytes",
        "range": span,
        "firstRequests": first_requests,
        "secondRequests": second_requests,
        "discoveredBy": target_evidence.get("discovered_by_subscriptions", []),
        "observationsBefore": target_observation_before,
        "observationsAfter": target_evidence.get("observation_count"),
        "detailRequests": detail_requests,
        "mediaRequests": media_requests if auto else [],
        "evidenceMediaBytes": attributed_media_bytes,
        "secondRound": {
            "entrySetUnchanged": same,
            "relationsUnchanged": relations_unchanged,
            "observationsUnchanged": observations_unchanged,
            "contextsUnchanged": contexts_unchanged,
            "jobsUnchanged": jobs_unchanged,
            "acquisitionsUnchanged": acquisitions_unchanged,
            "mediaEvidenceUnchanged": evidence_media_unchanged,
            "newJobs": repeated_jobs,
            "endpointOnly": endpoints_ok,
            "saveState": save_state_ok,
        },
    }


STEPS = ("account", "list", "single-video", "preview-download", "incremental-off", "incremental-on", "single-image", "single-text")


def run_site(root: Path, site: str, package: Path, steps: list[str], label: str | None = None) -> dict:
    run_dir = root / LOCAL / (label or time.strftime("run-%Y%m%d-%H%M%S")) / site
    with SiteRun(root, site, package, run_dir) as run:
        run.preview = None
        run.used_keys = set()
        rules = RULES[site]
        both = rules["sub"] + rules["preview"]
        plan = {
            "account": ("account-recognition", both, lambda: check_account(run)),
            "list": ("list-two-pages", both, lambda: check_list(run, RULES[site]["preview"][0])),
            "preview-download": ("preview-select-download", rules["preview"], lambda: check_preview_download(run)),
            "single-video": ("single-link-download", rules["video"], lambda: check_single_link(run, "video")),
            "single-image": ("single-link-download", rules["post"], lambda: check_single_link(run, "image")),
            "single-text": ("single-link-download", rules["post"], lambda: check_single_link(run, "text")),
            "incremental-off": ("incremental-auto-off", rules["sub"], lambda: check_incremental(run, False)),
            "incremental-on": ("incremental-auto-on", rules["sub"], lambda: check_incremental(run, True)),
        }
        list_tried = False
        for name in steps:
            title, step_rules, action = plan[name]
            if run.abandoned:
                run.checks.append({"name": title, "rules": step_rules, "outcome": "not-run", "note": run.abandoned, "metadataRequests": 0, "mediaBytes": 0})
                continue
            if name != "list" and name.startswith(("preview", "single", "incremental")) and run.preview is None and not run.stopped:
                if list_tried:
                    run.checks.append({"name": title, "rules": step_rules, "outcome": "not-run", "note": "the list of works did not load", "metadataRequests": 0, "mediaBytes": 0})
                    continue
                list_tried = True
                # Only a run that names the list check claims two pages; the others need one to choose from.
                wanted = 2 if "list" in steps else 1
                run.step("list-two-pages" if wanted == 2 else "list-first-page", both if wanted == 2 else [], lambda: check_list(run, RULES[site]["preview"][0], wanted))
                if run.preview is None:
                    run.checks.append({"name": title, "rules": step_rules, "outcome": "not-run", "note": "the list of works did not load", "metadataRequests": 0, "mediaBytes": 0})
                    continue
            if name == "list":
                list_tried = True
            run.step(title, step_rules, action)
            if name == "account" and run.checks and run.checks[-1]["outcome"] != "passed":
                run.abandoned = "account recognition did not pass; every later check needs it"
        receipt = run.receipt()
    out = run_dir / "receipt.json"
    out.write_text(json.dumps(receipt, ensure_ascii=False, indent=2), encoding="utf-8")
    return receipt


RANK = {"passed": 0, "not-run": 1, "failed": 2, "stopped": 3}


def not_applicable(check: dict) -> bool:
    """A link check that found no work of its kind on the account (no text post, no picture) did not
    run for want of a candidate; that says nothing about the kinds that did run."""
    return check["outcome"] == "not-run" and check.get("note", "").startswith("no ") and " candidate" in check.get("note", "")


def merge_receipt(data: dict, receipt: dict, date: str | None = None) -> None:
    """Fold one receipt's outcomes into the public record (counts and outcomes only).

    Within one receipt several checks of the same name (a video and an image link) must all pass
    for the name to pass, except a kind the account has no work of (see `not_applicable`); a later
    receipt replaces what an earlier one said about the same name.
    """
    site = receipt["site"]
    entry = data["sites"][site]
    run = entry.setdefault("run", {})
    run.update(date=date or receipt["date"], stoppedBy=receipt.get("stopReason"))
    groups: dict[tuple[str, str], list[dict]] = {}
    # A check cut short by the authorized budget did not meet any push-back: it was not completed.
    ran_out = receipt.get("stopReason") == "budget"
    for check in receipt["checks"]:
        check = {**check, "outcome": "not-run" if ran_out and check["outcome"] == "stopped" else check["outcome"]}
        for rid in check["rules"]:
            if rid not in entry["rules"]:
                raise ValueError(f"receipt names a rule the site does not declare: {rid}")
            groups.setdefault((rid, check["name"]), []).append(check)
    for (rid, name), members in groups.items():
        counted = [c for c in members if not not_applicable(c)] or members
        outcome = max((c["outcome"] for c in counted), key=RANK.__getitem__)
        result = {"name": name, "outcome": outcome,
                  "requests": sum(c.get("metadataRequests") or 0 for c in members),
                  "mediaBytes": sum(c.get("mediaBytes") or 0 for c in members)}
        rule = entry["rules"][rid]
        rule["checks"] = [c for c in rule.get("checks", []) if c["name"] != name] + [result]


def record(root: Path, sites: list[str]) -> int:
    path = root / rule_evidence.FILE
    data = json.loads(path.read_text(encoding="utf8"))
    ledger = Ledger(root)
    for site in sites:
        # Oldest first: a later receipt replaces what an earlier one said (labels do not sort by time).
        receipts = sorted((root / LOCAL).glob(f"run-*/{site}/receipt.json"), key=lambda p: p.stat().st_mtime)
        for receipt_path in receipts:
            # The day the run ended on the user's clock; the receipt's own date is UTC.
            ended = time.strftime("%Y-%m-%d", time.localtime(receipt_path.stat().st_mtime))
            receipt = json.loads(receipt_path.read_text(encoding="utf8"))
            errors = verify_package_identity(root, receipt)
            if errors:
                raise ValueError(f"{receipt_path}: {'; '.join(errors)}")
            merge_receipt(data, receipt, ended)
        used = ledger.used(site)
        data["sites"][site].setdefault("run", {}).update(metadataRequests=used["metadata"], mediaBytes=used["mediaBytes"])
    # Bytes, not text: the record stays LF on Windows, where text mode would write CRLF.
    path.write_bytes((json.dumps(data, ensure_ascii=False, indent=2) + chr(10)).encode("utf8"))
    return 0


def main(argv: list[str] | None = None) -> int:
    import evidence

    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="action", required=True)
    for name in ("prepare", "status", "record"):
        step = sub.add_parser(name)
        step.add_argument("--site", action="append")
    run = sub.add_parser("run")
    run.add_argument("--site", required=True)
    run.add_argument("--package", type=Path, required=True)
    run.add_argument("--steps", default=",".join(STEPS))
    run.add_argument("--label")
    args = parser.parse_args(argv)
    sites = args.site if isinstance(args.site, list) else [args.site] if getattr(args, "site", None) else evidence.adapter_sites(ROOT)
    try:
        if args.action == "prepare":
            print(json.dumps(prepare(ROOT, sites), indent=2))
            return 0
        if args.action == "status":
            ledger = Ledger(ROOT)
            print(json.dumps({s: ledger.remaining(ROOT, s) for s in sites}, indent=2))
            return 0
        if args.action == "record":
            return record(ROOT, sites)
        steps = [s for s in args.steps.split(",") if s]
        unknown = [s for s in steps if s not in STEPS]
        if unknown:
            raise PreparationError(f"unknown steps {unknown}")
        receipt = run_site(ROOT, args.site, args.package, steps, args.label)
        print(json.dumps({k: receipt[k] for k in ("site", "stopReason", "metadataRequests", "mediaBytes")} | {"checks": [{k: c[k] for k in ("name", "outcome", "note")} for c in receipt["checks"]]}, ensure_ascii=False, indent=2))
        return 1 if receipt["stopReason"] else 0
    except PreparationError as error:
        print(f"[NOT READY] {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
