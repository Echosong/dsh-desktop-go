package main

// 向导步骤编号
const (
	stepNode = iota + 1 // 1 检测 Node
	stepDsh             // 2 检测 dsh
	stepPort            // 3 确认端口
	stepRun             // 4 启动运行
)

// stepTotal 是向导总步数。
const stepTotal = 4

// 单步状态
const (
	stPending    = "pending"    // 未开始
	stChecking   = "checking"   // 检测中
	stOK         = "ok"         // 已满足，可继续
	stMissing    = "missing"    // 缺失，可安装
	stInstalling = "installing" // 安装中
	stError      = "error"      // 失败，可重试
	stDone       = "done"       // 已完成（第 4 步的终态）
)

// 安装来源，决定卸载时该不该动它
const (
	srcExisting = "existing" // 用户机器上本来就有的（PATH / config / DSH_BIN），绝不动
	srcManaged  = "managed"  // 本应用装进 runtime 目录的，可卸载
	srcPicked   = "picked"   // 用户手动指定的
)

// dsh 入口类型
const (
	kindJS  = "js"  // bin.js，需要用配对的 node 执行
	kindCmd = "cmd" // dsh.cmd / .exe / .bat，直接执行
)

// Node 硬下限：dsh 的 bin.js 使用 import.meta.main，该语法 Node 22.14 才加入。
// 低于此版本 dsh 任何子命令都会零输出静默退出，表现为「点了没反应」。
const (
	minNodeMajor = 22
	minNodeMinor = 14
)

// minWizardPort 是向导 UI 建议的端口下限（更低的端口在部分机器上需要提权）。
const minWizardPort = 1024

// 自动挑空闲端口时的扫描上限。
const portScanMax = 3400

// 向导相关事件名（与 app.go 里的 evtStatus / evtLog 同一套广播机制）
const (
	evtSetupStep     = "setup:step"
	evtSetupProgress = "setup:progress"
	evtSetupSnapshot = "setup:snapshot"
)

// NodeInfo 是一个候选 Node 运行时。
type NodeInfo struct {
	Path    string `json:"path"`    // node.exe 绝对路径
	Version string `json:"version"` // 形如 22.22.2，取不到时为空
	OK      bool   `json:"ok"`      // 是否满足 dsh 的硬要求
	Source  string `json:"source"`  // runtime | config | path | scan | picked
	Reason  string `json:"reason"`  // 不合格原因，可直接展示给用户
	Prefix  string `json:"prefix"`  // 该 node 对应的 npm 全局前缀
	NpmCLI  string `json:"npmCli"`  // npm-cli.js 绝对路径，为空表示未附带 npm
}

// DshInfo 是一个候选 dsh 安装。
type DshInfo struct {
	Bin     string `json:"bin"`     // bin.js 或 dsh.cmd 的绝对路径
	Kind    string `json:"kind"`    // js | cmd
	Node    string `json:"node"`    // 与之配对的 node.exe
	Version string `json:"version"` // 形如 0.1.5-rc.1
	Source  string `json:"source"`  // config | managed | env | path | prefix | picked
	Prefix  string `json:"prefix"`  // 所属 npm 前缀
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
	Message string `json:"message"` // 可直接展示的说明
}

// StepState 是向导单步的快照。
type StepState struct {
	Step    int    `json:"step"`
	Total   int    `json:"total"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	CanSkip bool   `json:"canSkip"`
}

// Progress 是下载 / 安装进度。Percent 为 -1 表示总量未知（npm 安装阶段）。
type Progress struct {
	Step     int     `json:"step"`
	Phase    string  `json:"phase"` // download | verify | extract | npm | done
	Percent  float64 `json:"percent"`
	Received int64   `json:"received"`
	Total    int64   `json:"total"`
	Speed    int64   `json:"speed"` // bytes/s
	Note     string  `json:"note"`
}

// Snapshot 是向导的整体快照，前端一次拉取即可渲染全部内容。
type Snapshot struct {
	Required    bool        `json:"required"`
	Steps       []StepState `json:"steps"`
	Node        *NodeInfo   `json:"node"`
	NodeChoices []NodeInfo  `json:"nodeChoices"`
	Dsh         *DshInfo    `json:"dsh"`
	DshChoices  []DshInfo   `json:"dshChoices"`
	Port        PortInfo    `json:"port"`
	PortFixed   bool        `json:"portFixed"`
	Busy        bool        `json:"busy"`
	Err         *SetupError `json:"err"` // 最近一次失败的明细，供前端展示「怎么办」与可复制命令
}

// SetupError 是结构化错误，前端不必解析字符串就能拿到展示所需的全部信息。
type SetupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	Cmd     string `json:"cmd"` // 可复制的兜底手动命令，可为空
}

// Error 让 SetupError 满足 error 接口，便于当成普通错误往上传。
func (e *SetupError) Error() string { return e.Message }

// OpResult 是向导所有操作的统一返回，避免前端去解析 error 字符串。
type OpResult struct {
	OK       bool        `json:"ok"`
	Err      *SetupError `json:"err"`
	Snapshot Snapshot    `json:"snapshot"`
}

// stepTitle 返回步骤标题。
func stepTitle(step int) string {
	switch step {
	case stepNode:
		return "检测 Node"
	case stepDsh:
		return "检测 dsh"
	case stepPort:
		return "确认端口"
	case stepRun:
		return "启动运行"
	}
	return ""
}

// newSetupError 构造结构化错误。
func newSetupError(code, message, hint, cmd string) *SetupError {
	return &SetupError{Code: code, Message: message, Hint: hint, Cmd: cmd}
}

// errBusy 返回「已有任务在跑」的错误。
func errBusy() *SetupError {
	return newSetupError("E_BUSY", "已有安装任务正在进行", "等待完成，或点「取消」后再试", "")
}

// allowScriptsFlag 是安装 dsh 必须带上的参数。
// 不带的话装完「看着成功」，但 node-pty / koffi 这类原生模块的 postinstall 没跑，
// 表现为终端与子进程能力不可用。
const allowScriptsFlag = "--allow-scripts=@deepseek-ai/dsh-subprocess-local,koffi,node-pty,@google/genai,protobufjs"

// manualInstallCommand 返回兜底的手动安装命令。
// 优先给出「用配对的 node 直接调 npm-cli.js」的精确写法，避免用户照着裸 npm 装到别的 prefix 下；
// 拿不到 npm-cli.js 时退回最朴素的 npm 写法。
func manualInstallCommand(nodePath, npmCLI, prefix string) string {
	if prefix == "" {
		prefix = runtimeNpmGlobal()
	}
	args := "install -g --prefix \"" + prefix + "\" " + allowScriptsFlag + " @deepseek-ai/dsh"
	if nodePath != "" && npmCLI != "" {
		return "\"" + nodePath + "\" \"" + npmCLI + "\" " + args
	}
	return "npm " + args
}
