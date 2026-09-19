# DSH Desktop · Setup 向导 实现级设计

> 配套文档：[`setup-wizard-plan.md`](./setup-wizard-plan.md)（方案 A：内嵌首启向导）。
> 本文给出**可以直接照着写代码**的粒度：数据结构、接口签名、状态机、事件 schema、前端骨架、UI 线框、文案表、测试清单。
>
> 约定：项目是单包 `package main`；所有代码**先 import 再写简单类名，禁止内联全路径**。

---

## 1. 文件清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `config.go` | 新增 | `Config` 结构 + 目录函数 + 原子读写 |
| `env_probe.go` | 新增 | node / npm / dsh / 端口 的探测（纯读，可单测） |
| `download.go` | 新增 | 下载（进度/续传）、sha256、zip 解压 |
| `setup.go` | 新增 | 向导状态机 + 绑定给前端的 `Setup*` 方法 |
| `setup_types.go` | 新增 | 向导相关类型与错误码（也可并入 setup.go） |
| `dsh.go` | 改造 | 拆出 `Locate()`；支持「按 config 指定的 node+dshBin 直接启动」 |
| `app.go` | 改造 | `resolvePort` 分级；`startup` 分流；新增 `stateSetup` |
| `main.go` | 改造 | `OnBeforeClose` 在向导期间走确认退出 |
| `tray.go` | 改造 | 加「环境设置…」 |
| `frontend/dist/index.html` | 改造 | 多视图（wizard / splash） |
| `env_probe_test.go` | 新增 | 探测层单元测试 + `TestDiagSetup` 结果对照 |

---

## 2. 常量

```go
// 向导步骤编号
const (
	stepNode = iota + 1 // 1
	stepDsh             // 2
	stepPort            // 3
	stepRun             // 4
)

const stepTotal = 4

// 单步状态
const (
	stPending    = "pending"    // 未开始
	stChecking   = "checking"   // 检测中
	stOK         = "ok"         // 已满足，可跳过
	stMissing    = "missing"    // 缺失，可安装
	stInstalling = "installing" // 安装中
	stError      = "error"      // 失败
	stDone       = "done"       // 已完成
)

// app 状态：新增向导态（现有 stateStarting/stateReady/stateError 保留）
const stateSetup = "setup"

// 安装来源，决定卸载时该不该清
const (
	srcExisting = "existing" // 用户机器上已有的，绝不动
	srcManaged  = "managed"  // 本应用装的，可卸载
	srcPicked   = "picked"   // 用户手动指定的
)

// node 硬下限：dsh 的 bin.js 用 import.meta.main（22.14+）
const minNodeMajor = 22
const minNodeMinor = 14

const defaultPort = 3388 // 已存在，复用
const portScanMax = 3400 // 自动选端口的扫描上限

// 事件名
const (
	evtSetupStep     = "setup:step"
	evtSetupProgress = "setup:progress"
)
```

---

## 3. 数据结构（`setup_types.go`）

```go
// NodeInfo 是一个候选 Node 运行时。
type NodeInfo struct {
	Path    string `json:"path"`    // node.exe 绝对路径
	Version string `json:"version"` // 22.22.2
	OK      bool   `json:"ok"`      // 是否满足 dsh 的硬要求
	Source  string `json:"source"`  // runtime | config | path | scan | picked
	Clean   bool   `json:"clean"`   // true=合格，false=需要看 Reason
	Reason  string `json:"reason"`  // 不合格原因，直接可展示
	Prefix  string `json:"prefix"`  // 该 node 对应的 npm 全局 prefix（探测得到）
	NpmCLI  string `json:"npmCli"`  // npm-cli.js 绝对路径
}

// DshInfo 是一个候选 dsh 安装。
type DshInfo struct {
	Bin     string `json:"bin"`     // bin.js 绝对路径
	Node    string `json:"node"`    // 与之配对的 node.exe
	Label   string `json:"label"`   // 展示用，如 "node bin.js"
	Version string `json:"version"` // 0.1.5-rc.1
	Source  string `json:"source"`  // config | managed | env | path | prefix | picked
	Prefix  string `json:"prefix"`  // 所属 npm prefix
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"`
}

// PortInfo 是端口探测结果。
type PortInfo struct {
	Port    int    `json:"port"`
	Free    bool   `json:"free"`
	Invalid bool   `json:"invalid"` // 端口号本身非法
	PID     int    `json:"pid"`
	Process string `json:"process"` // 占用者进程名，如 node.exe
	Message string `json:"message"` // 直接可展示
}

// StepState 是单步快照。
type StepState struct {
	Step    int    `json:"step"`
	Total   int    `json:"total"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	CanSkip bool   `json:"canSkip"` // 当前是否允许「跳过」
}

// Progress 是下载 / 安装进度。
type Progress struct {
	Step     int     `json:"step"`
	Phase    string  `json:"phase"` // download | verify | extract | npm | done
	Percent  float64 `json:"percent"` // 0~100；-1 表示总量未知（npm 阶段）
	Received int64   `json:"received"`
	Total    int64   `json:"total"`
	Speed    int64   `json:"speed"` // bytes/s
	Note     string  `json:"note"`
}

// Snapshot 是向导的整体快照，前端一次拉取即可渲染全部。
type Snapshot struct {
	Required    bool        `json:"required"` // true=应展示向导而非启动页
	Steps       []StepState `json:"steps"`
	Node        *NodeInfo   `json:"node"`        // 当前选定
	NodeChoices []NodeInfo  `json:"nodeChoices"` // 全部候选（含不合格的，供用户选）
	Dsh         *DshInfo    `json:"dsh"`
	DshChoices  []DshInfo   `json:"dshChoices"`
	Port        PortInfo    `json:"port"`
	PortFixed   bool        `json:"portFixed"` // 环境变量锁定了端口，UI 禁用输入
	Busy        bool        `json:"busy"`      // 有安装任务在跑
	Version     string      `json:"version"`   // 应用版本，展示用
}

// SetupError 是结构化错误，前端不需要解析字符串。
type SetupError struct {
	Code    string `json:"code"`    // 见 §9 错误码表
	Message string `json:"message"` // 一句话给用户看
	Hint    string `json:"hint"`    // 怎么办
	Cmd     string `json:"cmd"`     // 可复制的兜底手动命令（可为空）
}

func (e *SetupError) Error() string { return e.Message }

// OpResult 是向导所有操作的统一返回，避免前端解析 error 字符串。
type OpResult struct {
	OK       bool        `json:"ok"`
	Err      *SetupError `json:"err"`
	Snapshot Snapshot    `json:"snapshot"`
}
```

---

## 4. 配置持久化（`config.go`）

```go
// Config 是向导写入的持久化配置。
// 注意：字段不要带初始化默认值（否则局部更新会误写回库级默认值），默认值一律在 load 后显式兜底。
type Config struct {
	SetupCompleted bool   `json:"setupCompleted"`
	NodePath       string `json:"nodePath"`
	NodeVersion    string `json:"nodeVersion"`
	NodeSource     string `json:"nodeSource"`
	NpmPrefix      string `json:"npmPrefix"`
	DshBin         string `json:"dshBin"`
	DshVersion     string `json:"dshVersion"`
	DshSource      string `json:"dshSource"`
	Port           int    `json:"port"`
	Mirror         string `json:"mirror"`
	LastCheckAt    string `json:"lastCheckAt"`
}

// dataDir 返回 %LOCALAPPDATA%\DSH Desktop（与既有 app.log 同目录）。
func dataDir() string

// runtimeDir 返回 dataDir()\runtime，应用自管的东西都放这里。
func runtimeDir() string

// runtimeNodeDir 返回 runtimeDir()\node，便携 Node 的落位点。
func runtimeNodeDir() string

// runtimeNpmGlobal 返回 runtimeDir()\npm-global，应用私有 npm prefix。
func runtimeNpmGlobal() string

// configPath 返回数据目录下的 config.json 路径。
func configPath() string

// LoadConfig 读取配置；文件不存在或损坏时返回零值，不报错。
func LoadConfig() Config

// Save 原子写入配置（先写 .tmp 再 Rename）。
func (c Config) Save() error

// ResolvedPort 返回最终端口：环境变量 > 配置 > 默认。
// 同时返回端口是否被环境变量锁定（锁定则 UI 禁用编辑）。
func (c Config) ResolvedPort() (port int, locked bool)
```

要点：

- **原子写**：`config.json.tmp` → `os.Rename`，避免写一半断电留下坏文件。
- 写失败**不阻断流程**：降级为「本次会话内存生效」，并记一条日志 + 前端提示。
- 不碰 `~/.dsh`（那是 dsh 自己的家目录）。

---

## 5. 探测层（`env_probe.go`）

全部是**只读纯函数**，不产生副作用，方便 `diag_setup.py` 对照和后续单测。

```go
// probeNodes 枚举所有候选 Node，按可信度排序：
//   runtime 自管 > config 记录 > PATH 中的每一个 > 常见安装目录扫描
// 关键：绝不只用 exec.LookPath，PATH 里可能有多个 node（见 plan §6 坑①）。
func probeNodes() []NodeInfo

// probeOneNode 校验单个 node.exe：
//   1) 跑 node -v 拿到版本，解析不出即视为损坏
//   2) 版本 >= 22.14
//   3) 运行时探针 node --input-type=module -e "console.log(import.meta.main)" 必须输出 true
func probeOneNode(exe, source string) (NodeInfo, bool)

// npmPrefixOf 反查该 node 对应的 npm 全局 prefix。
// 依次尝试：node 同目录 > npm_config_prefix > 与 node 同级的 node_modules\npm > 该 node 目录本身
func npmPrefixOf(node NodeInfo) string

// probeDshs 枚举候选 dsh：config > runtime\npm-global > DSH_BIN > PATH > 各 prefix 下的 bin.js
func probeDshs(node NodeInfo, cfg Config) []DshInfo

// probeOneDsh 用「能跑起来」判定而非「文件存在」：
//   执行 node bin.js --version，3s 超时，解析出版本号即 OK
func probeOneDsh(node NodeInfo, bin, source, prefix string) (DshInfo, bool)

// probePort 探测端口占用，并解析出占用者 pid 与进程名。
func probePort(port int) PortInfo

// pickFreePort 从 from 开始 +1 找第一个空闲端口，上限 to。
func pickFreePort(from, to int) int

// processName 由 pid 反查进程名（Windows 用 tasklist /FI "PID eq N" /FO CSV /NH）。
func processName(pid int) string
```

实现细节：

- `probeNodes` 里 PATH 逐项拼接 `node.exe` 检查存在性，**不要**用 `exec.LookPath`（它只给第一个）。
- 每个候选跑探针都要**带超时**（2~3s），否则某个坏 shim 会把向导卡死。
- 探针命令统一走现有的 `hiddenCmd()`（不弹黑窗），复用 `dsh.go` 里那套 `CreationFlags`。
- 反查进程名不要引入新依赖：`hiddenCmd("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")` 解析第一列即可。

---

## 6. 下载层（`download.go`）

```go
// ProgressFn 是进度回调（接收已下载字节数与本轮总字节数，总长未知时为 -1）。
type ProgressFn func(received, total int64)

// fetchText 拉取小文本（index.json / SHASUMS256.txt）。
func fetchText(ctx context.Context, url string) (string, error)

// downloadFile 下载到 dst，支持断点续传（写 dst+".part"）与进度回调。
// 已存在 .part 时带 Range 头续传；服务器不支持 Range 则从头下。
func downloadFile(ctx context.Context, url, dst string, onProgress ProgressFn) error

// sha256File 计算文件摘要（流式，不整文件进内存）。
func sha256File(path string) (string, error)

// expectSHA256 从 SHASUMS256.txt 文本里取出指定文件名的摘要。
func expectSHA256(shasums, filename string) string

// unzipStripTop 解压 zip，strip 为 true 时剥掉最外层目录（node 包结构需要）。
// 用标准库 archive/zip，不引入第三方依赖。
func unzipStripTop(src, dst string, strip bool) error

// nodeMirrors 返回镜像列表，按顺序回退。
func nodeMirrors() []string

// latestLTSNode 从镜像的 index.json 里挑出最新的 LTS 版本号（避开 22.0~22.13）。
func latestLTSNode(ctx context.Context) (string, error)

// installPortableNode 完整走一遍：选版本 → 下载 → 校验 → 解压 → 原子落位 → 复检。
// 任一步失败都清理临时产物，不留下半成品。
func installPortableNode(ctx context.Context, onProgress ProgressFn) (NodeInfo, error)
```

关键约束：

- **镜像顺序**：`https://npmmirror.com/mirrors/node/` → `https://nodejs.org/dist/`。
- **sha256 校验失败必须硬失败**，删掉 `.part` 重下一次；连续失败则报 `E_SHA_MISMATCH`。
- **原子落位**：解压到 `runtime\node.tmp\`，校验通过后 `os.Rename` 成 `runtime\node\`；若目标已存在则先 `Rename` 成 `node.old` 再删。
- **超时**：单次请求空闲 60s、整体 10 分钟；失败重试 3 次（换镜像）。
- **取消**：`ctx` 取消 → 删 `.part` 与 `node.tmp`，恢复干净状态。

---

## 7. 向导服务（`setup.go`）

### 7.1 App 上新增的字段

```go
type App struct {
	// ... 既有字段

	cfg      Config
	stepSt   map[int]StepState // 各步当前状态
	setupMu  sync.Mutex        // 保护向导状态
	busy     bool              // 是否有安装任务在跑
	cancelFn context.CancelFunc // 当前安装任务的取消函数
	forced   bool              // 用户从托盘「环境设置」主动进入，强制展示向导
}
```

### 7.2 绑定给前端的方法（全部返回 `OpResult`）

```go
// GetSetupSnapshot 返回向导完整快照（页面加载 / 每次操作后调用）。
func (a *App) GetSetupSnapshot() OpResult

// RecheckSetup 重新检测指定步骤（step=0 表示全部重检）。
func (a *App) RecheckSetup(step int) OpResult

// UseNode 在候选列表里选定一个 Node（index 为 NodeChoices 下标）。
func (a *App) UseNode(index int) OpResult

// PickNodeFile 让用户手动指定 node.exe（系统文件对话框）。
func (a *App) PickNodeFile() OpResult

// InstallNode 自动下载安装便携版 Node（异步，进度走 setup:progress 事件）。
func (a *App) InstallNode() OpResult

// UseDsh 在候选列表里选定一个 dsh。
func (a *App) UseDsh(index int) OpResult

// PickDshFile 让用户手动指定 dsh 入口（bin.js 或 dsh.cmd）。
func (a *App) PickDshFile() OpResult

// InstallDsh 用配对的 node+npm 安装 dsh（异步，进度走事件）。
func (a *App) InstallDsh() OpResult

// CheckPort 校验端口并返回占用详情。
func (a *App) CheckPort(port int) OpResult

// AutoPickPort 自动挑一个空闲端口。
func (a *App) AutoPickPort() OpResult

// KillPort 结束占用端口的进程（复用现有 KillPortOccupant 的 killTree）。
func (a *App) KillPort(port int) OpResult

// CancelInstall 取消正在进行的安装。
func (a *App) CancelInstall() OpResult

// FinishSetup 写配置并进入启动流程（步骤 ④）。
func (a *App) FinishSetup(port int) OpResult

// ResetSetup 清空向导状态，回到第 1 步重新检测（供「环境设置」重跑）。
func (a *App) ResetSetup() OpResult

// ManualCommand 返回当前环境对应的兜底手动安装命令（供「复制」按钮）。
func (a *App) ManualCommand() string
```

约定：

- **所有方法都不返回 `error`**，错误统一塞进 `OpResult.Err`。这样前端不用解析字符串，指令码稳定。
- `InstallNode` / `InstallDsh` 立即返回（`busy=true`），实际安装跑在 goroutine，完成后发一次 `setup:step` 与最终 `snapshot`。
- 同一时刻只允许一个安装任务，`busy=true` 时再次调用直接返回 `E_BUSY`。

### 7.3 状态机

```
每步独立：stPending → stChecking → ┬→ stOK          （已满足，可跳过）
                                  └→ stMissing → stInstalling → ┬→ stOK
                                                                └→ stError（可重试）
```

四步的推进规则：

| 步骤 | 进入下一步的条件 |
|---|---|
| 1 Node | `Node.OK == true` |
| 2 dsh | `Dsh.OK == true` 且它配对的 node 与第 1 步选定的 node 一致 |
| 3 端口 | `PortInfo.Free == true` 且端口号合法 |
| 4 启动 | 无（终态，触发 `FinishSetup`） |

**「下一步」按钮置灰规则**：当前步不是 `stOK` 时禁用（不给用户跳过必填项的机会）；但 `stOK` 时按钮文案变「下一步」，并额外提供「重新检测」。

### 7.4 自动模式的启动分支

```go
// startup 里：读配置后决定进向导还是直接启动
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.cfg = LoadConfig()
	a.initSetupState() // 把四步置为 stPending
	writeFileLog("startup: 应用启动，日志文件 " + logFilePath())
	a.startTray()
	go a.heartbeat()

	go func() {
		if a.cfg.SetupCompleted && !a.forced {
			a.bootstrap(false) // 原路径
			return
		}
		a.enterSetup() // 新路径：只发快照，不启动 dsh
	}()
}

// enterSetup 切到向导视图，并立刻做一次全量检测。
func (a *App) enterSetup() {
	a.setStatus(stateSetup, "正在检测运行环境 ...")
	go func() {
		a.recheck(0)
		a.emitSnapshot()
	}()
}
```

### 7.5 完成安装（步骤 ④ → 原启动链）

```go
// FinishSetup 写配置 + 按新端口重建 runner + 交回 bootstrap。
func (a *App) FinishSetup(port int) OpResult {
	if err := a.requireSetupReady(port); err != nil { // 前置校验，缺一不可
		return opErr(err)
	}

	a.cfg.SetupCompleted = true
	a.cfg.Port = port
	a.cfg.LastCheckAt = time.Now().Format(time.RFC3339)
	if err := a.cfg.Save(); err != nil {
		// 写盘失败不阻断：内存生效 + 明确告知用户下次启动会再问一遍
		a.appendLog("配置写入失败（本次会话仍生效）: %v", err)
		a.emitSnack("配置未能保存到磁盘，本次可以正常使用，但下次启动需要重新检测。")
	}

	// 端口变了必须重建 runner，否则还是旧端口
	if port != a.runner.Port() {
		a.runner.Stop()
		a.runner = newDshRunner(port, func(line string) { a.appendLog("%s", line) })
		a.webURL = a.runner.RootURL()
	}

	go a.bootstrap(false) // 复用原逻辑：EnsureRunning → navigateToUI
	return a.opOK()
}
```

### 7.6 运行期不再猜 PATH（关键收益）

向导选定后，`dsh.go::resolveDshLaunch` 的**首选分支**变成「用 config 里的 node + dshBin 直接拼命令」，而不是走 PATH 猜测：

```go
// resolveDshLaunch 解析启动方式。cfg 完整时直接用它，彻底绕开 PATH 里多 node / 多 dsh 的混乱。
func resolveDshLaunch(port int, cfg Config) (*dshLaunch, error) {
	dshArgs := []string{"web", "--port", strconv.Itoa(port), "--no-open"}

	// 0) 向导选定的组合最可信
	if cfg.NodePath != "" && cfg.DshBin != "" && fileExists(cfg.NodePath) && fileExists(cfg.DshBin) {
		return &dshLaunch{
			Exe:   cfg.NodePath,
			Args:  append([]string{cfg.DshBin}, dshArgs...),
			Label: cfg.NodePath + " " + cfg.DshBin,
		}, nil
	}

	// 以下为原有候选链（DSH_BIN → PATH → 各 prefix 的 bin.js），保持不变
	// ...
}
```

这样**坑①（PATH 多 node）与坑②（prefix 指向错）在运行期彻底消失** —— 它们只会出现在向导的检测阶段，而检测阶段本来就枚举全量、允许人工指定。

---

## 8. 事件 schema

### `setup:step`

```json
{
  "step": 1, "total": 4, "state": "missing",
  "title": "检测 Node",
  "message": "未找到可用的 Node.js（需要 22.14 或更高）",
  "detail": "已扫描 PATH 与常见安装目录，共发现 2 个 node，均不满足版本要求",
  "canSkip": false
}
```

### `setup:progress`

```json
{
  "step": 1, "phase": "download", "percent": 42.7,
  "received": 12792627, "total": 29963358, "speed": 3145728,
  "note": "正在下载 node-v22.22.2-win-x64.zip（12.2 / 28.6 MB，3.0 MB/s）"
}
```

npm 阶段总量未知，`percent` 固定为 `-1`，前端切「不确定进度条 + 已用时」显示。

---

## 9. 错误码与文案

| Code | Message（给用户） | Hint（怎么办） | 可复制命令 |
|---|---|---|---|
| `E_NODE_MISSING` | 未找到可用的 Node.js | 点「自动安装」由应用装一个，或手动指定已有的 node.exe | — |
| `E_NODE_TOO_OLD` | 检测到 Node v{ver}，低于 dsh 要求的 22.14 | 升级 Node 到 22.14 以上 | — |
| `E_NODE_BROKEN` | Node 无法正常执行 | 可能被杀毒软件拦截，请检查隔离区后重试 | — |
| `E_NPM_NOT_FOUND` | 该 Node 未附带 npm | 换一个 Node，或用「自动安装」 | — |
| `E_DSH_MISSING` | 未找到 dsh | 点「自动安装」，或手动指定 bin.js | `npm i -g @deepseek-ai/dsh` |
| `E_DSH_BROKEN` | dsh 无法运行 | 与选定的 Node 不匹配，建议重新安装 | 同上 |
| `E_PORT_INVALID` | 端口需在 1024–65535 之间 | 换一个端口或点「自动选择」 | — |
| `E_PORT_IN_USE` | 端口 {port} 已被 {proc}(PID {pid}) 占用 | 换端口，或点「结束该进程」 | — |
| `E_NET` | 下载失败 | 已自动重试 3 次，请检查网络后重试 | — |
| `E_SHA_MISMATCH` | 安装包校验失败 | 下载的文件不完整或被篡改，请重试 | — |
| `E_NPM_FAIL` | dsh 安装失败 | 查看下方日志；也可复制命令自行安装 | 见 §5 完整参数 |
| `E_PERM` | 无法写入应用数据目录 | 检查 `%LOCALAPPDATA%\DSH Desktop` 权限 | — |
| `E_BUSY` | 已有安装任务正在进行 | 等待完成或点「取消」 | — |

---

## 10. 前端改造（`frontend/dist/index.html`）

### 10.1 视图结构

现有 splash 内容原样保留，外面套一层视图切换：

```html
<div class="view" id="view-wizard">…</div>
<div class="view" id="view-splash">…现有的 splash 内容…</div>
```

```js
function showView(name) {
  el('view-wizard').style.display = name === 'wizard' ? 'flex' : 'none';
  el('view-splash').style.display = name === 'splash' ? 'flex' : 'none';
}
```

分流：`status.state === 'setup'` → 向导；`starting/ready/error` → splash。**不改现有 `apply(status)` 的其余逻辑**。

### 10.2 向导 DOM 骨架

```html
<div class="view" id="view-wizard">
  <header class="wz-head">
    <img class="wz-logo" src="logo.png" alt="DeepSeek" />
    <div>
      <h1>环境配置</h1>
      <p class="sub">首次运行需要配置运行环境，只需一次</p>
    </div>
  </header>

  <div class="wz-body">
    <ol class="wz-steps" id="steps"></ol>
    <section class="wz-panel" id="panel"></section>
  </div>

  <footer class="wz-foot">
    <button id="btnPrev" onclick="goPrev()">上一步</button>
    <span class="wz-spacer"></span>
    <button id="btnGhost" onclick="onGhost()"></button>
    <button class="primary" id="btnNext" onclick="goNext()">下一步</button>
  </footer>

  <pre class="log" id="wz-log"></pre>
</div>
```

### 10.3 关键 JS 函数

```js
let snap = null;   // 最新快照
let step = 1;      // 当前展示的步骤

function renderSteps()             // 渲染左侧 4 步，按 snap.steps[i].state 上图标与颜色
function renderPanel()             // 按 step 渲染右侧内容（候选列表 / 端口表单 / 启动摘要）
function applySnapshot(s)          // 收事件与初始拉取的统一入口
function goNext() / goPrev()       // 步骤切换，进入下一步前先调 RecheckSetup(step+1)
function onGhost()                 // 次要按钮：缺失→「自动安装」 / 已满足→「重新检测」
async function call(fn, ...args)   // 统一 await native 方法 + 处理 OpResult.Err
function toastErr(e)               // 用 e.Code/e.Hint 展示错误块 + 「复制命令」按钮
function fmtBytes(n) / fmtSpeed(n) // 进度格式化
```

### 10.4 交互要点

- 每个 native 调用都走 `call()`：`ok=false` 时**不抛异常**，而是渲染一个错误块（Message + Hint + 可选「复制命令」）。
- 步骤 2 选完 dsh 后，若它配对的 node 与步骤 1 选定的 node **不一致**，给出黄色提示条（「两者不匹配，建议改用同一套」）但不硬拦。
- 端口输入框 `oninput` 防抖 300ms 调 `CheckPort`；`snap.portFixed` 为 true 时输入框 `disabled` 并显示「由环境变量 DSH_WEB_PORT 指定」。
- 安装中：`btnNext` 禁用、`btnGhost` 变「取消」、底部日志区实时滚动。
- 端口被占用时，面板上直接给「结束该进程（显示进程名与 PID）」按钮 —— 这正是现有 `KillPortOccupant` 的场景。

---

## 11. 托盘与关闭行为（`tray.go` / `main.go`）

```go
// tray.go 新增菜单项
mSetup := systray.AddMenuItem("环境设置…", "重新检测并配置运行环境")
mSetup.Click(func() { a.trayAction(a.reopenSetup) })

// reopenSetup 强制回到向导视图
func (a *App) reopenSetup() {
	a.setupMu.Lock()
	a.forced = true
	a.setupMu.Unlock()
	if a.ctx != nil {
		runtime.WindowReloadApp(a.ctx) // 回到 index.html，前端据状态展示向导
		a.showWindow()
	}
}
```

`main.go` 的 `OnBeforeClose` 增加向导分支：

```go
OnBeforeClose: func(ctx context.Context) bool {
	if app.isQuitting() {
		return false
	}
	// 向导期间：窗口就是全部交互界面，收起托盘会让用户以为装完了
	if app.inSetup() {
		if app.busy {
			choice := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
				Type:    runtime.QuestionDialog,
				Title:   "安装尚未完成",
				Message: "正在安装运行环境，退出会中断安装。确定要退出吗？",
			})
			if choice != "Yes" {
				return true // 阻止关闭
			}
		}
		app.quitApp()
		return false
	}
	app.hideToTray()
	return true
},
```

---

## 12. UI 线框（步骤 1 · 缺失 Node 态）

```
┌──────────────────────────────────────────────────────────────┐
│  [logo]  环境配置                                             │
│          首次运行需要配置运行环境，只需一次                     │
├────────────────────┬─────────────────────────────────────────┤
│ 1 检测 Node    ●   │  检测 Node                              │
│   未找到可用版本    │  未找到可用的 Node.js（需要 22.14 或更高）│
│                    │                                         │
│ 2 检测 dsh     ○   │  已扫描 PATH 与常见安装目录：             │
│                    │  ┌───────────────────────────────────┐  │
│ 3 确认端口     ○   │  │ ✗ D:\soft\nodejs\node.exe         │  │
│                    │  │   v22.22.2 · 可执行                  │  │
│ 4 启动运行     ○   │  ├───────────────────────────────────┤  │
│                    │  │ ! C:\...\workbuddy\...\node.exe   │  │
│                    │  │   v22.22.2 · 来自 PATH              │  │
│                    │  └───────────────────────────────────┘  │
│                    │                                         │
│                    │  未找到可用的 Node.js 版本               │
│                    │  ▸ 手动指定 node.exe                     │
│                    │  ▸ 复制手动安装命令                      │
├────────────────────┴─────────────────────────────────────────┤
│  上一步          [ 自动安装 ]        [ 下一步 ]（置灰）        │
├──────────────────────────────────────────────────────────────┤
│  15:22:01  正在扫描 PATH ...                                  │
│  15:22:02  发现 2 个候选，均不满足 import.meta.main 探针       │
└──────────────────────────────────────────────────────────────┘
```

（实际配色沿用现有变量：`--brand:#4D6BFE`、`--line:#e8eaef`、`--err:#d93025`；绿色勾用 splash 已有的 `#16a34a`。）

---

## 13. 测试清单

| # | 场景 | 构造方式 | 期望 |
|---|---|---|---|
| 1 | 全缺失 | 无 node 的干净环境（或临时改名 node.exe） | 步骤 1 报 `E_NODE_MISSING`，「自动安装」可用 |
| 2 | 有 node 但 < 22.14 | 装 22.13 或更低 | 步骤 1 报 `E_NODE_TOO_OLD`，**不放过**；探针返回 false |
| 3 | PATH 多 node | 本机现状 | 两候选都列出，能手动切换，选中项被正确用于步骤 2 |
| 4 | 有 node 无 dsh | 卸载 dsh | 步骤 2 缺失，安装后 prefix 与用户全局包一致（若复用） |
| 5 | 端口被占 | 手动 `node -e "require('net').createServer().listen(3388)"` | 步骤 3 显示占用 PID + 进程名，「结束该进程」生效 |
| 6 | 断网下载 | 断网后点自动安装 | 3 次重试后报 `E_NET`，**不卡死**，无残留 `.part`/`node.tmp` |
| 7 | 校验失败 | 篡改 `.part` 后重试 | 报 `E_SHA_MISMATCH` 并重下 |
| 8 | 安装中取消 | 下载中点取消 | 进程树被杀、临时目录清理、回到 `stMissing` |
| 9 | 写配置失败 | 数据目录设为只读 | 提示但不阻断，本次会话可正常启动 |
| 10 | 二次启动 | 首次配好后再启动 | **不进向导**，直接走 splash → Web UI |
| 11 | 托盘重进 | 托盘「环境设置…」 | 回到向导，且不中断正在运行的 dsh |
| 12 | 向导期关窗 | 安装中点关闭 | 弹确认；取消则窗口保留 |

对应脚本：`go test .`（断言）+ `go test -run TestDiagSetup -v .`（打印探测结果，与 `where node` / `where dsh` / `netstat` 对照）。

---

## 14. 实施顺序（P0 内部拆解）

| 序 | 内容 | 完成标志 |
|---|---|---|
| 1 | `config.go` + `setup_types.go` + 常量 | 能读写 config.json（原子） |
| 2 | `env_probe.go` | `TestDiagSetup` 输出与手工命令一致 |
| 3 | `setup.go`：快照 / 重检 / 选择 / 端口（**不含安装**） | 四步能检测、能选、能改端口 |
| 4 | `index.html`：向导视图 + 步骤 1~3 交互 | 手工点完四步能进启动 |
| 5 | `dsh.go` / `app.go` / `main.go` / `tray.go` 接线 | 二次启动不再走向导；托盘可重进 |
| 6 | `download.go` + `InstallNode` | 干净环境能自助装好 Node |
| 7 | `InstallDsh` + 取消/镜像回退 | 完整闭环 |

**第 1~5 步就是 P0**，跑通后再做 6~7（P1/P2）。

---

## 15. 实现记录与偏差（2026-09-19）

代码已按本设计实现完毕，`go vet` / `go test` / `wails build` 三项通过。以下是实现时相对设计的改动，
均已在代码中生效：

| # | 项 | 说明 |
|---|---|---|
| 1 | 方法挂在 `App` 上 | 没有另起 `SetupService`，17 个方法直接是 `App` 的方法（§2 允许的两个选项之一），`main.go` 的 `Bind` 无需改动。向导状态收在一个独立子结构 `setupState` 里（自带锁），避免与主状态锁 `App.mu` 互相等待 |
| 2 | 多了 `setup:snapshot` 事件 | 异步安装（Node / dsh）完成后必须让界面整体刷新，只靠 `setup:step` 不够。三个事件分工：`setup:step` 只管单步文案、`setup:progress` 管进度、`setup:snapshot` 管整体重取 |
| 3 | `Snapshot` 增加 `Err` 字段 | 异步安装的失败明细只能靠事件送达，放进快照里前端才能拿到 `code` / `hint` / 可复制命令 |
| 4 | 去掉 `Snapshot.Version` | 与 `wails.json` 的 `productVersion` 重复，会是第二个真相来源；且前端没用到 |
| 5 | `setupState` 增加 `probeMu` | 启动时的自动检测与前端主动重检可能并发，不加锁会让两轮检测的步骤状态交错（一边刚置 `ok`，另一边又置回 `checking`） |
| 6 | **版本探针加一次重试**（`nodeVersionStable`） | 实测踩到：同一台机器上 `node -v` 会**偶发**以非零码退出且无任何输出（杀软扫描 / 首次运行被拦一类）。单次失败就把可用的 Node 判成不可用，会把用户往「重装一个」的错误方向推 |
| 7 | 探针失败原因带出底层错误 | 原来只写「无法执行」，排查时无从下手；现在附上 `exit status N` 等原始信息并截断 |
| 8 | 增加 `adoptDshNode` | 机器上常有多个 node，只有一个是 dsh 真正装在其前缀下的。自动把第 1 步对齐到 dsh 实际使用的那个，避免给出没有实际意义的「两者不匹配」提示 |
| 9 | dsh 候选去重 | 同一个安装目录往往同时有 `bin.js` 与 `dsh.cmd`（npm 生成的转发脚本），是同一份安装；优先登记 `bin.js`，命中就不再登记 `cmd` |
| 10 | 前端端口输入框焦点保持 | 每次 `render()` 都会重建面板，会把正在输入的端口框焦点打断（输入第二个数字就跳走）。改为记住焦点再恢复，并把防抖计时器提到外层作用域 |
| 11 | splash 增加「重新配置环境」按钮 | 覆盖「dsh 因 nvm 切版本而消失」这类启动失败场景，用户不必去找托盘菜单 |
| 12 | `reopenSetup` 先切状态再刷页面 | 反过来的话，页面重载后拉到的还是旧状态，会显示成启动页 |
| 13 | 验收脚本由 Python 改为 Go 测试 | 原计划写 `scripts/diag_setup.py`，但那等于用另一种语言再实现一遍探测逻辑，测不出真实代码的问题。改为直接 `go test` 真实函数 |
