package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// createNewProcessGroup 让子进程成为独立进程组，便于整棵进程树一起结束。
const createNewProcessGroup = 0x00000200

// createNoWindow 让子进程完全不创建控制台窗口。
// GUI 程序里执行 cmd.exe / netstat / taskkill 这类控制台命令时，系统会新开一个黑窗；
// 启动 dsh 的 cmd.exe 更是一直挂在后面，所以必须显式关掉。
const createNoWindow = 0x08000000

// hiddenCmd 构造一个不会弹出控制台窗口的命令。
func hiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
		HideWindow:    true,
	}
	return cmd
}

// dshLaunch 描述“如何启动 dsh”已经解析好的结果。
type dshLaunch struct {
	Exe   string
	Args  []string
	Label string // 仅用于日志展示
}

// dshRunner 负责 dsh web 子进程的生命周期。
type dshRunner struct {
	mu      sync.Mutex
	port    int
	cmd     *exec.Cmd
	owns    bool // 进程是否由本应用启动
	stopped bool
	onLog   func(string)

	urlCh chan string
	exitCh chan error
}

func newDshRunner(port int, onLog func(string)) *dshRunner {
	return &dshRunner{
		port:  port,
		onLog: onLog,
		urlCh: make(chan string, 4),
	}
}

// Exited 返回子进程退出通知通道（未接管进程时为 nil）。
func (r *dshRunner) Exited() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitCh
}

// Port 返回 dsh web 监听的端口。
func (r *dshRunner) Port() int { return r.port }

// DSHPid 返回当前 dsh 子进程的 pid（未启动时为 0）。
func (r *dshRunner) DSHPid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}

// UIConnected 判断 dsh 的 Web UI 是否已经真正连上服务：
// 界面加载成功后会与服务端建立 WebSocket 等长连接，因此「监听端口的那个进程
// 上存在 ESTABLISHED 连接」即可作为界面已就绪的信号。
//
// 注意不能用 r.cmd.Process.Pid 比对：在 Windows 上命令是经由 cmd.exe 转发的，
// 真正监听端口的是它派生的 node 进程，pid 并不相同。
func (r *dshRunner) UIConnected() bool {
	out, err := hiddenCmd("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return false
	}
	suffix := fmt.Sprintf(":%d", r.port)

	type row struct {
		pid   string
		state string
	}
	var rows []row
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		rows = append(rows, row{pid: fields[len(fields)-1], state: strings.ToUpper(fields[3])})
	}

	listener := ""
	for _, it := range rows {
		if it.state == "LISTENING" {
			listener = it.pid
			break
		}
	}
	if listener == "" {
		return false
	}
	for _, it := range rows {
		if it.state == "ESTABLISHED" && it.pid == listener {
			return true
		}
	}
	return false
}

// IsStopped 报告当前进程是否已被本应用主动结束。
func (r *dshRunner) IsStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

func (r *dshRunner) log(format string, a ...any) {
	if r.onLog == nil {
		return
	}
	r.onLog(fmt.Sprintf(format, a...))
}

// RootURL 是不带访问令牌的地址。它必须与 dsh 打印的令牌地址保持**完全相同的 host**
// （都用 127.0.0.1），因为 dsh 的 Cookie 名由 authority 的哈希决定，host 不一致会被判为未授权。
func (r *dshRunner) RootURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", r.port)
}

// EnsureRunning 启动 dsh web 并等待其输出带令牌的访问地址。
func (r *dshRunner) EnsureRunning(timeout time.Duration) (string, error) {
	if r.portInUse() {
		return "", fmt.Errorf("端口 %d 已被占用，可能是上次异常退出残留的 dsh web 进程。可点击“强制重启”结束占用进程后重试，或设置环境变量 DSH_WEB_PORT 换一个端口", r.port)
	}

	launch, err := resolveDshLaunch(r.port)
	if err != nil {
		return "", err
	}
	r.log("启动命令: %s %s", launch.Label, strings.Join(launch.Args, " "))

	cmd := exec.Command(launch.Exe, launch.Args...)
	// 独立进程组便于整树结束；不创建控制台窗口，避免后面一直挂着一个 cmd 黑窗
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("创建输出管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("创建错误管道失败: %w", err)
	}

	// 每次启动都用全新的令牌通道，避免上一次的残留值
	r.mu.Lock()
	r.urlCh = make(chan string, 4)
	urlCh := r.urlCh
	r.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 dsh 失败: %w", err)
	}

	r.mu.Lock()
	r.cmd = cmd
	r.owns = true
	r.stopped = false
	r.mu.Unlock()

	// 纳入 Job Object：应用无论以何种方式消失，系统都会连带结束 dsh 进程树
	if err := attachToJob(cmd.Process.Pid); err != nil {
		r.log("未能将 dsh 加入 Job Object（不影响正常退出清理）: %v", err)
	} else {
		r.log("dsh 已纳入 Job Object，应用退出时会自动结束")
	}

	go r.pipe(stdout)
	go r.pipe(stderr)

	exitCh := make(chan error, 1)
	r.mu.Lock()
	r.exitCh = exitCh
	r.mu.Unlock()
	go func() { exitCh <- cmd.Wait() }()

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	warned := false
	for {
		select {
		case u := <-urlCh:
			r.log("dsh web 已就绪: %s", u)
			return u, nil

		case err := <-exitCh:
			return "", fmt.Errorf("dsh 进程已退出: %v", err)

		case <-time.After(10 * time.Second):
			if !warned && r.portOpen() {
				warned = true
				r.log("端口 %d 已监听，仍在等待 dsh 输出访问地址 ...", r.port)
			}

		case <-timeoutTimer.C:
			return "", fmt.Errorf("等待 dsh web 就绪超时（%s）", timeout)
		}
	}
}

// Restart 结束当前 dsh web 并重新拉起。
func (r *dshRunner) Restart(timeout time.Duration) (string, error) {
	r.Stop()
	r.KillPortOccupant()
	time.Sleep(500 * time.Millisecond)
	return r.EnsureRunning(timeout)
}

// Stop 结束由本应用启动的 dsh 进程树（外部启动的不动）。
func (r *dshRunner) Stop() {
	r.mu.Lock()
	cmd := r.cmd
	owns := r.owns
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.cmd = nil
	r.mu.Unlock()

	if !owns || cmd == nil || cmd.Process == nil {
		return
	}
	killTree(cmd.Process.Pid)
	r.log("已结束 dsh web 进程（pid=%d）", cmd.Process.Pid)
}

// KillPortOccupant 结束占用目标端口的进程（用于处理上次残留、导致端口冲突的情况）。
func (r *dshRunner) KillPortOccupant() bool {
	pids := listeningPIDs(r.port)
	if len(pids) == 0 {
		return false
	}
	for _, pid := range pids {
		r.log("正在结束占用端口 %d 的进程（pid=%d）", r.port, pid)
		killTree(pid)
	}
	time.Sleep(600 * time.Millisecond)
	return true
}

// portOpen 判断端口是否已被监听。
func (r *dshRunner) portOpen() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// portInUse 判断端口是否已被别的进程占用。
func (r *dshRunner) portInUse() bool {
	return len(listeningPIDs(r.port)) > 0
}

func (r *dshRunner) pipe(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		r.log("%s", line)

		if u := extractWebURL(line, r.port); u != "" {
			r.mu.Lock()
			ch := r.urlCh
			r.mu.Unlock()
			select {
			case ch <- u:
			default:
			}
		}
	}
}

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// extractWebURL 从 dsh 的输出行里解析出可访问的 Web 地址。
// dsh web 启动后会打印形如 http://127.0.0.1:3388/?token=xxxx 的地址，
// 直接访问不带令牌的根路径会返回 401，因此必须使用带令牌的地址。
func extractWebURL(line string, port int) string {
	candidate := urlPattern.FindString(line)
	if candidate == "" {
		return ""
	}
	candidate = strings.TrimRight(candidate, ".,;:)'\"]")
	if !strings.Contains(candidate, fmt.Sprintf(":%d", port)) {
		return ""
	}
	if !strings.Contains(candidate, "token=") {
		return ""
	}
	return candidate
}

// killTree 结束指定 pid 的整棵进程树。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		kill := hiddenCmd("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		_ = kill.Run()
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// listeningPIDs 返回监听指定端口的进程 pid（解析 netstat 输出）。
func listeningPIDs(port int) []int {
	out, err := hiddenCmd("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil
	}
	suffix := fmt.Sprintf(":%d", port)
	self := os.Getpid()

	seen := map[int]bool{}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[len(fields)-2], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 || pid == self || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}

// resolveDshLaunch 依次尝试多种方式定位 dsh 命令。
func resolveDshLaunch(port int) (*dshLaunch, error) {
	dshArgs := []string{"web", "--port", strconv.Itoa(port), "--no-open"}

	// 1) 显式指定（可为 dsh.cmd / dsh.exe / bin.js 的路径）
	if bin := strings.TrimSpace(os.Getenv("DSH_BIN")); bin != "" {
		if strings.HasSuffix(strings.ToLower(bin), ".js") {
			node, err := exec.LookPath("node")
			if err != nil {
				return nil, fmt.Errorf("DSH_BIN 指向 js 文件但找不到 node: %w", err)
			}
			return &dshLaunch{Exe: node, Args: append([]string{bin}, dshArgs...), Label: node + " " + bin}, nil
		}
		return wrapCommand(bin, dshArgs, bin), nil
	}

	// 2) PATH 中的 dsh（Windows 下 Go 会按 PATHEXT 命中 dsh.cmd）
	if p, err := exec.LookPath("dsh"); err == nil {
		// npm 生成的 dsh 是 sh 脚本，Windows 上不可直接执行，优先用同目录的 dsh.cmd
		if runtime.GOOS == "windows" && !hasExecutableExt(p) {
			for _, ext := range []string{".cmd", ".exe", ".bat"} {
				if cand := p + ext; fileExists(cand) {
					return wrapCommand(cand, dshArgs, cand), nil
				}
			}
		}
		return wrapCommand(p, dshArgs, p), nil
	}

	// 3) 常见 npm 全局目录里找 bin.js，用 node 直接跑
	if node, err := exec.LookPath("node"); err == nil {
		for _, candidate := range dshBinCandidates() {
			if fileExists(candidate) {
				return &dshLaunch{
					Exe:   node,
					Args:  append([]string{candidate}, dshArgs...),
					Label: node + " " + candidate,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("未找到 dsh 命令，请先安装（npm i -g @deepseek-ai/dsh），或设置环境变量 DSH_BIN 指向 dsh.cmd")
}

// wrapCommand 处理 Windows 上 .cmd/.bat 必须经由 cmd.exe 执行的问题。
func wrapCommand(path string, args []string, label string) *dshLaunch {
	ext := strings.ToLower(filepath.Ext(path))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		full := append([]string{"/c", path}, args...)
		return &dshLaunch{Exe: "cmd.exe", Args: full, Label: label}
	}
	return &dshLaunch{Exe: path, Args: args, Label: label}
}

func hasExecutableExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".exe", ".cmd", ".bat", ".com":
		return true
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dshBinCandidates() []string {
	rel := filepath.Join("node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	var roots []string

	if appData := os.Getenv("APPDATA"); appData != "" {
		roots = append(roots, filepath.Join(appData, "npm"))
	}
	if prefix := os.Getenv("npm_config_prefix"); prefix != "" {
		roots = append(roots, prefix)
	}
	if progFiles := os.Getenv("ProgramFiles"); progFiles != "" {
		roots = append(roots, filepath.Join(progFiles, "nodejs"))
	}
	// PATH 里含 node 的目录，多半就是 npm 的全局前缀
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		lower := strings.ToLower(dir)
		if strings.Contains(lower, "nodejs") || strings.Contains(lower, "npm") {
			roots = append(roots, dir)
		}
	}
	// 当前可执行文件所在目录（便于绿色版随包携带）
	if self, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(self))
	}

	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		cand := filepath.Join(root, rel)
		if !seen[cand] {
			seen[cand] = true
			out = append(out, cand)
		}
	}
	return out
}
