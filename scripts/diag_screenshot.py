"""截取正在运行的 DSH Desktop 窗口，生成 README 用的截图。

用 PrintWindow(PW_RENDERFULLCONTENT) 抓取窗口内容 —— WebView2 是 DirectComposition
渲染的，普通 BitBlt 会截到黑块，必须带这个标志。

用法：python diag_screenshot.py [输出路径]
"""

import ctypes
import os
import sys
import time
from ctypes import wintypes

from PIL import Image

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

user32 = ctypes.WinDLL("user32", use_last_error=True)
gdi32 = ctypes.WinDLL("gdi32", use_last_error=True)

TITLE = "DSH Desktop"
PW_RENDERFULLCONTENT = 0x00000002


class BITMAPINFOHEADER(ctypes.Structure):
    _fields_ = [
        ("biSize", wintypes.DWORD),
        ("biWidth", wintypes.LONG),
        ("biHeight", wintypes.LONG),
        ("biPlanes", wintypes.WORD),
        ("biBitCount", wintypes.WORD),
        ("biCompression", wintypes.DWORD),
        ("biSizeImage", wintypes.DWORD),
        ("biXPelsPerMeter", wintypes.LONG),
        ("biYPelsPerMeter", wintypes.LONG),
        ("biClrUsed", wintypes.DWORD),
        ("biClrImportant", wintypes.DWORD),
    ]


class BITMAPINFO(ctypes.Structure):
    _fields_ = [("bmiHeader", BITMAPINFOHEADER), ("bmiColors", wintypes.DWORD * 3)]


def find_main_window():
    found = []

    @ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
    def cb(hwnd, lparam):
        n = user32.GetWindowTextLengthW(hwnd)
        if n:
            buf = ctypes.create_unicode_buffer(n + 1)
            user32.GetWindowTextW(hwnd, buf, n + 1)
            if buf.value == TITLE and user32.IsWindowVisible(hwnd):
                found.append(hwnd)
        return True

    user32.EnumWindows(cb, 0)
    return found[0] if found else None


def capture(hwnd):
    """截取窗口，并裁掉标题栏与边框，只保留客户区（干净的界面图）。"""
    wrect = wintypes.RECT()
    user32.GetWindowRect(hwnd, ctypes.byref(wrect))
    w = wrect.right - wrect.left
    h = wrect.bottom - wrect.top

    hdc = user32.GetWindowDC(hwnd)
    memdc = gdi32.CreateCompatibleDC(hdc)
    bmp = gdi32.CreateCompatibleBitmap(hdc, w, h)
    old = gdi32.SelectObject(memdc, bmp)

    user32.PrintWindow(hwnd, memdc, PW_RENDERFULLCONTENT)

    bmi = BITMAPINFO()
    bmi.bmiHeader.biSize = ctypes.sizeof(BITMAPINFOHEADER)
    bmi.bmiHeader.biWidth = w
    bmi.bmiHeader.biHeight = -h  # 负值 = top-down
    bmi.bmiHeader.biPlanes = 1
    bmi.bmiHeader.biBitCount = 32
    bmi.bmiHeader.biCompression = 0  # BI_RGB

    buf = ctypes.create_string_buffer(w * h * 4)
    gdi32.GetDIBits(memdc, bmp, 0, h, buf, ctypes.byref(bmi), 0)

    img = Image.frombuffer("RGBA", (w, h), buf, "raw", "BGRA", 0, 1).convert("RGB")

    gdi32.SelectObject(memdc, old)
    gdi32.DeleteObject(bmp)
    gdi32.DeleteDC(memdc)
    user32.ReleaseDC(hwnd, hdc)

    # 算出客户区在整窗中的偏移，裁掉标题栏与边框
    crect = wintypes.RECT()
    user32.GetClientRect(hwnd, ctypes.byref(crect))
    origin = wintypes.POINT(0, 0)
    user32.ClientToScreen(hwnd, ctypes.byref(origin))
    off_x = origin.x - wrect.left
    off_y = origin.y - wrect.top

    cw = min(crect.right, w - off_x)
    ch = min(crect.bottom, h - off_y)
    if cw > 200 and ch > 200:
        img = img.crop((off_x, off_y, off_x + cw, off_y + ch))
    return img


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "docs", "screenshot.png")

    hwnd = find_main_window()
    if not hwnd:
        print("未找到 DSH Desktop 窗口（应用没在运行？）")
        return 1

    user32.ShowWindow(hwnd, 9)  # SW_RESTORE
    user32.SetForegroundWindow(hwnd)
    time.sleep(1.5)

    img = capture(hwnd)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    img.save(out)

    # 简单有效性检查：非纯黑像素占比
    small = img.resize((80, 45))
    pixels = list(small.getdata())
    bright = sum(1 for r, g, b in pixels if r + g + b > 90)
    ratio = bright / len(pixels)
    print(f"written: {out} size={img.size} 非黑像素占比={ratio:.2%}")
    if ratio < 0.15:
        print("⚠️ 画面接近全黑，可能是 WebView2 未渲染或窗口被遮挡")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
