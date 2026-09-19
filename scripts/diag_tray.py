"""验证「点关闭按钮 → 收进托盘」的行为。

用 Win32 API 直接找到 DSH Desktop 的主窗口，发送 WM_CLOSE —— 这与用户点击标题栏关闭按钮
是完全相同的路径。同时用 IsWindowVisible 判断窗口是否真的被隐藏、进程是否仍然存活。

用法：
  python diag_tray.py status   查看窗口状态（句柄/标题/是否可见/所属进程）
  python diag_tray.py close    发送 WM_CLOSE（等价于点关闭按钮）
  python diag_tray.py kill     强制结束进程（清理用）
"""

import ctypes
import subprocess
import sys
import time
from ctypes import wintypes

user32 = ctypes.WinDLL("user32", use_last_error=True)
WNDENUMPROC = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
WM_CLOSE = 0x0010

TITLE = "DSH Desktop"


def find_windows(title_part):
    found = []

    def cb(hwnd, lparam):
        n = user32.GetWindowTextLengthW(hwnd)
        if n:
            buf = ctypes.create_unicode_buffer(n + 1)
            user32.GetWindowTextW(hwnd, buf, n + 1)
            if title_part in buf.value:
                pid = wintypes.DWORD()
                user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
                found.append({
                    "hwnd": hwnd,
                    "title": buf.value,
                    "visible": bool(user32.IsWindowVisible(hwnd)),
                    "pid": pid.value,
                })
        return True

    user32.EnumWindows(WNDENUMPROC(cb), 0)
    return found


def listen_state(port):
    try:
        raw = subprocess.run(["netstat", "-ano", "-p", "tcp"],
                             capture_output=True, timeout=20).stdout
    except Exception:
        return "?"
    text = raw.decode("gbk", errors="ignore")
    return "是" if any(f":{port}" in ln and "LISTENING" in ln.upper()
                       for ln in text.splitlines()) else "否"


def report(tag):
    wins = find_windows(TITLE)
    print(f"--- {tag} ---")
    if not wins:
        print("  未找到 DSH Desktop 窗口")
    for w in wins:
        print(f"  窗口 hwnd=0x{w['hwnd']:X} 标题={w['title']!r} 可见={w['visible']} pid={w['pid']}")
    print(f"  dsh 是否仍在监听 3388: {listen_state(3388)}")
    return wins


def main():
    action = sys.argv[1] if len(sys.argv) > 1 else "status"

    if action == "status":
        report("当前状态")
        return 0

    if action == "close":
        wins = find_windows(TITLE)
        if not wins:
            print("未找到窗口，无法发送关闭请求")
            return 1
        hwnd = wins[0]["hwnd"]
        print(f"向 hwnd=0x{hwnd:X} 发送 WM_CLOSE（等价于点关闭按钮）")
        user32.PostMessageW(hwnd, WM_CLOSE, 0, 0)
        time.sleep(5)
        report("关闭请求之后")
        return 0

    if action == "kill":
        wins = find_windows(TITLE)
        pids = {w["pid"] for w in wins}
        if not pids:
            raw = subprocess.run(["tasklist", "/FI", "IMAGENAME eq DSH Desktop.exe", "/FO", "CSV"],
                                 capture_output=True).stdout.decode("gbk", errors="ignore")
            for line in raw.splitlines()[1:]:
                parts = [p.strip('"') for p in line.split('","')]
                if len(parts) > 1 and parts[1].isdigit():
                    pids.add(int(parts[1]))
        for pid in pids:
            print("强制结束 pid", pid)
            subprocess.run(["taskkill", "/T", "/F", "/PID", str(pid)], capture_output=True)
        time.sleep(1)
        return 0

    print("未知动作:", action)
    return 1


if __name__ == "__main__":
    sys.exit(main())
