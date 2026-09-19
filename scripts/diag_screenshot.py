"""截取正在运行的 DSH Desktop 窗口，生成 README 用的截图。

用 PrintWindow(PW_RENDERFULLCONTENT) 抓取窗口内容 —— WebView2 是 DirectComposition
渲染的，普通 BitBlt 会截到黑块，必须带这个标志。

**左侧面板会自动打码**：dsh 界面左侧是会话 / 工作区列表，会带出用户的会话标题、
项目名等隐私信息，直接发到公开仓库不合适。打码宽度是自动探测的（找第一处竖向边界），
也可以用 --panel-width 手动指定。

用法：
  python diag_screenshot.py                 截图 + 自动给左侧面板打码
  python diag_screenshot.py --raw           截图但保留原样（仅供自己查看，勿公开）
  python diag_screenshot.py --mask-only     只对已有截图重新打码
  python diag_screenshot.py --panel-width 320   手动指定左侧面板宽度
"""

import ctypes
import os
import sys
import time
from collections import Counter
from ctypes import wintypes

from PIL import Image

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

user32 = ctypes.WinDLL("user32", use_last_error=True)
gdi32 = ctypes.WinDLL("gdi32", use_last_error=True)

TITLE = "DSH Desktop"
PW_RENDERFULLCONTENT = 0x00000002

MOSAIC_BLOCK = 18  # 马赛克块边长（像素），够大才能确保文字不可辨认


def color_dist(a, b):
    return sum(abs(x - y) for x, y in zip(a[:3], b[:3]))


def detect_panel_width(img, max_ratio=0.5):
    """探测左侧面板宽度：从左边缘往右扫，找第一处「大部分行都变色」的竖边界。"""
    w, h = img.size
    base = img.getpixel((1, int(h * 0.5)))
    ys = list(range(int(h * 0.12), int(h * 0.88), 17))
    for x in range(60, int(w * max_ratio)):
        changed = sum(1 for y in ys if color_dist(img.getpixel((x, y)), base) > 24)
        if changed > len(ys) * 0.75:
            return x
    return 0


def mosaic_panel(img, width):
    """把左侧 width 像素宽的竖条打上马赛克。"""
    if width <= 0:
        return img, 0
    w, h = img.size
    width = min(width, w)
    region = img.crop((0, 0, width, h))
    small = region.resize(
        (max(1, width // MOSAIC_BLOCK), max(1, h // MOSAIC_BLOCK)), Image.NEAREST
    )
    img.paste(small.resize((width, h), Image.NEAREST), (0, 0))
    return img, width


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
    args = sys.argv[1:]
    raw = "--raw" in args
    mask_only = "--mask-only" in args
    forced_width = None
    if "--panel-width" in args:
        idx = args.index("--panel-width")
        if idx + 1 < len(args):
            forced_width = int(args[idx + 1])

    positional = [a for a in args if not a.startswith("--")]
    # 去掉 --panel-width 后面的数值参数
    if forced_width is not None and str(forced_width) in positional:
        positional.remove(str(forced_width))
    out = positional[0] if positional else os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "docs", "screenshot.png")

    if mask_only:
        if not os.path.exists(out):
            print("找不到已有截图:", out)
            return 1
        img = Image.open(out).convert("RGB")
    else:
        hwnd = find_main_window()
        if not hwnd:
            print("未找到 DSH Desktop 窗口（应用没在运行？）")
            return 1

        user32.ShowWindow(hwnd, 9)  # SW_RESTORE
        user32.SetForegroundWindow(hwnd)
        time.sleep(1.5)
        img = capture(hwnd)

    # 给左侧面板（会话 / 工作区菜单）打码，避免把个人会话内容发到公开仓库
    if raw:
        print("⚠️ --raw：保留原样输出，含个人信息，请勿直接公开")
        panel_w = 0
    else:
        panel_w = forced_width or detect_panel_width(img)
        img, panel_w = mosaic_panel(img, panel_w)

    os.makedirs(os.path.dirname(out), exist_ok=True)
    img.save(out)

    # 简单有效性检查：非纯黑像素占比
    small = img.resize((80, 45))
    pixels = list(small.getdata())
    bright = sum(1 for r, g, b in pixels if r + g + b > 90)
    ratio = bright / len(pixels)
    print(f"written: {out} size={img.size} 非黑像素占比={ratio:.2%}")
    if panel_w:
        print(f"左侧面板已打码：0~{panel_w}px（块大小 {MOSAIC_BLOCK}px）")
    if ratio < 0.15:
        print("⚠️ 画面接近全黑，可能是 WebView2 未渲染或窗口被遮挡")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
