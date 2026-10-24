#!/usr/bin/env python3
"""Inspect and execute an actual package without accounts or site requests."""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

import core_client
import evidence
from core_client import NO_WINDOW, Core, Reader, adapter_hello, write_frame
from webview_probe import Desktop


class PackageTests(unittest.TestCase):
    package: Path

    def test_minimized_embedded_webview_close_reconnect_and_full_exit(self):
        folder = self.package
        root = evidence.ROOT / "artifacts" / "evidence-runs" / f"webview-{time.time_ns()}"
        root.mkdir()
        profile = evidence.read(folder / "profile.example.json")
        profile.update(archive_id=root.name, data_root=str(root / "data"), archive_root=str(root / "archive"),
                       vault_dir=str(root / "vault"), adapter_dir=str(folder / "adapters"), media_dir=str(folder / "media"))
        path = root / "profile.json"
        path.write_text(json.dumps(profile), encoding="utf8")
        env = dict(os.environ)
        env.pop("TRACE_PROFILE", None)
        env.pop("TRACE_DAEMON_PATH", None)
        proc = subprocess.Popen([str(folder / "trace-daemon.exe"), "start", "--profile", str(path)], stdout=subprocess.PIPE,
                                stderr=subprocess.DEVNULL, env=env, creationflags=NO_WINDOW)
        windows = []
        observations = []
        try:
            ready = Reader(proc.stdout, lines=True).get(30)
            self.assertEqual(ready["status"], "ready")
            first = Desktop(folder / "trace-desktop.exe", path, root / "first", show_status=True)
            windows.append(first)
            info = first.action("status-entry", screenshot=root / "status.png")
            first.assert_background()
            self.assertTrue(info["notificationEntry"])
            self.assertTrue(info["acquisitionQueries"])
            self.assertIn("/upgrades", info["routes"])
            self.assertEqual(info["instanceId"], ready["instance_id"])
            observations.append(info)
            layout = first.action("layout-matrix")
            first.assert_background()
            self.assertEqual(layout["matrix"], 119)
            self.assertTrue(layout["browser360"])
            self.assertTrue(layout["titlebarActions"])
            self.assertTrue(layout["modalWithinViewport"])
            self.assertTrue(layout["floatingLayerWithinViewport"])
            self.assertTrue(layout["computedFontChanged"])
            self.assertTrue(layout["noHorizontalOverflow"])
            self.assertEqual(
                {
                    item["route"]
                    for item in layout["observations"]
                    if item["width"] == 360
                },
                {"/settings", "/", "/creators", "/timeline", "/explorer", "/tasks", "/management"},
            )
            self.assertEqual(
                {(item["scale"], round(item["rootFont"], 2)) for item in layout["observations"]},
                {(100, 16.0), (110, 17.6), (125, 20.0), (150, 24.0)},
            )
            self.assertTrue(
                all(item["bodyOverflow"] <= 1 and item["mainOverflow"] <= 1 for item in layout["observations"])
            )
            observations.append(
                {
                    "layoutMatrix": layout["matrix"],
                    "widths": layout["widths"],
                    "scales": layout["scales"],
                    "browser360": layout["browser360"],
                    "titlebarActions": layout["titlebarActions"],
                }
            )
            requested = first.action("close")
            first.wait(timeout=10)
            self.assertIsNone(proc.poll(), "closing GUI stopped daemon")
            observations.append(requested)
            second = Desktop(folder / "trace-desktop.exe", path, root / "second")
            windows.append(second)
            info = second.action()
            second.assert_background()
            self.assertEqual(info["instanceId"], ready["instance_id"])
            observations.append(info)
            other = Desktop(folder / "trace-desktop.exe", path, root / "other")
            windows.append(other)
            self.assertEqual(other.action()["instanceId"], ready["instance_id"])
            other.assert_background()
            second.action("exit")
            second.wait()
            self.assertEqual(proc.wait(timeout=25), 0)
            self.assertTrue(other.action("disconnected")["disconnected"])
            time.sleep(3)
            status = subprocess.run([str(folder / "trace-daemon.exe"), "status", "--profile", str(path)], capture_output=True,
                                    timeout=10, env=env, creationflags=NO_WINDOW)
            self.assertNotEqual(status.returncode, 0, "another open window restarted stopped daemon")
            other.action("close")
            other.wait(timeout=10)
            (root / "result.json").write_text(json.dumps({"status": "passed", "observations": observations, "noForegroundInput": True,
                                                          "productAcceptance": "not-run"}, ensure_ascii=False, indent=2), encoding="utf8")
        finally:
            for window in windows:
                window.dispose()
            if proc.poll() is None:
                subprocess.run([str(folder / "trace-daemon.exe"), "shutdown", "--profile", str(path)], capture_output=True,
                               timeout=30, env=env, creationflags=NO_WINDOW)
                try:
                    proc.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)
            proc.stdout.close()

    def test_packaged_sites_are_isolated_and_the_inventory_is_complete(self):
        folder = self.package
        self.assertEqual(evidence.validate_package(folder, evidence.inputs()), [])
        actual = evidence.inventory(folder)
        layout = evidence.package_layout()
        sums = {}
        for line in (folder / "SHA256SUMS.txt").read_text(encoding="utf8").splitlines():
            digest, name = line.split("  ", 1)
            evidence.contained(folder, name)
            self.assertNotIn(name, sums)
            sums[name] = digest
        self.assertEqual(sums, {n: s for n, s in actual.items() if n != "SHA256SUMS.txt"})
        manifest = evidence.read(folder / "manifest.json")
        self.assertEqual(manifest["sourceInputs"], evidence.inputs())
        self.assertEqual(manifest["productAcceptance"], "not-run")
        self.assertEqual(manifest["schema_version"], evidence.schema_version())
        self.assertEqual(manifest["built_binaries"], {n: actual[n] for n in manifest["components"]})
        self.assertEqual({Path(c["argv"][0]).stem for c in manifest["build_commands"]}, {"cargo", "corepack"})
        self.assertTrue(any("trace-desktop/custom-protocol" in c["argv"] for c in manifest["build_commands"]))
        for command in manifest["build_commands"]:
            self.assertEqual(command["exitCode"], 0)
            self.assertEqual(evidence.sha(evidence.contained(evidence.ROOT, command["log"])), command["logSha256"])
        self.assertEqual({n for n in actual if n.endswith(".exe")}, layout["executables"])
        for name in layout["legal"] + ["media/verified.json", "tools/verified.json"]:
            self.assertIn(name, actual)
        media = evidence.read(folder / "media/verified.json")
        self.assertEqual(media["lock"], evidence.read(evidence.ROOT / "media-backend.lock.json"))
        for name, digest in media["files"].items():
            self.assertEqual(actual["media/" + name], digest)
        tools = evidence.read(folder / "tools/verified.json")
        self.assertEqual(tools["lock"], evidence.read(evidence.ROOT / "media-tools.lock.json"))
        for name, digest in tools["files"].items():
            self.assertEqual(actual["tools/" + name], digest)
        profile = evidence.read(folder / "profile.example.json")
        self.assertEqual(profile["test_allowed_hosts"], [])
        self.assertFalse(profile.get("allow_media_path", False))
        env = dict(os.environ)
        env.pop("TRACE_PROFILE", None)
        sites = evidence.adapter_sites()
        self.assertGreaterEqual(len(sites), 8)
        # Direct handshakes never trigger the host proxy. Kill one adapter and demand a fresh
        # framed response from another after the failure.
        children = []
        try:
            for site in sites:
                response, proc, reader = adapter_hello(folder / f"adapters/trace-adapter-{site}.exe", env)
                children.append((proc, reader))
                self.assertTrue(response["ok"], site)
                self.assertEqual(response["result"]["platform"], site)
                self.assertTrue({"resolve_account", "enumerate", "observe", "resolve_media"} <= set(response["result"]["capabilities"]), site)
                self.assertTrue(response["result"]["rules"], f"{site} declares its rules")
            children[0][0].kill()
            children[0][0].wait(timeout=10)
            proc, reader = children[1]
            write_frame(proc.stdin, {"protocol_version": core_client.PROTOCOL, "request_id": "after-peer-death", "method": "hello",
                                     "params": {}, "deadline_ms": 5000})
            self.assertEqual(reader.get(10)["request_id"], "after-peer-death")
        finally:
            for proc, _ in children:
                if proc.poll() is None:
                    proc.kill()
                proc.wait(timeout=10)
                proc.stdin.close()
                proc.stdout.close()
        with tempfile.TemporaryDirectory(prefix="trace-package-") as temp:
            root = Path(temp)
            profile.update(archive_id=root.name, data_root=str(root / "data"), archive_root=str(root / "archive"),
                           vault_dir=str(root / "vault"), adapter_dir=str(folder / "adapters"), media_dir=str(folder / "media"))
            path = root / "profile.json"
            path.write_text(json.dumps(profile), encoding="utf8")
            daemon = folder / "trace-daemon.exe"
            doctor = subprocess.run([str(daemon), "doctor", "--profile", str(path)], capture_output=True, timeout=60, env=env,
                                    creationflags=NO_WINDOW)
            self.assertEqual(doctor.returncode, 0)
            diag = json.loads(doctor.stdout)
            self.assertTrue(diag["release_check"]["test_allowed_hosts_empty"])
            self.assertTrue(diag["media_backend"]["ffprobe"])
            self.assertTrue(diag["media_tools"]["ready"], diag["media_tools"])
            self.assertEqual(diag["media_tools"]["source"], "packaged")
            with Core(daemon, path, env, client="package-verifier") as core:
                plugins = core.call("plugin.list", {})["items"]
                self.assertEqual({p["platform"] for p in plugins}, set(sites))
                # The start-up refresh runs in the background; ask once so every site has declared
                # before one is disabled, and the disabled one keeps what it declared.
                self.assertEqual(core.call("site.refresh", {})["answered"], len(sites))
                core.call("plugin.set_state", {"plugin_id": "bilibili", "state": "disabled"})
                plugins = core.call("plugin.list", {})["items"]
                self.assertEqual(next(p["state"] for p in plugins if p["platform"] == "bilibili"), "disabled")
                self.assertNotEqual(next(p["state"] for p in plugins if p["platform"] == "douyin"), "disabled")
                core.call("site.refresh", {})
                catalog = core.call("site.catalog", {})
                self.assertEqual({site["id"] for site in catalog["sites"]}, set(sites))
                for site in catalog["sites"]:
                    self.assertTrue(site["rules"], f"{site['id']} has rules after its plugin is disabled or not")
                core.call("daemon.status", {})
                core.call("daemon.shutdown", {}, expect_ok=False)
                self.assertEqual(core.proc.wait(timeout=30), 0)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("package", type=Path)
    args = parser.parse_args()
    PackageTests.package = args.package.resolve()
    names = ["test_packaged_sites_are_isolated_and_the_inventory_is_complete",
             "test_minimized_embedded_webview_close_reconnect_and_full_exit"]
    result = unittest.TextTestRunner(verbosity=2).run(unittest.TestSuite(PackageTests(name) for name in names))
    return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    raise SystemExit(main())
