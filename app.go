package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// logFilePath 返回运行日志文件路径：%LOCALAPPDATA%\DSH Desktop\app.log
// 生产模式下 exe 没有控制台，日志落盘便于排查启动问题。
func logFilePath() string {
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "app.log")
}

var (
	fileLogOnce sync.Once
	fileLog     *os.File
)

// writeFileLog 把一行日志追加到磁盘（同时限制文件大小）。
func writeFileLog(line string) {
	fileLogOnce.Do(func() {
		path := logFilePath()
		if st, err := os.Stat(path); err == nil && st.Size() > 2*1024*1024 {
			_ = os.Remove(path) // 超过 2MB 直接重置
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			fileLog = f
		}
	})
	if fileLog == nil {
		return
	}
	_, _ = fileLog.WriteString(time.Now().Format("2006-01-02 15:04:05.000") + "  " + line + "\r\n")
	_ = fileLog.Sync()
}

const (
	defaultPort    = 3388
	startupTimeout = 3 * time.Minute
)

// 应用状态
const (
	stateSetup    = "setup" // 首次运行向导
	stateStarting = "starting"
	stateReady    = "ready"
	stateError    = "error"
)

// 推送给前端的事件名
const (
	evtStatus = "dsh:status"
	evtLog    = "dsh:log"
)

// Status 是推送给 splash 页面的状态快照。
type Status struct {
	State   string `json:"state"`
	Message string `json:"message"`
	URL     string `json:"url"`
	Port    int    `json:"port"`
}

// App 是绑定给前端的应用对象。
type App struct {
	ctx context.Context

	mu      sync.Mutex
	runner  *dshRunner
	status  Status
	webURL  string
	logs    []string
	maxLogs int

	cfg   Config
	setup *setupState

	domReady     chan struct{}
	domReadyOnce sync.Once
	quitting     bool
}

// NewApp 创建应用实例。
func NewApp() *App {
	cfg := LoadConfig()
	port, _ := cfg.ResolvedPort()

	app := &App{maxLogs: 400, domReady: make(chan struct{}), cfg: cfg}
	app.runner = newDshRunner(port, func(line string) { app.appendLog("%s", line) })
	app.initSetupState()
	app.webURL = app.runner.RootURL()

	// 已经配置过环境就直接启动；否则先进向导，避免先闪一下启动页再跳走
	if cfg.SetupCompleted {
		app.status = Status{
			State:   stateStarting,
			Message: "正在启动 dsh web ...",
			URL:     app.webURL,
			Port:    port,
		}
	} else {
		app.status = Status{
			State:   stateSetup,
			Message: "正在检测运行环境 ...",
			URL:     app.webURL,
			Port:    port,
		}
	}
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	writeFileLog("startup: 应用启动，日志文件 " + logFilePath())
	a.startTray()
	go a.heartbeat()

	go func() {
		if a.cfg.SetupCompleted {
			// 已经配好：直接启动；若 dsh 已不在原位，启动失败时会给出一键重跑的入口
			a.bootstrap(false)
			return
		}
		a.enterSetup()
	}()
}

// onDomReady 标记启动页已就绪，导航必须等它之后才能注入。
func (a *App) onDomReady(ctx context.Context) {
	a.domReadyOnce.Do(func() { close(a.domReady) })
}

// showWindow 把主窗口显示出来（托盘点击 / 单击图标时调用）。
func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
	a.setTrayTooltip("DSH Desktop — DeepSeek Harness")
}

// quitApp 真正退出应用：托盘菜单「退出」才会走到这里。
func (a *App) quitApp() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

// isQuitting 报告是否正在主动退出（用于区分「点关闭按钮」和「退出」）。
func (a *App) isQuitting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quitting
}

// hideToTray 把窗口收进托盘（点击窗口关闭按钮时调用）。
func (a *App) hideToTray() {
	if a.ctx == nil {
		return
	}
	runtime.WindowHide(a.ctx)
	a.setTrayTooltip("DSH Desktop — 已收进托盘，右键图标可退出")
	writeFileLog("窗口已隐藏到系统托盘（右键托盘图标可退出）")
}

// heartbeat 在设置了 DSH_DEBUG=1 时每 5 秒记录一次存活，用于排查异常退出。
func (a *App) heartbeat() {
	if os.Getenv("DSH_DEBUG") == "" {
		return
	}
	for i := 1; ; i++ {
		time.Sleep(5 * time.Second)
		writeFileLog(fmt.Sprintf("heartbeat #%d", i))
	}
}

func (a *App) shutdown(ctx context.Context) {
	writeFileLog("shutdown: 应用退出，清理 dsh 进程")
	a.runner.Stop()
	a.stopTray()
}

// bootstrap 启动 dsh web 并等待就绪，全程把状态推给 splash 页面。
// force 为 true 时会先结束占用端口的进程。
func (a *App) bootstrap(force bool) {
	a.setStatus(stateStarting, "正在启动 dsh web ...")

	var (
		url string
		err error
	)
	if force {
		a.runner.KillPortOccupant()
		time.Sleep(300 * time.Millisecond)
		url, err = a.runner.EnsureRunning(startupTimeout)
	} else {
		url, err = a.runner.EnsureRunning(startupTimeout)
	}

	if err != nil {
		a.appendLog("启动失败: %v", err)
		a.setStatus(stateError, err.Error())
		return
	}

	a.mu.Lock()
	a.webURL = url
	a.status.URL = url
	a.mu.Unlock()

	a.setStatus(stateReady, "dsh web 已就绪")
	go a.watchRunner(a.runner.Exited())
	a.navigateToUI(url)
}

// navigateToUI 把窗口带到 dsh 的 Web UI。
//
// 这里不能从启动页直接跳到带令牌的地址：启动页在 http://wails.localhost，
// 与 dsh 的 http://127.0.0.1:<port> 不是同一个 site，而 dsh 的会话 Cookie 是
// `HttpOnly; SameSite=Strict`，跨站发起的导航链不会携带它，页面会落到
// "dsh web authentication required"。
//
// 经实测（scripts/diag_samesite.py / diag_redirect.py），除了「文档已经落在 dsh 站点上」
// 之外没有别的绕法：HTTP 302 重定向同样不行（Chrome 会沿用最初的 initiator）。
// 所以流程是：先把文档带到 dsh 的站，再由该同站文档重发带令牌的导航。
//
// 中间那一两秒必然经过 dsh 的鉴权拦截页，因此**切换期间把窗口藏起来**，
// 等界面真正连上服务（出现长连接）再显示，用户只会看到启动页无缝变成 Web UI。
func (a *App) navigateToUI(tokenURL string) {
	if a.ctx == nil {
		return
	}
	select {
	case <-a.domReady:
	case <-time.After(3 * time.Second):
	}

	runtime.WindowHide(a.ctx)
	a.appendLog("正在打开 Web UI ...")
	time.Sleep(150 * time.Millisecond)

	// 第一次导航：带令牌。它会写入会话 Cookie，但随后会落到鉴权拦截页
	runtime.WindowExecJS(a.ctx, fmt.Sprintf("location.replace(%s)", jsString(tokenURL)))

	// 阶段一：高频补跳，尽快离开鉴权拦截页（同站导航才会回传 Cookie）
	connected := false
	for i := 0; i < 10 && !connected; i++ {
		time.Sleep(200 * time.Millisecond)
		if i%2 == 1 {
			connected = a.runner.UIConnected()
		}
		a.reauthorize(tokenURL)
	}

	// 阶段二：等界面真正连上服务（每秒探测一次）
	deadline := time.Now().Add(12 * time.Second)
	for !connected && time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		connected = a.runner.UIConnected()
	}
	if !connected {
		a.appendLog("等待 Web UI 建立连接超时，仍然显示窗口")
	} else {
		a.appendLog("Web UI 已连接，准备显示窗口")
	}

	// 让首屏渲染出来，避免露出白屏
	time.Sleep(600 * time.Millisecond)
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	a.setTrayTooltip("DSH Desktop — DeepSeek Harness")
	a.appendLog("Web UI 已打开")
}

// reauthorize 只在当前页面确实是鉴权失败页时才补跳令牌地址，正常加载的 UI 不会被干扰。
// 注入的脚本自带防重入，同一个页面上只会跳一次。
func (a *App) reauthorize(tokenURL string) {
	if a.ctx == nil {
		return
	}
	js := fmt.Sprintf(
		`(function(){try{`+
			`if(window.__dshAuthFixing)return;`+
			`var b=document.body?document.body.innerText:'';`+
			`if(b&&b.indexOf('authentication required')>=0){`+
			`window.__dshAuthFixing=true;location.replace(%s);}}catch(e){}})()`,
		jsString(tokenURL),
	)
	runtime.WindowExecJS(a.ctx, js)
}

// jsString 把 Go 字符串转成安全的 JS 字符串字面量。
func jsString(s string) string {
	return strconv.Quote(s)
}

// watchRunner 监控 dsh 子进程，意外退出时把状态回退为错误，让用户能一键重启。
func (a *App) watchRunner(ch <-chan error) {
	if ch == nil {
		return
	}
	err := <-ch
	// 通道已被替换说明发生了重启，旧进程的退出无需上报
	if a.runner.Exited() != ch {
		return
	}
	if a.runner.IsStopped() {
		return
	}
	detail := "dsh web 进程已退出"
	if err != nil {
		detail = fmt.Sprintf("dsh web 进程已退出: %v", err)
	}
	a.appendLog("%s", detail)
	a.setStatus(stateError, detail+"，点击「重试」可重新启动")

	// 此时 WebView 还停在已经失效的 dsh 页面上，把它拉回启动页展示错误与重试按钮
	if a.ctx != nil {
		runtime.WindowReloadApp(a.ctx)
	}
}

// GetStatus 返回当前状态（splash 页面加载后主动拉取一次）。
func (a *App) GetStatus() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// GetLogs 返回启动日志。
func (a *App) GetLogs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.logs))
	copy(out, a.logs)
	return out
}

// RestartDSH 重启 dsh web 服务。
func (a *App) RestartDSH() Status {
	a.setStatus(stateStarting, "正在重启 dsh web ...")
	go a.bootstrap(true)
	return a.GetStatus()
}

// OpenInBrowser 用系统默认浏览器打开 Web UI。
func (a *App) OpenInBrowser() {
	if a.ctx == nil {
		return
	}
	a.mu.Lock()
	url := a.webURL
	a.mu.Unlock()
	runtime.BrowserOpenURL(a.ctx, url)
}

// Reload 重新加载 Web UI 页面。
func (a *App) Reload() {
	if a.ctx == nil {
		return
	}
	// 已经跳转到 dsh 页面时，重新加载当前地址
	runtime.WindowExecJS(a.ctx, "window.location.reload()")
}

// withCtx 在运行时上下文可用时执行回调（托盘菜单等场景会用到）。
func (a *App) withCtx(fn func(context.Context)) {
	if a.ctx == nil {
		return
	}
	fn(a.ctx)
}

// setStatus 更新状态并广播。
func (a *App) setStatus(state, message string) {
	a.mu.Lock()
	a.status.State = state
	a.status.Message = message
	a.status.URL = a.webURL
	snapshot := a.status
	a.mu.Unlock()

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, evtStatus, snapshot)
	}
}

// appendLog 记录一行日志：推送前端 + 落盘。
func (a *App) appendLog(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	writeFileLog(fmt.Sprintf(format, args...))

	a.mu.Lock()
	a.logs = append(a.logs, line)
	if len(a.logs) > a.maxLogs {
		a.logs = a.logs[len(a.logs)-a.maxLogs:]
	}
	a.mu.Unlock()

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, evtLog, line)
	}
}
