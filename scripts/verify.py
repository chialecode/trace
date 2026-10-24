#!/usr/bin/env python3
"""TRACE 验证入口：六套件（offline/desktop/recovery/site/resources/structure）。

语义：
- pass：套件命令全部成功；
- fail：命令失败（真实失败，保留原始输出）；
- blocked：前置缺失（工具/构建产物/授权样本）——不计通过，退出码非 0。

用法：
  python scripts/verify.py --suite offline
  python scripts/verify.py --all
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
from pathlib import Path
import time
import evidence

ROOT = Path(__file__).resolve().parent.parent

SITES = ["bilibili", "douyin", "weibo", "xiaohongshu", "instagram", "x", "youtube", "tiktok"]
SITE_CRATES = ["trace-site-support", *[f"trace-adapter-{site}" for site in SITES]]

# 套件 → (命令, cwd, 说明, 覆盖用例)
SUITES: dict[str, dict] = {
    "offline": {
        "desc": "离线测试：工作区全部单元/契约/适配器夹具测试",
        "covers": ["C01", "C02", "C03", "C04", "C05", "C06", "C07", "C08", "C09", "C10"],
        "cmd": ["cargo", "test", "--workspace", "--locked"],
        "prebuild": True,
        "blocked_if_missing": ["cargo"],
    },
    "desktop": {
        "desc": "桌面前端：类型检查 + Vitest + 生产构建",
        "covers": ["C11"],
        "cmd": ["__corepack__", "pnpm", "-C", "apps/desktop", "run", "verify"],
        "blocked_if_missing": ["corepack"],
    },
    "recovery": {
        "desc": "恢复与进程级：对象库恢复矩阵、离线闭环、daemon 双启动/退出/重启",
        "covers": ["C01", "C05", "C06", "C07"],
        "cmd": ["cargo", "test", "-p", "trace-core", "--locked", "--lib", "--", "recovery"],
        "extra_cmds": [
            ["cargo", "test", "-p", "trace-e2e", "--locked", "--test", "full_loop"],
            ["cargo", "test", "-p", "trace-e2e", "--locked", "--test", "daemon_process"],
        ],
        "prebuild": True,
        "blocked_if_missing": ["cargo"],
    },
    "site": {
        "desc": "八个站点适配器与共用工具：协议夹具、规则声明、签名向量与错误分类",
        "covers": ["C02", "C03", "C09", "C10"],
        "cmd": ["cargo", "test", "--locked", *[arg for site in SITE_CRATES for arg in ("-p", site)]],
        "blocked_if_missing": ["cargo"],
    },
    "structure": {
        "desc": "代码结构约束：IPC 层无 SQL、超长行、非测试文件规模",
        "covers": ["C01"],
        "cmd": [sys.executable, "scripts/check_structure.py"],
        "blocked_if_missing": [],
    },
    "resources": {
        "desc": "静态与资源检查：凭证哨兵不回显、发行配置无测试端点、依赖许可台账存在",
        "covers": ["C12"],
        "cmd": None,  # 由 python 内部检查实现
        "blocked_if_missing": [],
    },
}


def resolve(cmd: list[str]) -> list[str]:
    """把 __corepack__ 占位解析为可执行路径（Windows 上 .cmd 必须完整路径）。"""
    if cmd and cmd[0] == "__corepack__":
        for cand in ("corepack.cmd", "corepack"):
            p = shutil.which(cand)
            if p:
                return [p] + cmd[1:]
        raise FileNotFoundError("corepack 不在 PATH")
    return cmd


def run(cmd: list[str], cwd: Path) -> tuple[int, str]:
    proc = subprocess.run(resolve(cmd), cwd=str(cwd), capture_output=True, text=True, encoding="utf-8", errors="replace")
    output=(proc.stdout or "") + (proc.stderr or "")
    logs=ROOT/'artifacts'/'evidence-runs'/'verify-commands';logs.mkdir(parents=True,exist_ok=True)
    log=logs/f'{time.time_ns()}.log';log.write_text(f"$ {' '.join(cmd)}\n{output}\nexitCode={proc.returncode}\n",encoding='utf8')
    return proc.returncode, f"Complete log: {log.relative_to(ROOT)}\n{output}"


def check_resources() -> tuple[str, str]:
    """返回 (status, detail)。不触碰真实档案与凭证。"""
    problems: list[str] = []
    # 1) 文档与运行产物不得出现测试哨兵明文。
    #    源码/测试中的哨兵字符串是断言本身（预期存在），不在此扫描范围。
    sentinels = ["SENTINEL-COOKIE", "SENTINEL-SECRET-VALUE", "PLAINTEXT-COOKIE-SENTINEL"]
    scan_roots = [ROOT / "docs", ROOT / "profiles"]
    hits: list[str] = []
    for d in scan_roots:
        if not d.exists():
            continue
        for f in d.rglob("*"):
            if not f.is_file() or f.suffix in {".png", ".ico"}:
                continue
            if "target" in f.parts or "node_modules" in f.parts:
                continue
            try:
                text = f.read_text(encoding="utf8", errors="ignore")
            except OSError:
                continue
            for s in sentinels:
                if s in text:
                    hits.append(f"{f.relative_to(ROOT)}:{s}")
    if hits:
        problems.append("哨兵明文出现在文档/运行产物：" + ", ".join(hits[:5]))

    # 2) 仓库 profile 配置不得默认放行测试端点。
    profiles = ROOT / "profiles"
    if profiles.exists():
        for f in profiles.rglob("*.json"):
            try:
                data = json.loads(f.read_text(encoding="utf8"))
            except json.JSONDecodeError:
                continue
            if data.get("test_allowed_hosts"):
                problems.append(f"{f.relative_to(ROOT)} 含 test_allowed_hosts（发行/开发档案均不得默认放行）")

    # 3) 依赖许可台账存在。
    ledger = ROOT / "docs" / "delivery" / "dependency-licenses.md"
    if not ledger.exists():
        problems.append("缺少依赖许可台账 docs/delivery/dependency-licenses.md（python scripts/gen_licenses.py 生成）")

    if problems:
        return "fail", "\n".join(problems)
    return "pass", "哨兵/发行端点/许可台账检查通过"


# 进程级测试通过 target/debug 定位 daemon 与适配器二进制；`cargo test` 不保证
# 构建其他包的 bin，干净检出必须先构建工作区（2026-10-01 A 复核 F-001）。
PREBUILD_CMD = ["cargo", "build", "--workspace", "--locked"]
PREBUILD_ARTIFACTS = ["target/debug/trace-daemon.exe", "target/debug/trace-fake-adapter.exe", *[f"target/debug/trace-adapter-{site}.exe" for site in SITES], "target/debug/frame_probe.exe", "target/debug/host_probe.exe", "target/debug/lifecycle_adapter.exe"]
_prebuild_result: tuple[int, str] | None = None


def prebuild() -> tuple[int, str]:
    """每次运行只构建一次；构建后核对进程级测试依赖的二进制确实存在。"""
    global _prebuild_result
    if _prebuild_result is None:
        code, out = run(PREBUILD_CMD, ROOT)
        missing = [a for a in PREBUILD_ARTIFACTS if not (ROOT / a).exists()]
        if code == 0 and missing:
            code, out = 1, out + f"\n构建后仍缺少：{', '.join(missing)}"
        _prebuild_result = (code, f"$ {' '.join(PREBUILD_CMD)}\n{out[-1600:]}")
    return _prebuild_result


def missing_blockers(names: list[str]) -> list[str]:
    missing = []
    for n in names:
        if shutil.which(n) is None and not (ROOT / n).exists():
            missing.append(n)
    return missing


def run_suite(name: str) -> dict:
    spec = SUITES[name]
    missing = missing_blockers(spec["blocked_if_missing"])
    result: dict = {"suite": name, "desc": spec["desc"], "covers": spec["covers"], "commands": []}
    if missing:
        result["status"] = "blocked"
        result["detail"] = f"缺少前置：{', '.join(missing)}（先构建/安装后重跑）"
        return result

    if name == "resources":
        status, detail = check_resources()
        result["status"] = status
        result["detail"] = detail
        return result

    outputs: list[str] = []
    if spec.get("prebuild"):
        code, out = prebuild()
        result["commands"].append(" ".join(PREBUILD_CMD))
        outputs.append(out)
        if code != 0:
            result["status"] = "fail"
            result["detail"] = out
            return result
    for cmd in [spec["cmd"], *spec.get("extra_cmds", [])]:
        if cmd is None:
            continue
        code, out = run(cmd, ROOT)
        result["commands"].append(" ".join(cmd))
        outputs.append(f"$ {' '.join(cmd)}\n{out[-1600:]}")
        if code != 0:
            result["status"] = "fail"
            result["detail"] = outputs[-1]
            return result
    result["status"] = "pass"
    result["detail"] = "\n".join(outputs)[-2000:]
    return result


def main() -> int:
    # JSON/CJK output must survive a redirected Windows console (GBK default).
    for stream in (sys.stdout, sys.stderr):
        stream.reconfigure(encoding="utf-8")
    ap = argparse.ArgumentParser(description="TRACE verify suites")
    ap.add_argument("--suite", choices=sorted(SUITES), help="运行单个套件")
    ap.add_argument("--all", action="store_true", help="运行全部套件")
    ap.add_argument("--json", action="store_true", help="输出 JSON 摘要")
    ap.add_argument("--stage-bundle", type=Path, help="核验阶段证据报告（stage.py report 的产物）；缺项/过期/失败均拒绝")
    args = ap.parse_args()
    if args.stage_bundle:
        errors=evidence.validate(evidence.read(args.stage_bundle))
        for error in errors:print('[FAIL]',error)
        if errors:return 1
        if not args.suite and not args.all:return 0
    if not args.suite and not args.all:
        ap.print_help()
        return 2

    names = sorted(SUITES) if args.all else [args.suite]
    results = [run_suite(n) for n in names]

    if args.json:
        print(json.dumps(results, ensure_ascii=False, indent=2))
    else:
        for r in results:
            print(f"[{r['status'].upper()}] {r['suite']}: {r['desc']}")
            if r["status"] != "pass":
                print(f"    {r.get('detail','')[:1200]}")

    failed = [r for r in results if r["status"] != "pass"]
    if failed:
        print(f"\n{len(failed)} 个套件未通过（blocked 不计通过）")
        return 1
    print(f"\n{len(results)} 个套件全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
