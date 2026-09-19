# DSH Desktop · Setup 首次运行向导 方案

> 目标：让用户拿到 `DSH Desktop.exe` 后，**不装任何前置环境、不看命令行**就能跑起来。
> 四步流程：① 检测/安装 Node → ② 检测/安装 dsh → ③ 确认端口 → ④ 启动。

---

## 1. 形态：方案 A（已定稿）

**采用 A. 内嵌首启向导。** 向导就是现在这个 splash 页面 —— `frontend/dist/index.html` 扩展成多视图，检测/安装/配置全在 Go 侧完成。

理由：

- 单 exe 分发，零额外构建链，开源仓库不增加维护面；
- 步骤 ④「启动运行」天然属于 app 自身行为，放进外部安装器反而割裂；
- 向导页与 splash 同在 `wails.localhost`，`navigateToUI` 的**同站导航 + 令牌注入**逻辑可以原样复用，不需要为新页面单独解决 Cookie/`SameSite` 问题。

> **方案 B（独立 `Setup.exe` 引导器 / Inno Setup）已废弃，不做。** 相关设计从本方案移除（见下方变更记录）。

---

## 2. 现有代码接入点

现有启动链是 `startup() → go bootstrap(false) → EnsureRunning() → navigateToUI()`。向导要插在 `bootstrap` **之前**：

```
startup()
  ├─ startTray()
  ├─ 读 config.json
  ├─ if !config.SetupCompleted → go runSetupWizard()   ← 新增
  └─ else                      → go bootstrap(false)   ← 原逻辑
```

| 文件 | 动作 | 改动要点 |
|---|---|---|
| `setup.go` | **新增** | `SetupService`：四步状态机、`Probe*` / `Install*` / `SaveConfig` 等绑定给前端的方法 |
| `env_probe.go` | **新增** | node / npm / dsh 的**全量枚举探测**（纯函数，好单测） |
| `download.go` | **新增** | 流式下载（带进度/断点续传）、sha256 校验、zip 解压（`archive/zip` 标准库，不加新依赖） |
| `config.go` | **新增** | `%LOCALAPPDATA%\DSH Desktop\config.json` 原子读写 |
| `dsh.go` | 改造 | `resolveDshLaunch()` 拆出 `Locate()`；候选顺序最前面插入 config 记录的路径 |
| `app.go` | 改造 | `resolvePort()` 改为 `env > config > 3388`；向导改端口后需 `newDshRunner(newPort)` 重建 runner |
| `main.go` | 微改 | 把 `SetupService` 一并 `Bind`（或直接挂在 `App` 上，二选一） |
| `frontend/dist/index.html` | 改造 | 单页多视图：`wizard` / `splash`，复用现有 CSS 变量与 `/wails/ipc.js` |
| `tray.go` | 改造 | 托盘菜单加「环境设置…」→ 重新进入向导 |
| `env_probe_test.go` | **新增** | 探测层的单元测试 + 结果对照（见 §9） |

---

## 3. 四步流程详设

### 公共机制

- **每步都是「检测 → 报告 → 按需安装 → 复检」的幂等循环**，已满足的步骤显示绿勾并允许「跳过」。
- 每步支持：`重新检测`、`手动指定路径`（`runtime.OpenFileDialog`）。
- **Step 2 的检测必须是「能跑起来」而不是「文件存在」**：实际执行一次 `dsh --version`（或 `node bin.js --version`），3 秒超时、看退出码。文件在但 ENOENT / 版本不兼容 / shim 坏掉这类问题只有真跑一次才能暴露。

### Step 1 — Node

**探测顺序**（`env_probe.go`，**枚举而非 `exec.LookPath`**）：

1. 应用自带：`%LOCALAPPDATA%\DSH Desktop\runtime\node\node.exe`
2. `config.json` 记录的 `nodePath`
3. **`PATH` 中出现的每一个 `node.exe`**（关键，见 §6 坑①）
4. 常见安装位置扫描：`C:\Program Files\nodejs`、`%LOCALAPPDATA%\Programs\nodejs`、`%APPDATA%\nvm\current`、`%LOCALAPPDATA%\fnm_multishells\*`、`D:\soft\nodejs`、`D:\nodejs`、`%LOCALAPPDATA%\Volta\bin`
5. 注册表 `HKLM/HKCU\SOFTWARE\Node.js` 的 `InstallPath`（可选增强）

**合格判定：Node ≥ 22.14，硬下限，不满足直接阻断。**

依据（已实测核实）：

- `@deepseek-ai/dsh` 的 `bin.js` 使用了 **`import.meta.main`**（本机 grep 命中 1 处）。
- 该语法是 **Node 22.14** 才加入的。
- 本机 `D:\soft\nodejs\node.exe`（22.22.2）验证：`node --input-type=module -e "console.log(import.meta.main)"` → `true`。
- **危险点**：Node 低于 22.14（尤其 22.0–22.13）时，`dsh` 任何子命令都会**零输出静默退出**——不报错、不打日志，看起来像「装坏了」，极难排查。所以这里必须是硬校验，不能只警告。

**校验方法（向导内部直接用这一条）**：

```bash
node --input-type=module -e "console.log(import.meta.main)"   # 必须输出 true
```

> 注：`package.json` 里**没有 `engines` 字段**（实测），所以没有官方声明可读，只能靠上面这条运行时探针。
> 版本候选里也要主动避开 22.0–22.13 这类「看着是 22.x 但不可用」的版本；推荐直接选当前 22.x LTS 最新。

**不合格/缺失 → 安装**（见 §5）。

### Step 2 — dsh

**探测顺序**（复用并扩展现有 `resolveDshLaunch` 的候选链）：

1. `config.json` 的 `dshBin`
2. `%LOCALAPPDATA%\DSH Desktop\runtime\npm-global\node_modules\@deepseek-ai\dsh\lib\bin.js`（默认托管位置）
3. `DSH_BIN` 环境变量（保持现有逃生口）
4. `PATH` 中的 `dsh` / `dsh.cmd`
5. 各 npm prefix 下的 `bin.js`（扩展现有 `dshBinCandidates()`：补 `npm prefix -g`、pnpm、`D:\soft\nodejs` 这类 portable prefix）

**判定**：跑一次 `--version` 通过即可用；同时记录版本，低于最新版时提示「可升级」但不强制。

**缺失 → 安装**（见 §5）。

### Step 3 — 端口

- 默认 `3388`（与现有 `defaultPort` 一致）。
- 输入框实时校验 + 后端 `ProbePort(port)`：
  - 范围 1–65535，避开 <1024（可能需提权）与已知占用端口
  - 返回「是否被占用 / 占用进程 pid + 进程名」，占用时红字提示
- 「自动选择可用端口」按钮：从 3388 起 `+1` 探测到第一个空闲端口（上限 3400）。
- 优先级最终定为：`DSH_WEB_PORT 环境变量 > config.json > 3388`（现有实现只读环境变量，需扩展）。
- 保存时立即校验一次，占用则不允许进入下一步（或让用户选「结束占用进程」，复用现有 `KillPortOccupant()`）。

### Step 4 — 启动

1. 写 `config.json`（`setupCompleted=true` + 全部探测结果），**原子写**（`.tmp` → `os.Rename`）。
2. 若端口变了 → 用新端口重建 `dshRunner`。
3. 交回原 `bootstrap(false)`：`EnsureRunning()` → `navigateToUI()`（同站导航那套逻辑完全复用，向导页同样在 `wails.localhost`，不受影响）。
4. 首次启动后提示可关闭窗口（收托盘），并把「环境设置」入口放进托盘菜单。

---

## 4. 配置持久化

`%LOCALAPPDATA%\DSH Desktop\config.json`（与既有 `app.log` 同目录，不碰 `~/.dsh`）：

```json
{
  "setupCompleted": true,
  "nodePath": "D:\\soft\\nodejs\\node.exe",
  "nodeVersion": "22.22.2",
  "nodeSource": "existing",
  "npmPrefix": "D:\\soft\\nodejs",
  "dshBin": "D:\\soft\\nodejs\\node_modules\\@deepseek-ai\\dsh\\lib\\bin.js",
  "dshVersion": "0.1.5-rc.1",
  "dshSource": "managed",
  "port": 3388,
  "mirror": "npmmirror",
  "lastCheckAt": "2026-09-19T21:30:00+08:00"
}
```

- `nodeSource` / `dshSource` ∈ `existing | managed`，卸载时只清 `managed` 的东西。
- 写入失败（权限/杀软）要能降级为「本次会话内存生效 + 提示」。

---

## 5. 安装策略（免提权路线）

### Node：portable zip（不推荐 msi / winget）

| 方式 | 提权 | 可控性 | 结论 |
|---|---|---|---|
| **zip 解压到 `%LOCALAPPDATA%\DSH Desktop\runtime\node\`** | 否 | 高，删目录即卸载 | **推荐** |
| `winget install OpenJS.NodeJS.LTS` | 是（UAC） | 低，用户机器上多一份系统级安装 | 仅作兜底提示 |
| 官方 msi `/qn` | 是 | 低 | 不采用 |

流程：
1. 取版本清单：`https://npmmirror.com/mirrors/node/index.json`（回退 `https://nodejs.org/dist/index.json`），选 LTS。
2. 下载 `node-v{V}-win-x64.zip` → `runtime\download\node.zip.part`。
3. 校验：同目录 `SHASUMS256.txt` 取 sha256 比对，不通过则删除重下（**校验失败必须硬失败**）。
4. 解压到 `runtime\node.tmp\`，把内层 `node-v{V}-win-x64\` **拍平**到 `runtime\node\`，成功后 `os.Rename` 原子落位。
5. 复检 `runtime\node\node.exe -v`。

### dsh：npm 装到「按优先级选出的 prefix」

> `<prefix>` 不一定是应用私有目录 —— 先按下方「优先复用」原则决定，再执行本条命令。

```bash
{node}\node.exe {node}\node_modules\npm\bin\npm-cli.js install -g \
  --prefix "<prefix>" \
  --registry=https://registry.npmmirror.com \
  --no-audit --no-fund \
  --allow-scripts=@deepseek-ai/dsh-subprocess-local,koffi,node-pty,@google/genai,protobufjs \
  @deepseek-ai/dsh
```

要点：

- **绝不调裸 `npm`，也不依赖 `PATH` 里的 node**：必须用「配对的那个 node」的 `node.exe + npm-cli.js` 直接调。原因见坑②。
- **必须带 `--allow-scripts=...`（Windows）**：不带的话装完「看着成功」，但**终端与子进程能力缺失，实际用不了**。这是最阴的一个坑。
- **成功判据是最后一行有没有 `added ... packages`**，不是「过程没报错」。安装中会刷出大量 npm warn 与 `cleanup failed`，**属正常现象**，不要据此判失败。
- **`<prefix>` 的选择要遵循「全机只有一份」**（见下方「优先复用」原则）。
- 装完校验 `bin.js` 存在 + 跑一次 `dsh --version` 拿到版本号。
- npm 的 stdout/stderr **逐行推送到前端日志区**（复用 `evtLog`），安装中显示不确定进度条 + 「已用时」，避免用户以为卡死。
- 支持取消：`context` 取消 → 结束 npm 进程树（复用 `killTree`）→ 清理半成品目录。
- 镜像回退：npmmirror 失败自动重试官方 `registry.npmjs.org`。

#### 优先复用：不要制造重复副本

dsh 的安装原则是**全机只有一份**（多份会导致「版本对不上」类诡异问题）。所以向导的 `<prefix>` 决策顺序应该是：

1. **已有可用 dsh** → 直接用，什么都不装（Step 2 显示绿勾 + 路径 + 版本）。
2. **已有 node/npm 但无 dsh** → **优先复用该 node 的 prefix** 安装（与用户其它全局包同处，符合「系统统一位置」），而不是另起应用私有目录。
3. **本次由向导新装的便携 Node** → 才用应用私有 prefix `%LOCALAPPDATA%\DSH Desktop\runtime\npm-global`，因为便携 Node 本身就在应用目录里，自成一体。

同时：若检测到**旧副本残留**（其它软件目录、npx 缓存 `%LOCALAPPDATA%\npm-cache\_npx`），提示用户并让其一键清理，避免 `where dsh` 解析到错的那份。

> nvm 用户的特别注意：全局包按 node 版本目录隔离，**切换/升级 node 版本后 dsh 会「消失」**（`where dsh` 找不到），属正常现象；`D:\...\nodejs` 若是指向 nvm 版本目录的符号链接即属此类。向导检测到这种环境要在 Step 1/2 给出明确解释，别让用户以为装坏了。

---

## 6. 必须踩住的坑（本机实测证据）

**① `PATH` 里可能有多个 node，`exec.LookPath` 只会拿到第一个。**
本机实测：

```
where node →
  C:\Users\Administrator\.workbuddy\binaries\node\versions\22.22.2-3\node.exe   ← LookPath 命中这个
  D:\soft\nodejs\node.exe                                                      ← dsh 实际装在这个下面
```

如果只用 `LookPath("node")`，就会得出「node 在 A、dsh 在 B」的错乱结论。→ **必须枚举所有候选并让用户可手动指定**。

**② 裸 `npm config get prefix` 可能指向别的 node 目录，`npm i -g` 会装错地方。**
本机实测 `npm config get prefix` = `.workbuddy\...\22.22.2-3`（因为它在 PATH 更前面），而 dsh 在 `D:\soft\nodejs`。→ **安装时必须显式 `--prefix`，并用配对的 node 调 npm**。

**③ Windows 上 npm 的 `dsh` 是 sh 脚本，不可直接执行**，必须走 `dsh.cmd` 或 `node bin.js`（现有 `wrapCommand()` 已处理，向导里的「验证」逻辑要复用同一套，别另写一份）。

**④ 长路径 / 260 字符限制**：`runtime\npm-global\node_modules\@deepseek-ai\dsh\...` 已经约 60 字符，安全。但**不要**把 runtime 放到 `%APPDATA%`（更短但易被漫游同步）或深目录。

**⑤ 杀软误拦**：解压出来的 `node.exe` 可能被拦截或隔离，表现为「刚装完就找不到」。安装后必须做一次可执行性复检，失败时明确提示「疑为安全软件拦截」。

**⑥ 向导期间绝不能启动 dsh**，否则端口检测/占用判断会自相矛盾。

**⑦ 下载中断**：`Content-Length` + `Range` 断点续传写 `.part`，空闲 60s 超时、整体 10 分钟超时、失败重试 3 次（换镜像）。

**⑧ 不要把安装目录写进系统 `PATH`**：全程自包含（`DSH_BIN` / `config.json` 传递路径），退出时不留全局副作用。

**⑨ Node < 22.14 = 静默失败。** dsh 的 `bin.js` 用了 `import.meta.main`（22.14 才有），低于该版本时 `dsh` 任何命令**零输出直接退出**，不报错不打日志。用户看到的现象是「点了没反应」，会被误判成安装坏掉。→ Step 1 硬校验 + 用运行时探针 `node --input-type=module -e "console.log(import.meta.main)"` 判定。

**⑩ 装 dsh 不带 `--allow-scripts` = 装好了但不能用。** npm 默认不跑依赖的 postinstall 脚本，会让 `node-pty` / `koffi` 这类原生模块缺失，表现为**终端与子进程能力不可用**，而 npm 输出一切正常。→ 必须按 §5 的完整参数安装。

---

## 7. 事件协议与前端

复用现有 `EventsEmit` 风格，新增：

| 事件 | 载荷 | 用途 |
|---|---|---|
| `setup:step` | `{step, total, state, title, message, detail}` | 步骤状态与文案（`state` ∈ `checking/ok/missing/installing/done/error`） |
| `setup:progress` | `{step, phase, percent, received, total, speedBps, note}` | 下载/安装进度 |
| `setup:log` | `string` | 明细日志（也可直接复用 `evtLog`） |

前端方法（绑定）：`ProbeAll()` / `InstallNode()` / `InstallDsh()` / `ProbePort(port)` / `PickPort()` / `PickNodeFile()` / `SaveAndLaunch(cfg)` / `ResetWizard()`。

UI（沿用现有浅色 + `--brand:#4D6BFE` 视觉）：

- 左侧或顶部 4 步 stepper，每步三态图标（转圈 / 绿勾 / 黄叹号）。
- 底部按钮：`上一步` / `下一步` / `跳过` / `自动安装` / `手动指定`。
- 下载进度条带速率与剩余时间；失败态给「重试」+「复制手动命令」按钮（兜底：`npm i -g @deepseek-ai/dsh`）。
- 进入 Step 4 后不再回退，直接交给原 splash 的启动进度条。

---

## 8. 分期实施

| 期 | 内容 | 产出 |
|---|---|---|
| **P0** | 纯检测向导：四步探测 + 手动指定路径 + 端口配置 + 启动；不做自动下载 | 最小可用，1 次提交可验证 |
| **P1** | Node portable 自动下载安装（进度 / sha256 / 断点续传 / 原子落位） | 普通用户能自助补齐 Node |
| **P2** | dsh 自动安装（prefix 复用决策 + 镜像回退 + `--allow-scripts` + 取消清理）+ 托盘「环境设置」+ 卸载/重装 | 完整闭环 |

**按 P0 → P1 → P2 顺序推进。** P0 就能立刻验证「检测准不准」，风险最低，也是整个向导里唯一无法靠读代码保证正确性的部分。

> P3（独立 `Setup.exe` 引导器）随方案 B 一并废弃。

---

## 9. 验收方式

**单元测试（`env_probe_test.go`）** —— 直接测真实代码，而不是用脚本另写一份对照实现：

```bash
go test .                                  # 全部断言：版本判定 / 端口 / zip-slip / 命令拼装 …
go test -run TestDiagSetup -v .            # 打印四步探测结果，人工与 where node / where dsh / netstat 对照
```

覆盖的判定逻辑：Node 硬下限（重点覆盖 22.0~22.13 这段「看着是 22.x 但不可用」的版本）、版本比较、
`SHASUMS256` 摘要提取、解压越界防护、进度百分比、dsh 入口类型判定、Windows `.cmd` 必须经 `cmd.exe`、
端口区间与占用检出（**用独立进程占住端口**，验证能解析出占用者 PID 与进程名）、兜底命令必须带
`--prefix` 与 `--allow-scripts`。

**手工冒烟（改完必做）**：

1. 直接启动 exe（不删 `config.json`）→ 应**不进向导**，直接走启动页到 Web UI；
2. 删掉 `config.json` 或点托盘「环境设置…」→ 进向导，左侧四步应全部变绿勾（本机已有可用环境时）；
3. 制造降级场景：临时改名 `node.exe`（模拟缺失）、用别的进程占住 3388（模拟冲突）、断网（模拟下载失败），
   确认每种情况都有明确提示且**不会卡死**。

> 实测记录（2026-09-19，本机）：向导路径下日志只出现 `startup` 与 `tray` 两行，**没有** `启动命令` 行，
> 确认向导不会提前启动 dsh；同时不会写出 `config.json`。
> 截图特征校验：左侧 4 个步骤条目（间距均匀）、右侧面板 7 行内容（标题 + 说明 + 2 张候选卡片各 2 行 + 操作链接）、
> 恰好 1 张卡片为选中态（淡蓝底 ~56k 像素）。

---

## 10. 决策点（已定稿）

| # | 决策点 | 结论 |
|---|---|---|
| ★1 | 向导形态 | **A. 内嵌首启向导**（B. 独立 `Setup.exe` 已废弃） |
| ★2 | Node 安装方式 | **portable zip** 解到 `%LOCALAPPDATA%\DSH Desktop\runtime\node\`，免 UAC；winget/msi 仅作文案兜底，不实现 |
| ★3 | 缺失时行为 | **自动下载安装**，同时保留「手动指定路径」与「复制手动命令」两个出口 |
| ★4 | Node 版本阈值 | **硬下限 22.14**，低于即阻断；探针 `import.meta.main` 必须为 `true`（已核实，无选择余地） |
| ★5 | 实施范围 | **P0 → P1 → P2**，按序推进 |
| ★6 | dsh 托管 prefix | **优先复用已有 prefix**（全机只留一份）；仅在向导新装便携 Node 时才用应用私有 prefix |

---

## 11. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-09-19 | 首版方案（含 A/B 形态对比） |
| 2026-09-19 | 定稿方案 A，废弃方案 B（独立 `Setup.exe`）及其 P3 分期；Node 阈值由「20 起警告」纠正为**硬下限 22.14**；补 `--allow-scripts`、`added ... packages` 判据、prefix 复用原则、nvm 说明 |

> 实现级细节（Go 数据结构 / 接口签名 / 状态机 / 事件 schema / 前端骨架 / UI 线框）见 [`setup-wizard-design.md`](./setup-wizard-design.md)。
