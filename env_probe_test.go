package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNodeVersionUsable 覆盖 dsh 对 Node 的硬下限判定。
// 重点是 22.0~22.13 这一段「看着是 22.x 但不可用」的版本必须被判为不合格。
func TestNodeVersionUsable(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"22.14.0", true},
		{"22.22.2", true},
		{"22.13.9", false},
		{"22.0.0", false},
		{"20.11.0", false},
		{"18.20.4", false},
		{"24.0.0", true},
		{"23.5.0", true},
		{"", false},
		{"22", false},
		{"v22.14.0", false}, // 带 v 前缀属于非法输入，调用方已剥掉
	}
	for _, c := range cases {
		if got := nodeVersionUsable(c.version); got != c.want {
			t.Errorf("nodeVersionUsable(%q) = %v, 期望 %v", c.version, got, c.want)
		}
	}
}

// TestSemverGreater 覆盖版本比较。
func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"22.22.2", "22.14.0", true},
		{"22.14.0", "22.22.2", false},
		{"22.14.0", "22.14.0", false},
		{"24.0.0", "22.99.99", true},
		{"22.14.1", "22.14.0", true},
	}
	for _, c := range cases {
		if got := semverGreater(c.a, c.b); got != c.want {
			t.Errorf("semverGreater(%q, %q) = %v, 期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// TestExpectSHA256 覆盖从 SHASUMS256.txt 里取指定文件的摘要。
func TestExpectSHA256(t *testing.T) {
	sums := "aaa111  node-v22.14.0-win-x64.zip\n" +
		"bbb222  node-v22.14.0-darwin-arm64.tar.gz\n" +
		"\n" +
		"ccc333  headers.tar.gz\n"

	if got := expectSHA256(sums, "node-v22.14.0-win-x64.zip"); got != "aaa111" {
		t.Errorf("取 win-x64 摘要 = %q, 期望 aaa111", got)
	}
	if got := expectSHA256(sums, "不存在的文件.zip"); got != "" {
		t.Errorf("不存在的文件应返回空串，实际 %q", got)
	}
}

// TestSafeJoinBlocksZipSlip 确认解压时不会把文件写到目标目录之外。
func TestSafeJoinBlocksZipSlip(t *testing.T) {
	root := t.TempDir()

	if _, ok := safeJoin(root, "node-v22.14.0-win-x64/node.exe"); !ok {
		t.Error("正常条目不应被拒绝")
	}
	if _, ok := safeJoin(root, "../../evil.exe"); ok {
		t.Error("越界条目必须被拒绝")
	}
	if _, ok := safeJoin(root, "sub/../../evil.exe"); ok {
		t.Error("嵌套越界条目必须被拒绝")
	}
}

// TestPercentOf 覆盖进度百分比计算（总量未知时返回 -1）。
func TestPercentOf(t *testing.T) {
	if got := percentOf(50, 100); got != 50 {
		t.Errorf("percentOf(50,100) = %v, 期望 50", got)
	}
	if got := percentOf(1, 0); got != -1 {
		t.Errorf("总量未知时应返回 -1，实际 %v", got)
	}
	if got := percentOf(1, -1); got != -1 {
		t.Errorf("total 为负时应返回 -1，实际 %v", got)
	}
}

// TestKindOfDshBin 覆盖 dsh 入口类型判定。
func TestKindOfDshBin(t *testing.T) {
	cases := map[string]string{
		`C:\x\lib\bin.js`:  kindJS,
		`C:\x\dsh.cmd`:     kindCmd,
		`C:\x\dsh.exe`:     kindCmd,
		`C:\x\bin.JS`:      kindJS, // 扩展名大小写不敏感
		`C:\x\dsh`:         kindCmd,
		`C:\x\dsh.BAT`:     kindCmd,
		`C:\x\deepseek.js`: kindJS,
	}
	for bin, want := range cases {
		if got := kindOfDshBin(bin); got != want {
			t.Errorf("kindOfDshBin(%q) = %q, 期望 %q", bin, got, want)
		}
	}
}

// TestDshInvocation 覆盖「怎么把 dsh 跑起来」的翻译：js 走 node，cmd 走 cmd.exe。
func TestDshInvocation(t *testing.T) {
	node := `D:\soft\nodejs\node.exe`

	exe, args := dshInvocation(node, `D:\soft\nodejs\node_modules\@deepseek-ai\dsh\lib\bin.js`, kindJS)
	if exe != node || len(args) != 1 {
		t.Errorf("js 入口应使用配对的 node，实际 exe=%q args=%v", exe, args)
	}

	exe, args = dshInvocation(node, `D:\soft\nodejs\dsh.cmd`, kindCmd)
	if len(args) != 2 || args[0] != "/c" {
		t.Errorf("Windows 上 .cmd 必须经 cmd.exe /c 执行，实际 exe=%q args=%v", exe, args)
	}
}

// TestPortRange 覆盖端口合法性判定。
func TestPortRange(t *testing.T) {
	if probePort(80).Invalid != true {
		t.Error("低于 1024 的端口应被判为不合法")
	}
	if probePort(0).Invalid != true {
		t.Error("端口 0 应被判为不合法")
	}
	if probePort(70000).Invalid != true {
		t.Error("超过 65535 的端口应被判为不合法")
	}
	if probePort(3388).Invalid != false {
		t.Error("3388 应当是合法端口")
	}
}

// TestProbePortDetectsSelfOccupied 本进程自己占着端口时，必须报告「不可用」。
// 注意：probePort 走的是 listeningPIDs，而它会主动跳过自身 pid（避免误杀自己），
// 所以这种情况下解析不出 pid 是预期行为，不是 bug。
func TestProbePortDetectsSelfOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法建立监听，跳过：%v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	info := probePort(port)
	if info.Invalid {
		t.Fatalf("端口 %d 落在合法区间，不应被判为非法", port)
	}
	if info.Free {
		t.Errorf("端口 %d 已被本进程占用，probePort 却报告空闲", port)
	}
}

// TestProbePortResolvesOccupant 用独立进程占住端口，验证能解析出占用者 pid 与进程名。
// 「结束占用端口的进程」这个功能就靠它，所以必须真跑一遍。
func TestProbePortResolvesOccupant(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未找到 node，跳过占用进程解析测试")
	}

	port := pickFreePort(35000, 35100)
	if port == 0 {
		t.Skip("35100 以内没找到空闲端口，跳过")
	}

	cmd := exec.Command(node, "-e",
		fmt.Sprintf("require('net').createServer().listen(%d,'127.0.0.1')", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动占位进程，跳过：%v", err)
	}
	defer killTree(cmd.Process.Pid)

	// 等它真的把端口监听起来
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !probePort(port).Free {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	info := probePort(port)
	if info.Free {
		t.Fatalf("端口 %d 应被占位进程占用，probePort 却报告空闲", port)
	}
	if info.PID == 0 {
		t.Errorf("端口 %d 被占用，但没解析出占用进程 pid", port)
	}
	if info.Process == "" {
		t.Errorf("端口 %d 被占用，但没解析出占用进程名（tasklist 是否可用？）", port)
	} else {
		t.Logf("占用者：%s (PID %d)", info.Process, info.PID)
		if !strings.Contains(strings.ToLower(info.Process), "node") {
			t.Logf("提示：进程名 %q 不是 node，可能是解析到了别的行", info.Process)
		}
	}
}

// TestManualInstallCommand 确认兜底命令带上了三个关键要素：
// 用配对的 npm-cli.js、显式 --prefix、以及 Windows 必需的 --allow-scripts。
func TestManualInstallCommand(t *testing.T) {
	node := `D:\soft\nodejs\node.exe`
	cli := `D:\soft\nodejs\node_modules\npm\bin\npm-cli.js`
	prefix := `D:\soft\nodejs`

	cmd := manualInstallCommand(node, cli, prefix)
	for _, want := range []string{node, "npm-cli.js", "--prefix", prefix, "--allow-scripts", dshPackage} {
		if !strings.Contains(cmd, want) {
			t.Errorf("命令里应包含 %q，实际：%s", want, cmd)
		}
	}

	// 拿不到 npm-cli.js 时要退回朴素写法，而不是拼出半截命令
	fallback := manualInstallCommand("", "", "")
	if !strings.HasPrefix(fallback, "npm install") {
		t.Errorf("兜底命令应以 npm install 开头，实际：%s", fallback)
	}
}

// TestDiagSetup 不是断言型测试，而是把向导四步的探测结果打印出来，
// 便于和手工执行的 where node / where dsh / netstat 结果对照。
// 用 `go test -run TestDiagSetup -v` 单独运行。
func TestDiagSetup(t *testing.T) {
	start := time.Now()
	cfg := LoadConfig()

	t.Log("=========== 向导环境探测 ===========")
	t.Logf("配置：setupCompleted=%v nodePath=%q dshBin=%q port=%d", cfg.SetupCompleted, cfg.NodePath, cfg.DshBin, cfg.Port)

	nodes := probeNodes(cfg)
	t.Logf("Node 候选（共 %d 个）：", len(nodes))
	for i, n := range nodes {
		mark := "可用"
		if !n.OK {
			mark = "不可用：" + n.Reason
		}
		t.Logf("  [%d] %s  v%s  source=%s  %s  prefix=%s", i, n.Path, n.Version, n.Source, mark, n.Prefix)
	}

	dshs := probeDshs(nodes, cfg)
	t.Logf("dsh 候选（共 %d 个）：", len(dshs))
	for i, d := range dshs {
		mark := "可用"
		if !d.OK {
			mark = "不可用：" + d.Reason
		}
		t.Logf("  [%d] %s  %s  v%s  source=%s  node=%s", i, d.Bin, d.Kind, d.Version, d.Source, d.Node)
		t.Logf("        %s", mark)
	}

	port, locked := cfg.ResolvedPort()
	pi := probePort(port)
	t.Logf("端口：%d（被环境变量锁定=%v）→ %s", port, locked, pi.Message)
	t.Logf("空闲端口探测（从 %d 起）：%d", port, pickFreePort(port, portScanMax))
	t.Logf("耗时 %s", time.Since(start).Round(time.Millisecond))
	t.Log("====================================")
}
