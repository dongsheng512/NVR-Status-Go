#!/usr/bin/env python3
"""提交前脱敏检查：不让本机真实档案里的设备名 / 内网 IP 进入仓库。

真值只从本机 NVRStatus 配置读取、只用于比对，**不打印、不写入仓库**。

    scripts/check-secrets.py          检查 git 暂存区（pre-commit 默认）
    scripts/check-secrets.py --all   检查整个工作区文本

启用：git config core.hooksPath .githooks
本机找不到档案时退化为内网地址提醒（只警告，不阻断提交）。
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path

TEXT_EXT = {
    ".go", ".md", ".json", ".txt", ".yml", ".yaml", ".toml", ".cfg",
    ".ini", ".py", ".sh", ".html", ".css", ".js", ".ts", ".plist",
}
SKIP_PARTS = {".git", ".workbuddy", "build", "dist", "__pycache__",
              ".venv", "node_modules"}
IPV4 = re.compile(r"^\d{1,3}(?:\.\d{1,3}){3}$")
RFC1918 = re.compile(r"\b(?:10\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])|192\.168)"
                     r"\.\d{1,3}\.\d{1,3}\b")


def profile_candidates() -> list[Path]:
    out: list[Path] = []
    if os.environ.get("NVRSTATUS_PROFILE"):
        out.append(Path(os.environ["NVRSTATUS_PROFILE"]))
    out.append(Path.home() / "Library" / "Application Support" / "NVRStatus" / "profiles.json")
    if os.environ.get("APPDATA"):
        out.append(Path(os.environ["APPDATA"]) / "NVRStatus" / "profiles.json")
    out.append(Path.home() / ".config" / "NVRStatus" / "profiles.json")
    return out


def load_true_values() -> set[str]:
    """返回档案里的真实 IP 与名称；找不到/解析不了则返回空集合。"""
    for path in profile_candidates():
        if not path.is_file():
            continue
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            continue
        values: set[str] = set()

        def walk(node: object) -> None:
            if isinstance(node, dict):
                for key, value in node.items():
                    if isinstance(value, str):
                        k = key.lower()
                        if k in ("ip", "host", "address") and IPV4.match(value):
                            values.add(value)
                        elif k in ("name", "device_name") and len(value) >= 3:
                            values.add(value)
                    else:
                        walk(value)
            elif isinstance(node, list):
                for item in node:
                    walk(item)

        walk(data)
        return {v for v in values if len(v) >= 3}
    return set()


def is_text_file(path: Path) -> bool:
    return (path.is_file() and path.suffix.lower() in TEXT_EXT
            and not any(part in SKIP_PARTS for part in path.parts))


def staged_files() -> list[Path]:
    try:
        result = subprocess.run(
            ["git", "diff", "--cached", "--name-only", "--diff-filter=ACM"],
            capture_output=True, text=True, check=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        return []
    return [Path(line) for line in result.stdout.splitlines() if line.strip()]


def main() -> int:
    all_mode = "--all" in sys.argv[1:]
    files = ([p for p in Path(".").rglob("*") if is_text_file(p)]
             if all_mode else [p for p in staged_files() if is_text_file(p)])
    files = sorted(set(files))
    values = load_true_values()

    if not values:
        warned = []
        for path in files:
            try:
                lines = path.read_text(encoding="utf-8", errors="ignore").splitlines()
            except OSError:
                continue
            for i, line in enumerate(lines, 1):
                if RFC1918.search(line):
                    warned.append(f"{path}:{i}")
                    break
        print("! 未找到本机 NVRStatus 档案，跳过精确比对。")
        if warned:
            print(f"! 以下 {len(warned)} 个文件含 RFC1918 内网地址样式，请人工确认是否为示例值：")
            for item in warned[:10]:
                print(f"   {item}")
        print("  （此模式只警告不阻断；建议在开发机保留 profiles.json 以启用精确阻断）")
        return 0

    hits: list[str] = []
    for path in files:
        try:
            lines = path.read_text(encoding="utf-8", errors="ignore").splitlines()
        except OSError:
            continue
        for i, line in enumerate(lines, 1):
            if any(v in line for v in values):
                hits.append(f"{path}:{i}")
                break  # 每个文件只报第一处，避免刷屏

    if hits:
        print("⛔ 提交被阻止：以下文件命中本机真实档案中的设备名 / IP（不打印真值）：")
        for item in hits:
            print(f"   {item}")
        print("   请替换为示例值（如 NVR-A / 192.0.2.x）后重新暂存。")
        return 1

    print(f"✓ 脱敏检查通过：扫描 {len(files)} 个文本文件，0 命中")
    return 0


if __name__ == "__main__":
    sys.exit(main())
