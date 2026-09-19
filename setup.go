package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// setupState 是向导的运行时状态。
// 用独立锁，避免与 App.mu（主状态锁）互相等待。
type setupState struct {
	mu       sync.Mutex
	probeMu  sync.Mutex // 串行化整轮检测，避免两处同时检测把步骤状态交错写乱
	steps    map[int]StepState
	nodes    []NodeInfo
	node     *NodeInfo
	dshs     []DshInfo
	dsh      *DshInfo
	port     int
	portInfo PortInfo
	busy     bool
	forced   bool
	cancel   context.CancelFunc
	lastErr  *SetupError
}

// initSetupState 把四步状态重置为「未开始」。
func (a *App) initSetupState() {
	s := &setupState{steps: map[int]StepState{}}
	for i := stepNode; i <= stepTotal; i++ {
		s.steps[i] = StepState{Step: i, Total: stepTotal, State: stPending, Title: stepTitle(i)}
	}
	a.setup = s
}

// setupStateOf 返回向导状态，必要时先初始化。
func (a *App) setupStateOf() *setupState {
	if a.setup == nil {
		a.initSetupState()
	}
	return a.setup
}

// inSetup 报告当前是否处于向导视图。
func (a *App) inSetup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status.State == stateSetup
}

// busyNow 报告是否有安装任务在跑。
func (a *App) busyNow() bool {
	if a.setup == nil {
		return false
	}
	a.setup.mu.Lock()
	defer a.setup.mu.Unlock()
	return a.setup.busy
}

// selectedNode 返回当前选定的 Node。
func (a *App) selectedNode() *NodeInfo {
	s := a.setupStateOf()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

// selectedDsh 返回当前选定的 dsh。
func (a *App) selectedDsh() *DshInfo {
	s := a.setupStateOf()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dsh
}

// stepIsOK 判断某一步是否已达到可继续的状态。
func (a *App) stepIsOK(step int) bool {
	s := a.setupStateOf()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps[step].State == stOK || s.steps[step].State == stDone
}

// setStep 更新单步状态并广播。
func (a *App) setStep(step int, state, message, detail string, canSkip bool) {
	s := a.setupStateOf()

	s.mu.Lock()
	st := s.steps[step]
	st.Step = step
	st.Total = stepTotal
	st.Title = stepTitle(step)
	st.State = state
	st.Message = message
	st.Detail = detail
	st.CanSkip = canSkip
	s.steps[step] = st
	// 步骤转好说明上一次的失败已经翻篇
	if state == stOK || state == stDone {
		s.lastErr = nil
	}
	s.mu.Unlock()

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, evtSetupStep, st)
	}
}

// failStep 记录一次失败：既更新步骤状态，也把结构化错误留给前端。
func (a *App) failStep(step int, e *SetupError) {
	s := a.setupStateOf()
	s.mu.Lock()
	s.lastErr = e
	s.mu.Unlock()

	a.setStep(step, stError, e.Message, e.Hint, false)
}

// snapshot 汇总向导当前状态。
func (a *App) snapshot() Snapshot {
	s := a.setupStateOf()

	s.mu.Lock()
	steps := make([]StepState, 0, stepTotal)
	for i := stepNode; i <= stepTotal; i++ {
		st := s.steps[i]
		st.Step, st.Total, st.Title = i, stepTotal, stepTitle(i)
		steps = append(steps, st)
	}
	node, nodes := s.node, s.nodes
	dsh, dshs := s.dsh, s.dshs
	port, pi, busy, forced := s.port, s.portInfo, s.busy, s.forced
	lastErr := s.lastErr
	s.mu.Unlock()

	if port == 0 {
		port, _ = a.cfg.ResolvedPort()
	}
	if pi.Port != port {
		pi = PortInfo{Port: port, Message: "尚未检测"}
	}
	_, locked := a.cfg.ResolvedPort()

	return Snapshot{
		Required:    !a.cfg.SetupCompleted || forced,
		Steps:       steps,
		Node:        node,
		NodeChoices: nodes,
		Dsh:         dsh,
		DshChoices:  dshs,
		Port:        pi,
		PortFixed:   locked,
		Busy:        busy,
		Err:         lastErr,
	}
}

// emitSnapshot 把完整快照推给前端。
func (a *App) emitSnapshot() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, evtSetupSnapshot, a.snapshot())
}

// emitProgress 推送进度。
func (a *App) emitProgress(p Progress) {
	if a.ctx == nil {
		return
	}
	if p.Note != "" && p.Phase == "npm" {
		a.appendLog("%s", p.Note)
	}
	runtime.EventsEmit(a.ctx, evtSetupProgress, p)
}

// opOK 构造成功结果。
func (a *App) opOK() OpResult { return OpResult{OK: true, Snapshot: a.snapshot()} }

// opErr 构造失败结果。
func (a *App) opErr(e *SetupError) OpResult {
	return OpResult{OK: false, Err: e, Snapshot: a.snapshot()}
}

// ---------------------------------------------------------------- 检测

// recheck 执行检测。step 为 0 表示全部重检。
//
// 全程持有 probeMu：启动时的自动检测和前端主动触发的重检可能同时发生，
// 不加锁会让两轮检测的步骤状态互相交错（一边刚置 ok，另一边又置回 checking）。
func (a *App) recheck(step int) {
	s := a.setupStateOf()
	s.probeMu.Lock()
	defer s.probeMu.Unlock()

	if step <= 0 || step == stepNode {
		a.recheckNode()
	}
	if step <= 0 || step == stepDsh {
		a.recheckDsh()
	}
	if step <= 0 || step == stepPort {
		a.recheckPort()
	}
	if step <= 0 || step == stepRun {
		a.recheckRun()
	}
}

// recheckNode 枚举全部候选 Node 并选定一个。
func (a *App) recheckNode() {
	a.setStep(stepNode, stChecking, "正在扫描 Node …", "", false)

	nodes := probeNodes(a.cfg)

	s := a.setupStateOf()
	s.mu.Lock()
	prev := s.node
	s.nodes = nodes
	s.node = nil
	// 尽量保持用户上次的选择
	if prev != nil {
		for i := range nodes {
			if nodes[i].OK && strings.EqualFold(nodes[i].Path, prev.Path) {
				s.node = &nodes[i]
				break
			}
		}
	}
	if s.node == nil {
		for i := range nodes {
			if nodes[i].OK {
				s.node = &nodes[i]
				break
			}
		}
	}
	s.mu.Unlock()

	sel := a.selectedNode()
	if sel == nil {
		a.setStep(stepNode, stMissing,
			fmt.Sprintf("未找到可用的 Node.js（需要 %d.%d 或更高）", minNodeMajor, minNodeMinor),
			nodeDetail(nodes), false)
		return
	}
	a.setStep(stepNode, stOK, fmt.Sprintf("已选用 Node v%s", sel.Version), sel.Path, true)
}

// recheckDsh 枚举全部候选 dsh 并选定一个。
func (a *App) recheckDsh() {
	a.setStep(stepDsh, stChecking, "正在检测 dsh …", "", false)

	s := a.setupStateOf()
	s.mu.Lock()
	nodes := append([]NodeInfo{}, s.nodes...)
	prev := s.dsh
	s.mu.Unlock()

	node := a.selectedNode()
	if node == nil {
		a.setStep(stepDsh, stMissing, "需要先选定一个可用的 Node", "", false)
		return
	}

	dshs := probeDshs(nodes, a.cfg)

	s.mu.Lock()
	s.dshs = dshs
	s.dsh = nil
	if prev != nil {
		for i := range dshs {
			if dshs[i].OK && strings.EqualFold(dshs[i].Bin, prev.Bin) {
				s.dsh = &dshs[i]
				break
			}
		}
	}
	if s.dsh == nil {
		for i := range dshs {
			if dshs[i].OK {
				s.dsh = &dshs[i]
				break
			}
		}
	}
	s.mu.Unlock()

	sel := a.selectedDsh()
	if sel == nil {
		msg := "未找到可用的 dsh"
		if len(dshs) > 0 {
			msg = "找到了 dsh，但它跑不起来"
		}
		a.setStep(stepDsh, stMissing, msg, dshDetail(dshs), false)
		return
	}

	// 尽量让第 1、2 步指向同一套环境
	a.adoptDshNode(sel)

	nodeNow := a.selectedNode()
	detail := sel.Bin
	if nodeNow != nil && sel.Node != "" && !strings.EqualFold(sel.Node, nodeNow.Path) {
		detail = fmt.Sprintf("%s\n注意：它实际使用的是 %s，与第 1 步选定的不是同一个", sel.Bin, sel.Node)
	}
	a.setStep(stepDsh, stOK, fmt.Sprintf("已选用 dsh %s", sel.Version), detail, true)
}

// adoptDshNode 把第 1 步选定的 Node 对齐到 dsh 实际使用的那个。
//
// 机器上常有多个 node，其中只有一个是 dsh 真正装在其前缀下的。自动选中那一个，
// 可以让两步指向同一套环境，也避免给出没有实际意义的「两者不匹配」提示。
func (a *App) adoptDshNode(d *DshInfo) {
	if d == nil || d.Node == "" {
		return
	}

	s := a.setupStateOf()
	s.mu.Lock()
	switched := false
	for i := range s.nodes {
		if s.nodes[i].OK && strings.EqualFold(s.nodes[i].Path, d.Node) {
			if s.node == nil || !strings.EqualFold(s.node.Path, d.Node) {
				s.node = &s.nodes[i]
				switched = true
			}
			break
		}
	}
	var adopted *NodeInfo
	if switched {
		adopted = s.node
	}
	s.mu.Unlock()

	if adopted != nil {
		a.setStep(stepNode, stOK, fmt.Sprintf("已选用 Node v%s", adopted.Version), adopted.Path, true)
	}
}

// recheckPort 检测当前端口。
func (a *App) recheckPort() {
	a.setStep(stepPort, stChecking, "正在检查端口 …", "", false)

	port, _ := a.cfg.ResolvedPort()
	s := a.setupStateOf()
	s.mu.Lock()
	if s.port != 0 {
		port = s.port
	}
	s.mu.Unlock()

	a.applyPortResult(port, probePort(port))
}

// applyPortResult 记录并展示端口检测结果。
func (a *App) applyPortResult(port int, pi PortInfo) {
	s := a.setupStateOf()
	s.mu.Lock()
	s.port = port
	s.portInfo = pi
	s.mu.Unlock()

	switch {
	case pi.Invalid:
		a.setStep(stepPort, stError, pi.Message, "", false)
	case !pi.Free:
		a.setStep(stepPort, stError, pi.Message, "可以换一个端口，或点「结束该进程」", false)
	default:
		a.setStep(stepPort, stOK, pi.Message, "", true)
	}
}

// recheckRun 汇总前三步，决定能否进入启动。
func (a *App) recheckRun() {
	if !a.stepIsOK(stepNode) || !a.stepIsOK(stepDsh) || !a.stepIsOK(stepPort) {
		a.setStep(stepRun, stPending, "等待前面的步骤完成", "", false)
		return
	}
	a.setStep(stepRun, stOK, "环境已就绪，可以启动", "", true)
}

// enterSetup 切到向导视图并做一次全量检测。
func (a *App) enterSetup() {
	a.setStatus(stateSetup, "正在检测运行环境 ...")
	a.recheck(0)
	a.emitSnapshot()
}

// reopenSetup 由托盘「环境设置」触发：先结束当前 dsh，再回到向导。
// 必须先停 dsh，否则第 3 步会把「我们自己占着的端口」报成冲突。
func (a *App) reopenSetup() {
	if a.runner != nil && a.runner.IsRunning() {
		a.appendLog("进入环境设置，先结束当前的 dsh web")
		a.runner.Stop()
		time.Sleep(400 * time.Millisecond)
	}

	s := a.setupStateOf()
	s.mu.Lock()
	s.forced = true
	s.port = 0
	s.mu.Unlock()

	// 先把状态切成向导再刷新页面，否则页面重载后拉到的可能还是旧状态，会显示成启动页
	a.setStatus(stateSetup, "正在检测运行环境 ...")
	if a.ctx != nil {
		runtime.WindowReloadApp(a.ctx)
		a.showWindow()
	}

	a.recheck(0)
	a.emitSnapshot()
}

// nodeDetail 生成 Node 候选的补充说明。
func nodeDetail(nodes []NodeInfo) string {
	if len(nodes) == 0 {
		return "未在 PATH 与常见安装目录中发现 node.exe，可用「自动安装」装一个便携版"
	}
	var parts []string
	for i, n := range nodes {
		if i >= 3 {
			break
		}
		ver := n.Version
		if ver == "" {
			ver = "未知版本"
		}
		if n.OK {
			parts = append(parts, fmt.Sprintf("%s（v%s，可用）", n.Path, ver))
		} else {
			parts = append(parts, fmt.Sprintf("%s（v%s，%s）", n.Path, ver, n.Reason))
		}
	}
	return strings.Join(parts, "；")
}

// dshDetail 生成 dsh 候选的补充说明。
func dshDetail(dshs []DshInfo) string {
	if len(dshs) == 0 {
		return "未在 PATH、各 npm 前缀与应用目录中发现 dsh，可用「自动安装」"
	}
	var parts []string
	for i, d := range dshs {
		if i >= 3 {
			break
		}
		if d.OK {
			parts = append(parts, fmt.Sprintf("%s（%s，可用）", d.Bin, d.Version))
		} else {
			parts = append(parts, fmt.Sprintf("%s（%s）", d.Bin, d.Reason))
		}
	}
	return strings.Join(parts, "；")
}

// dshInstallPrefix 决定把 dsh 装到哪个 npm 前缀，遵循「全机只留一份」：
// 用户的 node 就复用它的前缀（与其它全局包同处），只有应用自装的便携版才用应用私有目录。
func (a *App) dshInstallPrefix(node *NodeInfo) string {
	if node == nil {
		return runtimeNpmGlobal()
	}
	if node.Source == srcManaged || node.Prefix == "" {
		return runtimeNpmGlobal()
	}
	return node.Prefix
}

// chooseFile 打开系统文件选择框。
func (a *App) chooseFile(title string, filters []runtime.FileFilter) (string, error) {
	if a.ctx == nil {
		return "", errors.New("运行时尚未就绪")
	}
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   title,
		Filters: filters,
	})
}

// ---------------------------------------------------------------- 后端方法

// GetSetupSnapshot 返回向导完整快照。
func (a *App) GetSetupSnapshot() OpResult { return a.opOK() }

// RecheckSetup 重新检测指定步骤，step 为 0 表示全部重检。
func (a *App) RecheckSetup(step int) OpResult {
	if a.busyNow() {
		return a.opErr(errBusy())
	}
	a.recheck(step)
	return a.opOK()
}

// UseNode 在候选列表里选定一个 Node。
func (a *App) UseNode(index int) OpResult {
	s := a.setupStateOf()

	s.mu.Lock()
	if index < 0 || index >= len(s.nodes) {
		s.mu.Unlock()
		return a.opErr(newSetupError("E_NODE_MISSING", "候选序号无效", "请重新检测后再选", ""))
	}
	chosen := s.nodes[index]
	s.node = &s.nodes[index]
	s.mu.Unlock()

	if !chosen.OK {
		a.setStep(stepNode, stMissing, "该 Node 不可用："+chosen.Reason, chosen.Path, false)
	} else {
		a.setStep(stepNode, stOK, fmt.Sprintf("已选用 Node v%s", chosen.Version), chosen.Path, true)
	}
	// Node 换了，dsh 的配对关系要重算
	a.recheck(stepDsh)
	a.recheckRun()
	return a.opOK()
}

// PickNodeFile 让用户手动指定 node.exe。
func (a *App) PickNodeFile() OpResult {
	path, err := a.chooseFile("选择 node.exe", []runtime.FileFilter{
		{DisplayName: "Node 运行时 (node.exe)", Pattern: "node.exe"},
		{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
	})
	if err != nil || strings.TrimSpace(path) == "" {
		return a.opOK() // 用户取消，不当作错误
	}

	info, ok := probeOneNode(path, srcPicked)
	if !ok {
		a.setStep(stepNode, stMissing, "该文件不可用："+info.Reason, info.Path, false)
		return a.opErr(newSetupError("E_NODE_BROKEN", "该文件不可用："+info.Reason,
			fmt.Sprintf("请选择 node.exe，且版本不低于 %d.%d", minNodeMajor, minNodeMinor), ""))
	}

	s := a.setupStateOf()
	s.mu.Lock()
	s.nodes = append([]NodeInfo{info}, s.nodes...)
	s.node = &s.nodes[0]
	s.mu.Unlock()

	a.setStep(stepNode, stOK, fmt.Sprintf("已选用 Node v%s", info.Version), info.Path+"（手动指定）", true)
	a.recheck(stepDsh)
	a.recheckRun()
	return a.opOK()
}

// InstallNode 下载并安装便携版 Node（异步，进度通过事件推送）。
func (a *App) InstallNode() OpResult {
	s := a.setupStateOf()

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return a.opErr(errBusy())
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, downloadTimeout)
	s.busy = true
	s.cancel = cancel
	s.mu.Unlock()

	a.setStep(stepNode, stInstalling, "正在下载并安装 Node …", "", false)
	a.appendLog("开始安装便携版 Node（下载源 %s）", nodeMirrors()[0])

	go func() {
		defer cancel()

		info, err := installPortableNode(ctx, func(p Progress) { a.emitProgress(p) })

		s.mu.Lock()
		s.busy = false
		s.cancel = nil
		s.mu.Unlock()

		switch {
		case err == nil:
			s.mu.Lock()
			s.nodes = append([]NodeInfo{info}, s.nodes...)
			s.node = &s.nodes[0]
			s.mu.Unlock()
			a.appendLog("Node v%s 已安装到 %s", info.Version, info.Path)
			a.recheck(stepDsh)
			a.recheckRun()
		case errors.Is(err, context.Canceled):
			a.setStep(stepNode, stMissing, "安装已取消", "可以重新点「自动安装」", false)
		case errors.Is(err, errChecksum):
			a.failStep(stepNode, newSetupError("E_SHA_MISMATCH", "安装包校验失败",
				"下载的文件不完整或被篡改，请重试", ""))
		default:
			a.failStep(stepNode, newSetupError("E_NET", "Node 安装失败",
				err.Error(), ""))
		}
		a.emitSnapshot()
	}()

	return a.opOK()
}

// UseDsh 在候选列表里选定一个 dsh。
func (a *App) UseDsh(index int) OpResult {
	s := a.setupStateOf()

	s.mu.Lock()
	if index < 0 || index >= len(s.dshs) {
		s.mu.Unlock()
		return a.opErr(newSetupError("E_DSH_MISSING", "候选序号无效", "请重新检测后再选", ""))
	}
	chosen := s.dshs[index]
	s.dsh = &s.dshs[index]
	s.mu.Unlock()

	if !chosen.OK {
		a.setStep(stepDsh, stMissing, "该 dsh 不可用："+chosen.Reason, chosen.Bin, false)
	} else {
		a.setStep(stepDsh, stOK, fmt.Sprintf("已选用 dsh %s", chosen.Version), chosen.Bin, true)
	}
	a.recheckRun()
	return a.opOK()
}

// PickDshFile 让用户手动指定 dsh 入口（bin.js 或 dsh.cmd）。
func (a *App) PickDshFile() OpResult {
	path, err := a.chooseFile("选择 dsh 入口（bin.js 或 dsh.cmd）", []runtime.FileFilter{
		{DisplayName: "dsh 入口 (*.js;*.cmd;*.bat;*.exe)", Pattern: "*.js;*.cmd;*.bat;*.exe"},
		{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
	})
	if err != nil || strings.TrimSpace(path) == "" {
		return a.opOK()
	}

	node := a.selectedNode()
	nodePath := ""
	if node != nil {
		nodePath = node.Path
	}
	info, ok := probeOneDsh(nodePath, path, kindOfDshBin(path), srcPicked, "")
	if !ok {
		a.setStep(stepDsh, stMissing, "该入口不可用："+info.Reason, info.Bin, false)
		return a.opErr(newSetupError("E_DSH_BROKEN", "该入口不可用："+info.Reason,
			"请选择 dsh 的 lib\\bin.js 或 dsh.cmd，并确认它能在当前 Node 下运行", ""))
	}

	s := a.setupStateOf()
	s.mu.Lock()
	s.dshs = append([]DshInfo{info}, s.dshs...)
	s.dsh = &s.dshs[0]
	s.mu.Unlock()

	a.setStep(stepDsh, stOK, fmt.Sprintf("已选用 dsh %s", info.Version), info.Bin+"（手动指定）", true)
	a.recheckRun()
	return a.opOK()
}

// InstallDsh 用配对的 node + npm 安装 dsh（异步，进度通过事件推送）。
func (a *App) InstallDsh() OpResult {
	node := a.selectedNode()
	if node == nil || !node.OK {
		return a.opErr(newSetupError("E_NODE_MISSING", "Node 尚未就绪", "请先回到第 1 步完成 Node 检测", ""))
	}
	if node.NpmCLI == "" {
		return a.opErr(newSetupError("E_NPM_NOT_FOUND", "该 Node 未附带 npm",
			"换一个 Node，或用「自动安装」装一个便携版", ""))
	}

	prefix := a.dshInstallPrefix(node)
	s := a.setupStateOf()

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return a.opErr(errBusy())
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	s.busy = true
	s.cancel = cancel
	s.mu.Unlock()

	a.setStep(stepDsh, stInstalling, "正在安装 dsh …", prefix, false)
	a.appendLog("开始安装 dsh：%s %s（prefix=%s）", node.Path, node.NpmCLI, prefix)

	go func() {
		defer cancel()

		err := installDshWith(ctx, *node, prefix, func(p Progress) { a.emitProgress(p) })

		s.mu.Lock()
		s.busy = false
		s.cancel = nil
		s.mu.Unlock()

		switch {
		case err == nil:
			a.appendLog("dsh 安装完成")
			a.recheck(stepDsh)
			a.recheckRun()
		case errors.Is(err, context.Canceled):
			a.setStep(stepDsh, stMissing, "安装已取消", "可以重新点「自动安装」", false)
		case errors.Is(err, context.DeadlineExceeded):
			a.failStep(stepDsh, newSetupError("E_NET", "安装超时",
				"网络较慢，请重试，或复制下面的命令自行安装",
				manualInstallCommand(node.Path, node.NpmCLI, prefix)))
		default:
			a.failStep(stepDsh, newSetupError("E_NPM_FAIL", "dsh 安装失败", err.Error(),
				manualInstallCommand(node.Path, node.NpmCLI, prefix)))
		}
		a.emitSnapshot()
	}()

	return a.opOK()
}

// CheckPort 校验端口并返回占用详情。
func (a *App) CheckPort(port int) OpResult {
	a.applyPortResult(port, probePort(port))
	a.recheckRun()
	return a.opOK()
}

// AutoPickPort 从当前端口往后找一个空闲端口。
func (a *App) AutoPickPort() OpResult {
	start, _ := a.cfg.ResolvedPort()

	s := a.setupStateOf()
	s.mu.Lock()
	if s.port != 0 {
		start = s.port
	}
	s.mu.Unlock()

	p := pickFreePort(start, portScanMax)
	if p == 0 {
		return a.opErr(newSetupError("E_PORT_IN_USE",
			fmt.Sprintf("%d–%d 之间没有找到空闲端口", start, portScanMax), "请手动指定一个端口", ""))
	}
	return a.CheckPort(p)
}

// KillPort 结束占用指定端口的进程。
func (a *App) KillPort(port int) OpResult {
	pids := listeningPIDs(port)
	if len(pids) == 0 {
		return a.opErr(newSetupError("E_PORT_IN_USE",
			fmt.Sprintf("端口 %d 当前没有被监听", port), "可以直接点「下一步」继续", ""))
	}
	for _, pid := range pids {
		a.appendLog("正在结束占用端口 %d 的进程（pid=%d）", port, pid)
		killTree(pid)
	}
	time.Sleep(600 * time.Millisecond)
	return a.CheckPort(port)
}

// CancelInstall 取消正在进行的安装。
func (a *App) CancelInstall() OpResult {
	s := a.setupStateOf()
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()

	if cancel == nil {
		return a.opOK()
	}
	cancel()
	a.appendLog("用户取消了安装任务")
	return a.opOK()
}

// FinishSetup 写配置并进入启动流程（步骤 4）。
func (a *App) FinishSetup(port int) OpResult {
	node := a.selectedNode()
	if node == nil || !node.OK {
		return a.opErr(newSetupError("E_NODE_MISSING", "Node 尚未就绪", "请回到第 1 步完成检测", ""))
	}
	dsh := a.selectedDsh()
	if dsh == nil || !dsh.OK {
		return a.opErr(newSetupError("E_DSH_MISSING", "dsh 尚未就绪", "请回到第 2 步完成检测或安装", ""))
	}

	pi := probePort(port)
	if !pi.Free {
		return a.opErr(newSetupError("E_PORT_IN_USE", pi.Message, "换一个端口，或结束占用该端口的进程", ""))
	}

	a.cfg.SetupCompleted = true
	a.cfg.NodePath = node.Path
	a.cfg.NodeVersion = node.Version
	a.cfg.NodeSource = node.Source
	a.cfg.NpmPrefix = node.Prefix
	a.cfg.DshBin = dsh.Bin
	a.cfg.DshKind = dsh.Kind
	a.cfg.DshVersion = dsh.Version
	a.cfg.DshSource = dsh.Source
	a.cfg.Port = port
	a.cfg.Mirror = "npmmirror"
	a.cfg.LastCheckAt = time.Now().Format(time.RFC3339)

	if err := a.cfg.Save(); err != nil {
		// 写盘失败不阻断：内存里已经生效，本次照样能用
		a.appendLog("配置写入失败（本次会话仍然生效）: %v", err)
		a.emitSnack("配置未能写入磁盘，本次可以正常使用，但下次启动需要重新检测。")
	}

	s := a.setupStateOf()
	s.mu.Lock()
	s.forced = false
	s.port = port
	s.mu.Unlock()

	a.setStep(stepRun, stDone, "正在启动 dsh web …", "", false)

	// 端口变了必须重建 runner，否则还会按旧端口拉起
	if port != a.runner.Port() {
		a.runner.Stop()
		a.runner = newDshRunner(port, func(line string) { a.appendLog("%s", line) })
		a.mu.Lock()
		a.webURL = a.runner.RootURL()
		a.mu.Unlock()
	}
	a.runner.SetPreferred(a.cfg.NodePath, a.cfg.DshBin, a.cfg.DshKind)

	go a.bootstrap(false)
	return a.opOK()
}

// ResetSetup 回到第 1 步重新检测（供「环境设置」重跑）。
func (a *App) ResetSetup() OpResult {
	s := a.setupStateOf()
	s.mu.Lock()
	s.forced = true
	s.port = 0
	s.mu.Unlock()

	a.enterSetup()
	return a.opOK()
}

// ManualCommand 返回当前环境对应的兜底手动安装命令。
func (a *App) ManualCommand() string {
	node := a.selectedNode()
	if node == nil {
		return manualInstallCommand("", "", "")
	}
	return manualInstallCommand(node.Path, node.NpmCLI, a.dshInstallPrefix(node))
}

// emitSnack 推送一条提示性消息（复用日志通道，前端在向导里以提示条展示）。
func (a *App) emitSnack(message string) {
	a.appendLog("%s", message)
}
