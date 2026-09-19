"""生成项目 banner / GitHub social preview 图（1280x640）。

用法：python scripts/make_banner.py
产出：docs/banner.png
"""

import os
import sys

from PIL import Image, ImageDraw, ImageFilter, ImageFont

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from make_icon import draw_mark  # noqa: E402

W, H = 1280, 640
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")

DEEP = (8, 13, 28)
MID = (30, 45, 120)
ACCENT = (77, 107, 254)
TITLE = "DSH Desktop"
SUBTITLE = "DeepSeek Harness 的 Windows 桌面客户端"
FEATURES = "Go + Wails v2   ·   无命令行黑窗   ·   托盘常驻   ·   退出自动清理"
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

    from PIL import ImageChops
    img = ImageChops.screen(img, glow)
    return img


def main():
    os.makedirs(DOCS, exist_ok=True)
    img = build_background()

    # 左侧鲸鱼标记
    mark = draw_mark(230, (255, 255, 255, 255))
    img.paste(mark, (96, (H - 230) // 2), mark)

    d = ImageDraw.Draw(img)
    f_title = pick_font(68, bold=True)
    f_sub = pick_font(28)
    f_feat = pick_font(21)
    f_foot = pick_font(19)

    x = 380
    d.text((x, 186), TITLE, font=f_title, fill=(255, 255, 255))
    d.text((x + 2, 276), SUBTITLE, font=f_sub, fill=(186, 202, 255))

    d.line([(x + 2, 336), (W - 96, 336)], fill=(90, 120, 220), width=1)

    d.text((x + 2, 362), FEATURES, font=f_feat, fill=(150, 172, 230))
    d.text((x + 2, 420), FOOTER, font=f_foot, fill=(96, 116, 175))

    out = os.path.join(DOCS, "banner.png")
    img.save(out)
    print("written:", out, img.size)


if __name__ == "__main__":
    main()
