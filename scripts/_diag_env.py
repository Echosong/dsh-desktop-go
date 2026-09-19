"""诊断脚本共用的环境探测。

原先各脚本里写死了本机路径（例如 D:\\soft\\nodejs\\dsh.cmd），既不可移植也会
把本地目录结构带到公开仓库里，这里统一改成自动探测 + 环境变量覆盖：

  DSH_BIN   指向 dsh 命令（dsh.cmd / dsh / bin.js）
  DSH_EXE   指向编译产物 exe
  DSH_EDGE  指向 Chromium 内核浏览器可执行文件
"""

import os
import shutil

HERE = os.path.dirname(os.path.abspath(__file__))
PROJECT_ROOT = os.path.dirname(HERE)


def _from_env(name):
    value = os.environ.get(name, "").strip()
    if value and os.path.exists(value):
        return value
    return None


def find_dsh():
    """定位 dsh 命令（优先环境变量，其次 PATH）。"""
    env = _from_env("DSH_BIN")
    if env:
        return env
    for name in ("dsh.cmd", "dsh.exe", "dsh"):
        found = shutil.which(name)
        if found:
            return found
    return None


def find_exe():
    """定位编译产物：build/bin 下的第一个 exe。"""
    env = _from_env("DSH_EXE")
    if env:
        return env
    bindir = os.path.join(PROJECT_ROOT, "build", "bin")
    if os.path.isdir(bindir):
        for name in sorted(os.listdir(bindir)):
            if name.lower().endswith(".exe"):
                return os.path.join(bindir, name)
    return None


def find_browser():
    """定位 Chromium 内核浏览器（用于 headless 对照实验）。"""
    env = _from_env("DSH_EDGE")
    if env:
        return env
    candidates = [
        r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
        r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
        r"C:\Program Files\Google\Chrome\Application\chrome.exe",
        r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    ]
    for path in candidates:
        if os.path.exists(path):
            return path
    for name in ("msedge", "chrome", "chromium"):
        found = shutil.which(name)
        if found:
            return found
    return None


def node_dir():
    """dsh 是随 node 一起安装的，必要时把它的目录返回给 PATH 用。"""
    env = os.environ.get("DSH_NODE_DIR", "").strip()
    if env and os.path.isdir(env):
        return env
    dsh = find_dsh()
    if dsh:
        return os.path.dirname(dsh)
    return ""
