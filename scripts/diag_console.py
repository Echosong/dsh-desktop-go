"""检查是否有可见的控制台（cmd）窗口挂在那里。

枚举所有顶层窗口，筛出「可见 + 类名是控制台窗口」的，用于验证：
  - 启动 dsh 时不会新开 cmd 黑窗
  - 内部调用 netstat / taskkill 时不会闪黑窗

用法：
  python diag_console.py           列出当前可见的控制台窗口
  python diag_console.py snapshot  只输出计数（便于前后对比）
"""

import ctypes
import subprocess
import sys
from ctypes import wintypes

user32 = ctypes.WinDLL("user32", use_last_error=True)
WNDENUMPROC = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)

CONSOLE_CLASSES = {"ConsoleWindowClass", "CASCADIA_HOSTING_WINDOW_CLASS"}


def enum_windows():
    out = []

    def cb(hwnd, lparam):
        n = user32.GetWindowTextLengthW(hwnd)
        title = ctypes.create_unicode_buffer(n + 1)
        user32.GetWindowTextW(hwnd, title, n + 1)
        cls = ctypes.create_unicode_buffer(256)
        user32.GetClassNameW(hwnd, cls, 256)
        pid = wintypes.DWORD()
        user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
        out.append({
            "hwnd": hwnd,
            "title": title.value,
            "class": cls.value,
            "pid": pid.value,
            "visible": bool(user32.IsWindowVisible(hwnd)),
        })
        return True

    user32.EnumWindows(WNDENUMPROC(cb), 0)
    return out


def pid_name(pid):
    try:
        raw = subprocess.run(["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                             capture_output=True, timeout=10).stdout
        text = raw.decode("gbk", errors="ignore").strip()
        if text and "INFO" not in text.upper():
            return text.split('","')[0].strip('"')
    except Exception:
        pass
    return "?"


def console_windows():
    return [w for w in enum_windows()
            if w["visible"] and w["class"] in CONSOLE_CLASSES]


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "list"
    wins = console_windows()
    if mode == "snapshot":
        pids = sorted({w["pid"] for w in wins})
        print(f"可见控制台窗口数={len(wins)} pids={pids}")
        return 0

    print(f"可见的控制台窗口：{len(wins)} 个")
    for w in wins:
        print(f"  hwnd=0x{w['hwnd']:X} pid={w['pid']} ({pid_name(w['pid'])}) "
              f"class={w['class']} title={w['title']!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
