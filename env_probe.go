package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// dshBinRel 是 dsh 的入口在 npm 前缀下的相对路径。
var dshBinRel = filepath.Join("node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")

// npmCLIRel 是 npm 的入口相对 node 目录的路径。
var npmCLIRel = filepath.Join("node_modules", "npm", "bin", "npm-cli.js")

// versionPattern 用于从命令输出里抠出版本号。
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?`)

// probeTimeout 是单次探测命令的超时。任何探测都不允许把向导卡住。
const probeTimeout = 3 * time.Second

// probeCmd 执行一次探测命令并返回合并后的输出。
// 走 createNoWindow，避免 GUI 程序里闪出黑窗。
func probeCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
		HideWindow:    true,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// nodeVersion 执行 node -v，返回不带前缀 v 的版本号。
func nodeVersion(exe string) (string, error) {
	out, err := probeCmd(probeTimeout, exe, "-v")
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	v := strings.TrimPrefix(strings.TrimSpace(out), "v")
	if m := versionPattern.FindString(v); m != "" {
		return m, nil
	}
	return "", fmt.Errorf("无法从输出中解析版本号：%q", truncate(out, 120))
}

// nodeVersionStable 跑 node -v，首次失败再试一次。
//
// 单次执行失败未必代表这个 Node 不可用 —— 杀毒软件扫描、磁盘抖动、首次运行被拦
// 都可能让它偶发地以一个非零码退出且没有任何输出。直接据此判定会把可用的 Node
// 判成不可用，进而把用户往「重装一个」的错误方向推，所以这里必须重试。
func nodeVersionStable(exe string) (string, error) {
	ver, err := nodeVersion(exe)
	if err == nil {
		return ver, nil
	}
	time.Sleep(150 * time.Millisecond)
	if ver2, err2 := nodeVersion(exe); err2 == nil {
		return ver2, nil
	}
	return "", err
}

// truncate 把过长的输出裁短，避免把一整屏错误塞进界面。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// nodeVersionUsable 判断版本号是否满足 dsh 的硬下限（≥ 22.14）。
func nodeVersionUsable(ver string) bool {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major != minNodeMajor {
		return major > minNodeMajor
	}
	return minor >= minNodeMinor
}

// probeImportMetaMain 是判定 Node 能不能跑 dsh 的运行时探针。
//
// dsh 的 bin.js 使用 import.meta.main，该语法 Node 22.14 才加入；低于此版本时 dsh 会
// 零输出静默退出，光看版本号容易放过 22.0~22.13 这类「看着是 22.x 但不可用」的版本。
func probeImportMetaMain(exe string) (bool, error) {
	out, err := probeCmd(probeTimeout, exe, "--input-type=module", "-e", "console.log(import.meta.main)")
	if err != nil && strings.TrimSpace(out) == "" {
		return false, err
	}
	if strings.TrimSpace(out) != "true" {
		return false, fmt.Errorf("探针输出为 %q，期望 true", truncate(out, 120))
	}
	return true, nil
}

// npmCLIOf 返回该 node 自带的 npm-cli.js 路径，没有则返回空串。
// 用「配对的 node + npm-cli.js」直接调，可以绕开 PATH 里指向别的 node 的裸 npm。
func npmCLIOf(exe string) string {
	cand := filepath.Join(filepath.Dir(exe), npmCLIRel)
	if fileExists(cand) {
		return cand
	}
	return ""
}

// npmPrefixOf 反查该 node 对应的 npm 全局前缀。
// Windows 上 npm 的内置前缀就是 node.exe 所在目录，因此优先取同目录；
// 若用户显式配置了 npm_config_prefix 则以它为准。
func npmPrefixOf(exe string) string {
	if p := strings.TrimSpace(os.Getenv("npm_config_prefix")); p != "" {
		return filepath.Clean(p)
	}
	return filepath.Dir(exe)
}

// pathNodeCandidates 返回 PATH 中所有 node.exe。
//
// 关键：这里绝不能改用 exec.LookPath —— 它只返回第一个命中项，而用户机器上
// 可能同时存在多个 node（例如某个工具链自带的 node 排在 PATH 更前面），
// 只取第一个会得出「node 在 A、dsh 在 B」的错乱结论。
func pathNodeCandidates() []string {
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "node.exe"))
	}
	return out
}

// scannedNodeDirs 返回除 PATH 之外还要额外检查的常见安装目录。
// 覆盖官方安装器、nvm-for-windows、Volta、fnm 以及应用自己的 runtime 目录。
func scannedNodeDirs() []string {
	var dirs []string
	push := func(d string) {
		if strings.TrimSpace(d) != "" {
			dirs = append(dirs, d)
		}
	}

	if pf := os.Getenv("ProgramFiles"); pf != "" {
		push(filepath.Join(pf, "nodejs"))
	}
	if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
		push(filepath.Join(pf86, "nodejs"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		push(filepath.Join(la, "Programs", "nodejs"))
		push(filepath.Join(la, "Volta", "bin"))
		push(filepath.Join(la, "fnm_multishells"))
	}
	if ad := os.Getenv("APPDATA"); ad != "" {
		push(filepath.Join(ad, "nvm", "current"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		push(filepath.Join(home, ".local", "bin"))
	}
	push(runtimeNodeDir())

	return dirs
}

// probeOneNode 校验单个 node.exe，返回候选信息与是否合格。
func probeOneNode(exe, source string) (NodeInfo, bool) {
	info := NodeInfo{Path: filepath.Clean(exe), Source: source}

	if !fileExists(info.Path) {
		info.Reason = "文件不存在"
		return info, false
	}

	ver, verErr := nodeVersionStable(info.Path)
	if verErr != nil {
		// 把底层错误原样带出来，否则「无法执行」这种笼统提示会把排查带偏
		info.Reason = "无法执行：" + truncate(verErr.Error(), 160)
		return info, false
	}
	info.Version = ver

	if !nodeVersionUsable(ver) {
		info.Reason = fmt.Sprintf("v%s 低于 %d.%d，dsh 会零输出静默退出", ver, minNodeMajor, minNodeMinor)
		return info, false
	}
	if ok, probeErr := probeImportMetaMain(info.Path); !ok {
		if probeErr != nil {
			info.Reason = "运行时探针失败：" + truncate(probeErr.Error(), 160)
		} else {
			info.Reason = fmt.Sprintf("缺少 import.meta.main 支持（需 Node %d.%d 以上）", minNodeMajor, minNodeMinor)
		}
		return info, false
	}

	info.OK = true
	info.Prefix = npmPrefixOf(info.Path)
	info.NpmCLI = npmCLIOf(info.Path)
	if info.NpmCLI == "" {
		info.Reason = "该 Node 未附带 npm，无法用它安装 dsh（仍可用它运行已有的 dsh）"
	}
	return info, true
}

// sourceRank 用于给候选排序：越小的来源越可信，优先展示与选用。
func sourceRank(source string) int {
	switch source {
	case srcManaged:
		return 0
	case srcExisting:
		return 1
	case "config":
		return 1
	default:
		return 2
	}
}

// probeNodes 枚举所有候选 Node，合格的排在前面。
func probeNodes(cfg Config) []NodeInfo {
	type candidate struct{ path, source string }

	var cands []candidate
	seen := map[string]bool{}
	add := func(p, source string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		clean := filepath.Clean(p)
		key := strings.ToLower(clean)
		if seen[key] {
			return
		}
		seen[key] = true
		cands = append(cands, candidate{path: clean, source: source})
	}

	// 1) 应用自管的便携版
	add(filepath.Join(runtimeNodeDir(), "node.exe"), srcManaged)
	// 2) 上次配置记录的位置
	add(cfg.NodePath, srcExisting)
	// 3) PATH 中的每一个 node
	for _, p := range pathNodeCandidates() {
		add(p, "path")
	}
	// 4) 常见安装目录
	for _, dir := range scannedNodeDirs() {
		add(filepath.Join(dir, "node.exe"), "scan")
	}

	var out []NodeInfo
	for _, c := range cands {
		// 不存在就直接跳过，避免拿一堆无效路径把界面塞满
		if !fileExists(c.path) {
			continue
		}
		info, _ := probeOneNode(c.path, c.source)
		out = append(out, info)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return out[i].OK
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

// probeOneDsh 校验单个 dsh 入口能否真的跑起来，并读出它的版本号。
//
// 判定标准是「能跑通」而不是「文件存在」：文件在但跑不起来的情况
// （shim 坏掉、与所选 node 不匹配、原生模块缺失）只有实际执行一次才能发现。
func probeOneDsh(node, bin, kind, source, prefix string) (DshInfo, bool) {
	info := DshInfo{
		Bin:    filepath.Clean(bin),
		Kind:   kind,
		Node:   node,
		Source: source,
		Prefix: prefix,
	}
	if !fileExists(info.Bin) {
		info.Reason = "文件不存在"
		return info, false
	}
	if kind == kindJS && (node == "" || !fileExists(node)) {
		info.Reason = "缺少配对的 node，无法运行 bin.js"
		return info, false
	}

	exe, prefixArgs := dshInvocation(info.Node, info.Bin, info.Kind)
	args := append(append([]string{}, prefixArgs...), "--version")
	out, err := probeCmd(5*time.Second, exe, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		info.Reason = "无法执行，可能缺少原生依赖或与所选 Node 不匹配"
		return info, false
	}
	if ver := versionPattern.FindString(out); ver != "" {
		info.Version = ver
	}
	if info.Version == "" {
		info.Reason = "命令没有输出版本号，可能已损坏"
		return info, false
	}

	info.OK = true
	return info, true
}

// kindOfDshBin 根据扩展名判断 dsh 入口类型。
func kindOfDshBin(bin string) string {
	if strings.EqualFold(filepath.Ext(bin), ".js") {
		return kindJS
	}
	return kindCmd
}

// nodeForPrefix 找出属于某个 npm 前缀的 node，找不到时返回兜底 node。
func nodeForPrefix(nodes []NodeInfo, prefix, fallback string) string {
	if prefix != "" {
		for _, n := range nodes {
			if n.Prefix != "" && strings.EqualFold(n.Prefix, prefix) {
				return n.Path
			}
		}
	}
	return fallback
}

// probeDshs 枚举所有候选 dsh 安装。
// 顺序体现可信度：配置记录 > 应用自管 > DSH_BIN > PATH > 各 node 前缀。
func probeDshs(nodes []NodeInfo, cfg Config) []DshInfo {
	type candidate struct{ bin, source, prefix string }

	var cands []candidate
	seen := map[string]bool{}
	add := func(bin, source, prefix string) {
		if strings.TrimSpace(bin) == "" {
			return
		}
		clean := filepath.Clean(bin)
		key := strings.ToLower(clean)
		if seen[key] {
			return
		}
		seen[key] = true
		cands = append(cands, candidate{bin: clean, source: source, prefix: prefix})
	}

	fallback := ""
	for _, n := range nodes {
		if n.OK {
			fallback = n.Path
			break
		}
	}

	// 1) 上次配置记录的入口
	add(cfg.DshBin, "config", cfg.NpmPrefix)
	// 2) 应用自管前缀
	add(filepath.Join(runtimeNpmGlobal(), dshBinRel), srcManaged, runtimeNpmGlobal())
	// 3) DSH_BIN 逃生口
	add(strings.TrimSpace(os.Getenv("DSH_BIN")), "env", "")
	// 4) PATH 中的 dsh。
	// 同一个安装目录下往往同时存在 bin.js 与 dsh.cmd（后者是 npm 生成的转发脚本），
	// 它们其实是同一份安装，所以优先登记 bin.js（可读版本、可精确调用），命中就不再登记 cmd。
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		jsPath := filepath.Join(dir, dshBinRel)
		if fileExists(jsPath) {
			add(jsPath, "path", dir)
			continue
		}
		for _, ext := range []string{".cmd", ".exe", ".bat"} {
			add(filepath.Join(dir, "dsh"+ext), "path", dir)
		}
	}
	// 5) 每个候选 node 的前缀下
	for _, n := range nodes {
		if n.Prefix != "" {
			add(filepath.Join(n.Prefix, dshBinRel), "prefix", n.Prefix)
		}
	}

	var out []DshInfo
	for _, c := range cands {
		if !fileExists(c.bin) {
			continue
		}
		node := nodeForPrefix(nodes, c.prefix, fallback)
		info, _ := probeOneDsh(node, c.bin, kindOfDshBin(c.bin), c.source, c.prefix)
		out = append(out, info)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return out[i].OK
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

// probePort 探测端口是否可用，被占用时给出占用进程。
func probePort(port int) PortInfo {
	info := PortInfo{Port: port}

	if port < minWizardPort || !portInRange(port) {
		info.Invalid = true
		info.Message = fmt.Sprintf("端口需在 %d–65535 之间", minWizardPort)
		return info
	}

	// 先尝试绑定：能绑上就是空闲的
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		_ = ln.Close()
		info.Free = true
		info.Message = fmt.Sprintf("端口 %d 可用", port)
		return info
	}

	if pids := listeningPIDs(port); len(pids) > 0 {
		info.PID = pids[0]
		info.Process = processName(info.PID)
		if info.Process == "" {
			info.Process = "未知进程"
		}
		info.Message = fmt.Sprintf("端口 %d 已被 %s (PID %d) 占用", port, info.Process, info.PID)
		return info
	}

	info.Message = fmt.Sprintf("端口 %d 无法绑定：%v", port, err)
	return info
}

// pickFreePort 从 from 开始向后找第一个空闲端口，找不到时返回 0。
func pickFreePort(from, to int) int {
	if from < minWizardPort {
		from = minWizardPort
	}
	for p := from; p <= to && p < 65536; p++ {
		if probePort(p).Free {
			return p
		}
	}
	return 0
}

// processName 由 pid 反查进程名（取不到时返回空串）。
func processName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := probeCmd(probeTimeout, "tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	if err != nil && strings.TrimSpace(out) == "" {
		return ""
	}
	line := strings.TrimSpace(out)
	if line == "" || strings.HasPrefix(strings.ToLower(line), "info:") {
		return ""
	}
	fields := strings.Split(line, ",")
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"`)
}
