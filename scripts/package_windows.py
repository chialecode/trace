#!/usr/bin/env python3
"""TRACE Windows x64 便携包（P8）。

产物：dist/trace-<version>-windows-x64/ 含
  trace-desktop.exe / trace-daemon.exe / adapters/trace-adapter-bilibili.exe
  profile.example.json / README.md / SHA256SUMS.txt / manifest.json

约束（C32）：
- 不打包测试件（trace-fake-adapter 等 e2e 二进制）；
- 发行 profile 不含 test_allowed_hosts（doctor 自检强制）；
- 步骤失败即非 0 退出，保留日志；不自动安装/不发布。

安装包（NSIS/WiX）需要网络与工具链；本脚本只出便携包，
安装包生成以 `pnpm tauri build` 另行执行并在证据中记录是否运行。
"""

from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
import sys
import time
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "dist"


def which_corepack() -> str:
    from shutil import which
    p = which("corepack.cmd") or which("corepack")
    if p is None:
        print("[FAIL] 未找到 corepack（需要 Node >= 20 与 corepack enable）")
        sys.exit(1)
    return p


def run(cmd: list[str], cwd: Path = ROOT) -> str:
    proc = subprocess.run(cmd, cwd=str(cwd), capture_output=True, text=True, encoding="utf8", errors="replace")
    if proc.returncode != 0:
        print(f"[FAIL] {' '.join(cmd)}\n{proc.stdout[-1500:]}\n{proc.stderr[-1500:]}")
        sys.exit(1)
    return proc.stdout


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def source_snapshot() -> dict[str, str]:
    """Bind the package to the actual build inputs, including not-yet-committed source."""
    names = set(run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).split("\0")) - {""}
    prefixes = ("apps/", "adapters/", "crates/", "scripts/")
    roots = {"Cargo.toml", "Cargo.lock", "rust-toolchain.toml", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "media-backend.lock.json", ".cargo/config.toml"}
    return {name: sha256(ROOT / name) for name in sorted(names)
            if (name.startswith(prefixes) or name in roots) and (ROOT / name).is_file()}


def main() -> int:
    sources = source_snapshot()
    with (ROOT / "Cargo.toml").open("rb") as f:
        version = tomllib.load(f)["workspace"]["package"]["version"]
    out = DIST / f"trace-{version}-windows-x64-closeout"
    suffix = 1
    while out.exists():
        out = DIST / f"trace-{version}-windows-x64-closeout-{suffix}"
        suffix += 1
    (out / "adapters").mkdir(parents=True)

    # 1) 前端构建（Tauri 编译期嵌入 dist/）。
    print("== 前端构建")
    run([which_corepack(), "pnpm", "-C", "apps/desktop", "run", "build"])

    # 2) release 构建（daemon、适配器、桌面外壳）。
    print("== release 构建")
    run([
        "cargo", "build", "--release", "--locked",
        "-p", "trace-daemon",
        "-p", "trace-adapter-bilibili",
        "-p", "trace-desktop",
    ])

    rel = ROOT / "target" / "release"
    binaries = {
        "trace-desktop.exe": rel / "trace-desktop.exe",
        "trace-daemon.exe": rel / "trace-daemon.exe",
        "adapters/trace-adapter-bilibili.exe": rel / "trace-adapter-bilibili.exe",
    }
    for dst, src in binaries.items():
        if not src.exists():
            print(f"[FAIL] 缺少构建产物 {src}")
            return 1
        shutil.copy2(src, out / dst)

    # Local package verification includes only the pinned, previously verified backend.
    media_root = ROOT / "target" / "media"
    receipt = json.loads((media_root / "verified.json").read_text(encoding="utf8"))
    lock = json.loads((ROOT / "media-backend.lock.json").read_text(encoding="utf8"))
    if receipt["lock"] != lock:
        raise ValueError("media backend lock mismatch")
    (out / "media").mkdir()
    for name, expected in receipt["files"].items():
        source = media_root / name
        if source.name != name or sha256(source) != expected:
            raise ValueError("media backend checksum mismatch")
        shutil.copy2(source, out / "media" / name)
    for name in ("LICENSE.txt", "SOURCE.txt", "verified.json"):
        shutil.copy2(media_root / name, out / "media" / name)

    # 3) 发行 profile 样例（不得含测试端点）。
    example = {
        "archive_id": "default",
        "data_root": "./trace-data/data",
        "archive_root": "./trace-data/archive",
        "vault_dir": None,
        "adapter_dir": "./adapters",
        "extra_media_hosts": [],
        "test_allowed_hosts": [],
    }
    (out / "profile.example.json").write_text(
        json.dumps(example, ensure_ascii=False, indent=2) + "\n", encoding="utf8"
    )

    # 4) 发行自检：不得包含测试二进制；doctor 对样例 profile 通过。
    strays = [p.name for p in out.rglob("*") if "fake" in p.name.lower() or "e2e" in p.name.lower()]
    if strays:
        print(f"[FAIL] 发行包含测试件：{strays}")
        return 1
    doctor = subprocess.run(
        [str(out / "trace-daemon.exe"), "doctor", "--profile", str(out / "profile.example.json")],
        capture_output=True, text=True, encoding="utf8", errors="replace",
    )
    if doctor.returncode != 0:
        print(f"[FAIL] doctor 自检失败：{doctor.stdout[-800:]}\n{doctor.stderr[-800:]}")
        return 1
    doctor_json = json.loads(doctor.stdout)

    # 5) 校验和 + 清单 + README。
    sums = []
    for p in sorted(out.rglob("*")):
        if p.is_file() and p.name not in {"SHA256SUMS.txt", "manifest.json"}:
            sums.append(f"{sha256(p)}  {p.relative_to(out).as_posix()}")
    (out / "SHA256SUMS.txt").write_text("\n".join(sums) + "\n", encoding="utf8")

    git_commit = run(["git", "rev-parse", "HEAD"], ROOT).strip()
    manifest = {
        "product": "TRACE",
        "version": version,
        "target": "x86_64-pc-windows-msvc",
        "built_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "git_commit": git_commit,
        "components": list(binaries.keys()),
        "control_protocol": "0.1",
        "plugin_protocol": "0.1",
        "schema_version": 3,
        "source_dirty": bool(run(["git", "status", "--porcelain"], ROOT).strip()),
        "source_files_sha256": sources,
        "source_tree_sha256": hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
        "media_asset": lock,
        "excludes_test_artifacts": True,
        "doctor": doctor_json,
        "install": "便利包：解压后先复制 profile.example.json 为 profile.json（可按需改路径），再运行 trace-desktop.exe",
        "uninstall": "删除解压目录；用户数据位于 profile.json 指定的 data_root/trace-data 与系统凭证目录",
    }
    (out / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf8")

    media = doctor_json["media_backend"]
    webview = doctor_json["webview2"]
    (out / "README.md").write_text(
        f"""# TRACE {version}（Windows x64 便携包）

## 运行
1. 解压到任意目录（避免中文/超长路径问题）。
2. 复制 `profile.example.json` 为 `profile.json`；默认数据写入包内 `./trace-data/`。
3. 双击 `trace-desktop.exe`。外壳会在后台未运行时自动以无窗口方式拉起 `trace-daemon.exe`。

## 依赖诊断（本包构建时实测）
- WebView2 运行时：{"可用（" + str(webview["registry"] or "安装目录存在") + "）" if webview["available"] else "**未检测到**：请安装 Microsoft Edge WebView2 Runtime，否则窗口无法渲染"}
- ffmpeg/ffprobe：{"已检测（" + str(media["version"]) + "）" if media["ffprobe"] else "**未检测到**：仍可保存原始媒体并外部打开；探测/封装能力受限且界面如实显示未知"}
- 随时运行 `trace-daemon.exe doctor --profile profile.json` 复核（JSON 输出）。

## 卸载
删除解压目录即可。用户数据在 `profile.json` 指定的 `data_root`/`archive_root`（默认 `./trace-data/`）；
凭证存放于系统用户私有目录（DPAPI 加密），不随包。

## 校验
`SHA256SUMS.txt` 为全文件摘要；`manifest.json` 记录版本、提交与构建时诊断。

## 边界
- 本包不含测试适配器与测试端点（doctor 强制校验）。
- 安装包（NSIS）由 `pnpm tauri build` 另行生成；本便携包不自动安装、不写系统目录。
""",
        encoding="utf8",
    )

    if source_snapshot() != sources:
        raise ValueError("build inputs changed during packaging; rebuild the final source")
    # Generate sums after README/manifest creation so every shipped file is covered.
    sums = [f"{sha256(p)}  {p.relative_to(out).as_posix()}" for p in sorted(out.rglob("*")) if p.is_file() and p.name != "SHA256SUMS.txt"]
    (out / "SHA256SUMS.txt").write_text("\n".join(sums) + "\n", encoding="utf8")
    for line in sums:
        expected, name = line.split("  ", 1)
        if sha256(out / name) != expected:
            raise ValueError("package checksum verification failed")
    total = sum(p.stat().st_size for p in out.rglob("*") if p.is_file())
    print(f"[PASS] {out.relative_to(ROOT)}（{len(sums)} 文件，{total/1024/1024:.1f} MiB）")
    print(f"  doctor: webview2={webview['available']} ffprobe={bool(media['ffprobe'])} test_allowed_hosts_empty={doctor_json['release_check']['test_allowed_hosts_empty']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
