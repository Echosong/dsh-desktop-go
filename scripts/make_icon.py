"""生成 DSH Desktop 应用图标：DeepSeek 品牌蓝圆角底 + 白色鲸鱼。

产出：
  build/appicon.png       1024x1024（Wails 会自动据此生成 Windows 图标）
  build/windows/icon.ico  多尺寸 Windows 图标
"""

import math
import os

from PIL import Image, ImageDraw

# DeepSeek 品牌蓝
BLUE = (77, 107, 254, 255)
WHITE = (255, 255, 255, 255)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUILD = os.path.join(ROOT, "build")


def rounded_rect(draw, box, radius, fill):
    draw.rounded_rectangle(box, radius=radius, fill=fill)


def ellipse_points(cx, cy, rx, ry, angle_deg, steps=360):
    """返回旋转椭圆的采样点（归一化 0..1 坐标）。"""
    ang = math.radians(angle_deg)
    out = []
    for i in range(steps):
        t = 2 * math.pi * i / steps
        x = rx * math.cos(t)
        y = ry * math.sin(t)
        out.append((cx + x * math.cos(ang) - y * math.sin(ang),
                    cy + x * math.sin(ang) + y * math.cos(ang)))
    return out


def wake_shapes():
    """鲸鱼的几何定义（归一化 0..1 坐标），图标与 banner 共用。"""
    return {
        "tail": [
            [(0.665, 0.545), (0.930, 0.318), (0.842, 0.586)],
            [(0.665, 0.628), (0.930, 0.855), (0.842, 0.586)],
        ],
        "fin": [(0.400, 0.470), (0.474, 0.282), (0.578, 0.470)],
        "body": ellipse_points(0.440, 0.585, 0.300, 0.168, -6),
        "eye": (0.252, 0.560, 0.030),
    }


def draw_logo(size):
    """绘制单位坐标下的 logo，size 为输出边长。"""
    ss = 4  # 超采样倍数，保证边缘平滑
    w = size * ss
    img = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def P(pts):
        return [(x * w, y * w) for x, y in pts]

    # 圆角方形底
    rounded_rect(d, (0, 0, w - 1, w - 1), radius=int(w * 0.224), fill=BLUE)

    shapes = wake_shapes()

    # 尾巴（两片鳍，从身体内部伸出、末端张开成 V 形）
    for tri in shapes["tail"]:
        d.polygon(P(tri), fill=WHITE)

    # 背鳍
    d.polygon(P(shapes["fin"]), fill=WHITE)

    # 身体（旋转椭圆，头部朝左）
    d.polygon(P(shapes["body"]), fill=WHITE)

    # 眼睛（用底色镂空；16px 小图标下自动合并，不影响大图识别）
    ex, ey, er = shapes["eye"]
    d.ellipse(P([(ex - er, ey - er), (ex + er, ey + er)]), fill=BLUE)

    out = img.resize((size, size), Image.LANCZOS)
    return out


def draw_mark(size, color=WHITE):
    """只画鲸鱼本身（透明底），用于叠在深色背景上（banner 等）。"""
    ss = 4
    w = size * ss
    img = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def P(pts):
        return [(x * w, y * w) for x, y in pts]

    shapes = wake_shapes()
    for tri in shapes["tail"]:
        d.polygon(P(tri), fill=color)
    d.polygon(P(shapes["fin"]), fill=color)
    d.polygon(P(shapes["body"]), fill=color)
    return img.resize((size, size), Image.LANCZOS)


def main():
    os.makedirs(os.path.join(BUILD, "windows"), exist_ok=True)

    appicon = draw_logo(1024)
    appicon_path = os.path.join(BUILD, "appicon.png")
    appicon.save(appicon_path)
    print("written:", appicon_path)

    ico_path = os.path.join(BUILD, "windows", "icon.ico")
    appicon.save(ico_path, format="ICO",
                 sizes=[(256, 256), (128, 128), (64, 64), (48, 48), (32, 32), (16, 16)])
    print("written:", ico_path)

    # 顺手给前端 splash 一份 256 的预览图
    frontend = os.path.join(ROOT, "frontend", "dist")
    os.makedirs(frontend, exist_ok=True)
    preview = os.path.join(frontend, "logo.png")
    draw_logo(256).save(preview)
    print("written:", preview)


if __name__ == "__main__":
    main()
