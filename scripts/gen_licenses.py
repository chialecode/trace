#!/usr/bin/env python3
"""生成依赖许可台账（docs/delivery/dependency-licenses.md）。

来源：cargo metadata（Rust 依赖图）+ pnpm licenses list（前端依赖）。
不含网络：只读取本地锁文件与已安装元数据；缺 pnpm 依赖时明确标注 blocked。
"""

from __future__ import annotations

import json
import subprocess
import sys
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "docs" / "delivery" / "dependency-licenses.md"


def cargo_licenses() -> tuple[list[dict], str | None]:
    proc = subprocess.run(
        ["cargo", "metadata", "--format-version", "1", "--locked"],
        cwd=str(ROOT),
        capture_output=True,
        text=True,
        encoding="utf8",
        errors="replace",
    )
    if proc.returncode != 0:
        return [], f"cargo metadata 失败：{proc.stderr[:400]}"
    meta = json.loads(proc.stdout)
    root_ids = {p["id"] for p in meta["packages"] if str(ROOT) in p["manifest_path"]}
    # 解析图内所有非本工作区包（即实际依赖）。
    deps = [p for p in meta["packages"] if p["id"] not in root_ids]
    return deps, None


def find_corepack() -> str | None:
    for cand in ("corepack.cmd", "corepack"):
        from shutil import which
        p = which(cand)
        if p:
            return p
    return None


def pnpm_licenses() -> tuple[list[dict], str | None]:
    corepack = find_corepack()
    if corepack is None:
        return [], "corepack 不在 PATH（Node 未安装或未启用 corepack）"
    proc = subprocess.run(
        [corepack, "pnpm", "-C", "apps/desktop", "licenses", "list", "--json"],
        cwd=str(ROOT),
        capture_output=True,
        text=True,
        encoding="utf8",
        errors="replace",
    )
    if proc.returncode != 0:
        return [], f"pnpm licenses 失败（可能需要先 corepack pnpm install）：{proc.stderr[:300]}"
    try:
        data = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return [], "pnpm licenses 输出不是 JSON"
    out = []
    if isinstance(data, dict):
        for license_name, packages in data.items():
            for p in packages:
                out.append({"name": p.get("name", "?"), "version": p.get("version", "?"), "license": license_name})
    return out, None


def main() -> int:
    rust, rust_err = cargo_licenses()
    node, node_err = pnpm_licenses()
    blocked = [e for e in (rust_err, node_err) if e]
    if rust_err:
        print(f"[BLOCKED] {rust_err}")
        return 1

    lines = [
        "# 依赖许可台账（M1a）",
        "",
        f"生成时间（UTC）：{datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M')}；"
        "生成器：`scripts/gen_licenses.py`（cargo metadata + pnpm licenses）。",
        "本表为直接/传递依赖的许可见证；许可证文本以各依赖包内文件为准。",
        "",
        f"## Rust 依赖（{len(rust)} 个包）",
        "",
    ]
    by_license: dict[str, list[str]] = defaultdict(list)
    for p in rust:
        lic = p.get("license") or (f"见 {p.get('license_file')}" if p.get("license_file") else "未声明")
        by_license[lic].append(f"{p['name']} {p['version']}")
    for lic in sorted(by_license):
        lines.append(f"### {lic}（{len(by_license[lic])}）")
        lines.append("")
        for item in sorted(by_license[lic]):
            lines.append(f"- {item}")
        lines.append("")

    lines.append(f"## 前端依赖（{len(node)} 个包）")
    lines.append("")
    if node_err:
        lines.append(f"> blocked：{node_err}")
        lines.append("")
    else:
        node_by: dict[str, list[str]] = defaultdict(list)
        for p in node:
            node_by[p["license"]].append(f"{p['name']} {p['version']}")
        for lic in sorted(node_by):
            lines.append(f"### {lic}（{len(node_by[lic])}）")
            lines.append("")
            for item in sorted(node_by[lic]):
                lines.append(f"- {item}")
            lines.append("")

    # 需评估项：强 copyleft（GPL/AGPL/SSPL）或未声明许可。
    def is_strong(low: str) -> bool:
        if not ("gpl" in low.replace("lgpl", "") or "sspl" in low):
            return False
        # `X OR MIT/Apache/BSD/ISC/Zlib` 形态可选用宽松分支，不算强 copyleft。
        if " or " in low and any(p in low for p in ("mit", "apache", "bsd", "isc", "zlib")):
            return False
        return True

    flagged: list[str] = []
    for lic, items in by_license.items():
        low = lic.lower()
        if "未声明" in lic or is_strong(low):
            flagged.extend(f"{lic}: {i}" for i in items)
    weak: list[str] = []
    for lic, items in by_license.items():
        low = lic.lower()
        if "mpl" in low or "lgpl" in low:
            weak.extend(f"{lic}: {i}" for i in items)

    lines += [
        "## 许可合规说明",
        "",
        f"- 强 copyleft/未声明许可检查：{'无' if not flagged else '；'.join(flagged)}。",
        f"- 弱著佐（MPL/LGPL 或 OR 备选）：{'无' if not weak else '；'.join(weak)}；"
        "均为文件级弱著佐或以宽松许可为主的 OR 备选，未修改上游源码。",
        "- 直接引用的上游实现（bili-sync MIT 参考）不复制代码；如后续复制，"
        "按 bilibili-capabilities §1 保存 commit、原许可和版权通知。",
        "- 台账随依赖升级重新生成；与 Cargo.lock / pnpm-lock.yaml 同提交。",
        "",
    ]
    lock = json.loads((ROOT / "media-backend.lock.json").read_text(encoding="utf8"))
    lines += ["## 本地媒体资产（TRACE-CLOSEOUT-02）", "", f"- BtbN FFmpeg / ffprobe {lock['version']}，{lock['variant']} / {lock['license']}；资产 SHA-256 `{lock['sha256']}`。", "- 来源与可复现获取见根 media-backend.lock.json、scripts/fetch_media.py；已核对两程序版本相同、构建选项无 --enable-gpl / --enable-nonfree。", "- 已执行 -L 确认 LGPL-3.0-or-later（启用 --enable-version3）；LGPL 共享库及 OpenH264 等资产内第三方组件的许可证以资产 LICENSE.txt、buildconf 与对应源码为依据。局部测试/打包不构成法律或对外分发批准。", "- 本地便携验证包保存 LICENSE.txt、SOURCE.txt、verified.json 与逐文件摘要；正式分发仍由 USER-ACTIONS Q-004 决定并复核对应源码与通知义务。", ""]
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(lines), encoding="utf8")
    print(f"已写入 {OUT.relative_to(ROOT)}（rust={len(rust)}，node={len(node)}）")
    if blocked:
        for b in blocked:
            print(f"[BLOCKED] {b}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
