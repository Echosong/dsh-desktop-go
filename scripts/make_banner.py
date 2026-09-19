"""生成项目 banner / GitHub social preview 图（1280x640）。

版面自上而下：标题 → 副标题 → **高亮胶囊（一键安装 / 傻瓜式运行）** → 分隔线
→ 环境说明 → 技术要点 → 页脚。

所有文案都走 `fit_font()` 按可用宽度自动收字号，改文案不会溢出画面。
用法：python scripts/make_banner.py
产出：docs/banner.png
"""

import os
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from make_icon import draw_mark  # noqa: E402

W, H = 1280, 640
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")

DEEP = (8, 13, 28)
MID = (30, 45, 120)
ACCENT = (77, 107, 254)

X = 380                      # 文字块左边界（右侧留给鲸鱼标记）
MAX_W = W - 96 - X           # 可用文字宽度 804px

TITLE = "DSH Desktop"
SUBTITLE = "DeepSeek Harness 的 Windows 桌面客户端"

# 高亮胶囊：整个 banner 的钩子
PILL_TEXT = "一键安装 · 傻瓜式运行"

# 胶囊下方两行说明
LINE_ENV = "首次运行自动装好 Node 与 dsh，零前置环境"
LINE_TECH = "Go + Wails v2   ·   无命令行黑窗   ·   托盘常驻   ·   退出自动清理"

FOOTER = "github.com/  ·  gitee.com/"


def pick_font(size, bold=False):
    candidates = [
        r"C:\Windows\Fonts\msyhbd.ttc" if bold else r"C:\Windows\Fonts\msyh.ttc",
        r"C:\Windows\Fonts\msyh.ttc",
        r"C:\Windows\Fonts\simhei.ttf",
        r"C:\Windows\Fonts\arialbd.ttf" if bold else r"C:\Windows\Fonts\arial.ttf",
    ]
    for path in candidates:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size)
            except Exception:
                continue
    return ImageFont.load_default()


_scratch = ImageDraw.Draw(Image.new("RGB", (8, 8)))


def measure(font, text):
    """返回文本实际渲染宽度。"""
    box = _scratch.textbbox((0, 0), text, font=font)
    return box[2] - box[0]


def fit_font(text, size, max_width, bold=False, min_size=12):
    """按最大宽度自动收字号；返回 (字体, 实际字号, 实际宽度)。"""
    for s in range(size, min_size - 1, -1):
        font = pick_font(s, bold=bold)
        width = measure(font, text)
        if width <= max_width:
            return font, s, width
    font = pick_font(min_size, bold=bold)
    return font, min_size, measure(font, text)


def build_background():
    img = Image.new("RGB", (W, H), DEEP)
    d = ImageDraw.Draw(img)

    # 垂直渐变
    for y in range(H):
        t = y / (H - 1)
        # 中间偏亮，两端偏深，形成一个柔和的弧
        k = 1 - abs(t - 0.42) * 1.6
        k = max(0.0, min(1.0, k))
        color = tuple(int(DEEP[i] + (MID[i] - DEEP[i]) * k) for i in range(3))
        d.line([(0, y), (W, y)], fill=color)

    # 右上角的品牌色光晕
    glow = Image.new("RGB", (W, H), (0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.ellipse([W - 620, -330, W + 200, 340], fill=ACCENT)
    glow = glow.filter(ImageFilter.GaussianBlur(160))

    return ImageChops.screen(img, glow)


def draw_pill(d, x, y, text, font, pad_x=26, pad_y=13, radius=None):
    """画高亮胶囊，返回 (宽度, 高度)。"""
    tw = measure(font, text)
    box_h = font.size + pad_y * 2
    box_w = tw + pad_x * 2
    if radius is None:
        radius = box_h // 2

    d.rounded_rectangle([x, y, x + box_w, y + box_h], radius=radius, fill=ACCENT)

    asc, desc = font.getmetrics()
    ty = y + (box_h - (asc + desc)) // 2
    d.text((x + pad_x, ty), text, font=font, fill=(255, 255, 255))
    return box_w, box_h


def main():
    os.makedirs(DOCS, exist_ok=True)
    img = build_background()

    # 左侧鲸鱼标记
    mark = draw_mark(230, (255, 255, 255, 255))
    img.paste(mark, (96, (H - 230) // 2), mark)

    d = ImageDraw.Draw(img)
    report = []

    f_title, s, w = fit_font(TITLE, 68, MAX_W, bold=True)
    report.append(("标题", TITLE, s, w))

    f_sub, s, w = fit_font(SUBTITLE, 28, MAX_W)
    report.append(("副标题", SUBTITLE, s, w))

    f_pill, s, w = fit_font(PILL_TEXT, 27, MAX_W - 60, bold=True)
    report.append(("胶囊", PILL_TEXT, s, w))

    f_env, s, w = fit_font(LINE_ENV, 23, MAX_W)
    report.append(("环境说明", LINE_ENV, s, w))

    f_tech, s, w = fit_font(LINE_TECH, 20, MAX_W)
    report.append(("技术要点", LINE_TECH, s, w))

    f_foot, s, w = fit_font(FOOTER, 19, MAX_W)
    report.append(("页脚", FOOTER, s, w))

    # 垂直排布（整块在画布中居中）
    y_title = 124
    y_sub = y_title + 92
    y_pill = y_sub + 58
    pill_w, pill_h = draw_pill(d, X, y_pill, PILL_TEXT, f_pill)
    y_line = y_pill + pill_h + 28
    y_env = y_line + 22
    y_tech = y_env + 38
    y_foot = y_tech + 58

    d.text((X, y_title), TITLE, font=f_title, fill=(255, 255, 255))
    d.text((X + 2, y_sub), SUBTITLE, font=f_sub, fill=(186, 202, 255))
    d.line([(X + 2, y_line), (W - 96, y_line)], fill=(90, 120, 220), width=1)
    d.text((X + 2, y_env), LINE_ENV, font=f_env, fill=(214, 226, 255))
    d.text((X + 2, y_tech), LINE_TECH, font=f_tech, fill=(150, 172, 230))
    d.text((X + 2, y_foot), FOOTER, font=f_foot, fill=(96, 116, 175))

    out = os.path.join(DOCS, "banner.png")
    img.save(out)

    print("written:", out, img.size)
    print(f"文字块可用宽度 {MAX_W}px，胶囊宽 {pill_w}px，内容底部 y={y_foot + 26}")
    for name, _text, size, width in report:
        flag = "OK " if width <= MAX_W else "溢出"
        print(f"  {flag} {name:6s} {size:2d}px  宽 {width:4d}px")


if __name__ == "__main__":
    main()
