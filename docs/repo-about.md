# 仓库简介（About / 描述）文案

用于 GitHub 仓库页右上角 About → Description，以及 Gitee 的仓库简介。GitHub 上限 350 字符。

## 推荐（纯中文，104 字符）

```
DeepSeek Harness（dsh）的 Windows 桌面客户端：双击即用，首次运行自动检测并装好 Node 与 dsh，零前置环境。Go + Wails v2，无黑窗、托盘常驻、退出自动清理子进程。
```

## 备选一：纯英文（216 字符，想优先吸引英文受众时用）

```
Windows desktop client for DeepSeek Harness (dsh): double-click to run, with a first-run wizard that installs Node and dsh for you. Go + Wails v2, no console windows, tray-resident, cleans up child processes on exit.
```

## 备选二：极简（83 字符，某些列表页显示宽度有限时用）

```
DeepSeek Harness（dsh）的 Windows 桌面客户端：双击即用，零前置环境（首次运行自动装好 Node 与 dsh）。Go + Wails v2。
```

## 建议：两个平台分开用

| 平台 | 用哪版 | 原因 |
| --- | --- | --- |
| GitHub | 备选一（英文） | 检索以英文为主，且仓库已有 `README.en.md` |
| Gitee | 推荐（中文） | 受众以中文为主 |

## Topics（GitHub 仓库标签，对检索的贡献比描述更大）

```
deepseek
deepseek-harness
dsh
wails
golang
windows
desktop-app
desktop-client
system-tray
webview2
zero-config
```

## 改动说明（相对原描述）

原描述：

> DeepSeek Harness（dsh）的 Windows 桌面客户端：把 dsh web 变成双击即用的桌面应用。Go + Wails v2，无命令行黑窗、系统托盘常驻、退出自动清理子进程。

三处问题：

1. **同义重复** —— 「桌面客户端」和「变成桌面应用」说的是同一件事，一句话里讲了两遍，浪费了最宝贵的开头。
2. **最有力的卖点缺席** —— 全程没提「零前置环境 / 自动装依赖」，而这正是与同类封装项目最大的差别：别人要求你先自己 `npm i -g`，这个不用。
3. **工程细节平铺** —— 无黑窗 / 托盘 / 清理子进程三个并列，读起来像功能清单，不像价值主张；压缩成「无黑窗、托盘常驻、退出自动清理子进程」更紧凑。

另外，**「傻瓜安装」在对外文案里建议表述为「零前置环境」**（或「首次运行自动装好依赖」）：
「傻瓜」在中文技术文案里容易显得廉价，而「零前置环境」信息量更准 —— 它同时表达了「不用你先装东西」和「我替你装」两层意思，且和 README 首屏的「全新机器只需 exe 本体」呼应。
