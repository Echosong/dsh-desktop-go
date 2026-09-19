package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProgressFn 是下载 / 安装的进度回调。
type ProgressFn func(p Progress)

// errChecksum 表示安装包校验失败。
var errChecksum = errors.New("安装包校验失败，下载的文件不完整或被篡改")

// downloadTimeout 是单次下载的整体超时上限。
const downloadTimeout = 10 * time.Minute

// httpClient 返回下载用的客户端：设置了连接与响应头超时，避免网络卡死时无限等待。
func httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
	}
}

// fetchText 拉取小体积文本（版本清单、校验清单）。
func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DSH-Desktop")

	resp, err := httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取 %s 失败：HTTP %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// progressWriter 统计写入字节数并节流上报进度。
type progressWriter struct {
	w          io.Writer
	done       int64
	total      int64
	last       time.Time
	onProgress ProgressFn
	step       int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)

	if p.onProgress != nil {
		now := time.Now()
		if now.Sub(p.last) >= 200*time.Millisecond {
			p.last = now
			p.onProgress(Progress{
				Step:     p.step,
				Phase:    "download",
				Percent:  percentOf(p.done, p.total),
				Received: p.done,
				Total:    p.total,
			})
		}
	}
	return n, err
}

// percentOf 计算百分比，总量未知时返回 -1。
func percentOf(done, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(done) / float64(total) * 100
}

// downloadFile 下载到 dst，支持断点续传（写 dst + ".part"）。
// 服务器不支持 Range 时会自动丢弃已下载的部分重新开始。
func downloadFile(ctx context.Context, url, dst string, step int, onProgress ProgressFn) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + ".part"

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "DSH-Desktop")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 本地缓存比远端还大，说明 .part 已经损坏，删掉重来
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		_ = os.Remove(part)
		return downloadFile(ctx, url, dst, step, onProgress)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("下载失败：HTTP %s", resp.Status)
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		offset = 0
		_ = os.Remove(part)
	}

	total := int64(-1)
	if resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if offset > 0 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}

	pw := &progressWriter{w: f, done: offset, total: total, onProgress: onProgress, step: step}
	_, copyErr := io.Copy(pw, resp.Body)

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	// Windows 上必须先关闭句柄才能改名
	if err := f.Close(); err != nil {
		return err
	}
	if copyErr != nil {
		return copyErr
	}
	if total > 0 && pw.done < total {
		return fmt.Errorf("下载不完整：%d/%d 字节", pw.done, total)
	}

	if onProgress != nil {
		onProgress(Progress{Step: step, Phase: "download", Percent: 100, Received: total, Total: total})
	}
	return os.Rename(part, dst)
}

// sha256File 流式计算文件摘要，不把整个文件读进内存。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// expectSHA256 从 SHASUMS256.txt 内容里取出指定文件的摘要。
func expectSHA256(shasums, filename string) string {
	want := strings.ToLower(filepath.Base(filename))
	for _, line := range strings.Split(shasums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		if strings.ToLower(filepath.Base(fields[1])) == want {
			return fields[0]
		}
	}
	return ""
}

// safeJoin 拼接解压目标路径，并挡住 zip slip（条目名里带 ..\ 越出目标目录）。
func safeJoin(dir, name string) (string, bool) {
	target := filepath.Join(dir, filepath.FromSlash(name))
	root := filepath.Clean(dir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), root) {
		return "", false
	}
	return target, true
}

// unzipStripTop 解压 zip；strip 为 true 时剥掉最外层目录（Node 官方包的目录结构需要）。
func unzipStripTop(src, dst string, strip bool) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		name := f.Name
		if strip {
			idx := strings.IndexAny(name, `/\`)
			if idx < 0 {
				continue
			}
			name = name[idx+1:]
		}
		if strings.TrimSpace(name) == "" {
			continue
		}

		target, ok := safeJoin(dst, name)
		if !ok {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

// extractZipEntry 释放单个 zip 条目到 target。
func extractZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// nodeMirrors 返回 Node 下载镜像，按国内优先的顺序回退。
func nodeMirrors() []string {
	return []string{
		"https://npmmirror.com/mirrors/node/",
		"https://nodejs.org/dist/",
	}
}

// nodeRelease 是 Node 版本清单里的一条记录。
// lts 字段在不同版本里可能是 false 或字符串，所以用 any 承接。
type nodeRelease struct {
	Version string `json:"version"`
	LTS     any    `json:"lts"`
}

// semverGreater 比较两个纯数字版本号，a > b 时返回 true。
func semverGreater(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(pb[i])
		}
		if na != nb {
			return na > nb
		}
	}
	return false
}

// latestLTSNode 从镜像的版本清单里挑出最新的、满足 dsh 硬下限的 LTS 版本。
func latestLTSNode(ctx context.Context) (string, error) {
	var lastErr error
	for _, mirror := range nodeMirrors() {
		raw, err := fetchText(ctx, mirror+"index.json")
		if err != nil {
			lastErr = err
			continue
		}
		var rels []nodeRelease
		if err := json.Unmarshal([]byte(raw), &rels); err != nil {
			lastErr = err
			continue
		}

		best := ""
		for _, r := range rels {
			// lts 为 false 或缺省表示不是 LTS
			if r.LTS == nil {
				continue
			}
			if isLTS, ok := r.LTS.(bool); ok && !isLTS {
				continue
			}
			ver := strings.TrimPrefix(strings.TrimSpace(r.Version), "v")
			// 复用同一套下限判断，天然排除 22.0~22.13 这类不可用版本
			if !nodeVersionUsable(ver) {
				continue
			}
			if best == "" || semverGreater(ver, best) {
				best = ver
			}
		}
		if best != "" {
			return best, nil
		}
		lastErr = fmt.Errorf("镜像 %s 的版本清单里没有满足 Node ≥ %d.%d 的 LTS 版本",
			mirror, minNodeMajor, minNodeMinor)
	}

	if lastErr == nil {
		lastErr = errors.New("无法获取 Node 版本清单")
	}
	return "", lastErr
}

// installPortableNode 完整走一遍便携版 Node 的安装：
// 选版本 → 下载（失败换镜像重试）→ sha256 校验 → 解压到临时目录 → 原子落位 → 复检。
// 任何一步失败都会清理临时产物，不留半成品。
func installPortableNode(ctx context.Context, onProgress ProgressFn) (NodeInfo, error) {
	report := func(phase string, note string, pct float64) {
		if onProgress != nil {
			onProgress(Progress{Step: stepNode, Phase: phase, Percent: pct, Note: note})
		}
	}

	report("verify", "正在获取 Node 版本清单 …", -1)
	ver, err := latestLTSNode(ctx)
	if err != nil {
		return NodeInfo{}, err
	}

	zipName := fmt.Sprintf("node-v%s-win-x64.zip", ver)
	if err := os.MkdirAll(runtimeDownloadDir(), 0o755); err != nil {
		return NodeInfo{}, err
	}
	zipPath := filepath.Join(runtimeDownloadDir(), zipName)

	mirrors := nodeMirrors()
	var dlErr error
	for attempt := 1; attempt <= 3; attempt++ {
		mirror := mirrors[(attempt-1)%len(mirrors)]
		base := mirror + "v" + ver + "/"
		if attempt > 1 {
			report("download", fmt.Sprintf("第 %d 次重试（换镜像 %s）…", attempt, mirror), -1)
		}

		dlErr = func() error {
			if err := downloadFile(ctx, base+zipName, zipPath, stepNode, onProgress); err != nil {
				return err
			}

			report("verify", "正在校验安装包 …", -1)
			sums, err := fetchText(ctx, base+"SHASUMS256.txt")
			if err != nil {
				return err
			}
			want := expectSHA256(sums, zipName)
			if want == "" {
				return fmt.Errorf("校验清单中未找到 %s", zipName)
			}
			got, err := sha256File(zipPath)
			if err != nil {
				return err
			}
			if !strings.EqualFold(got, want) {
				_ = os.Remove(zipPath)
				return errChecksum
			}
			return nil
		}()
		if dlErr == nil {
			break
		}
		// 校验失败说明已下载的文件不可信，删掉后重下
		if errors.Is(dlErr, errChecksum) {
			_ = os.Remove(zipPath)
		}
	}
	if dlErr != nil {
		_ = os.Remove(zipPath + ".part")
		return NodeInfo{}, dlErr
	}

	report("extract", "正在解压 …", -1)
	_ = os.RemoveAll(runtimeNodeTmpDir())
	if err := unzipStripTop(zipPath, runtimeNodeTmpDir(), true); err != nil {
		_ = os.RemoveAll(runtimeNodeTmpDir())
		return NodeInfo{}, fmt.Errorf("解压失败：%w", err)
	}

	// 原子落位：先把旧的挪开，再把新目录改名到位
	old := runtimeNodeDir() + ".old"
	_ = os.RemoveAll(old)
	if dirExists(runtimeNodeDir()) {
		if err := os.Rename(runtimeNodeDir(), old); err != nil {
			_ = os.RemoveAll(runtimeNodeTmpDir())
			return NodeInfo{}, fmt.Errorf("替换旧版本失败：%w", err)
		}
	}
	if err := os.Rename(runtimeNodeTmpDir(), runtimeNodeDir()); err != nil {
		// 尽力回滚
		if dirExists(old) {
			_ = os.Rename(old, runtimeNodeDir())
		}
		_ = os.RemoveAll(runtimeNodeTmpDir())
		return NodeInfo{}, fmt.Errorf("安装目录落位失败：%w", err)
	}
	_ = os.RemoveAll(old)
	_ = os.Remove(zipPath)

	// 复检：解开来的 node.exe 有可能被杀毒软件当场拦截
	exe := filepath.Join(runtimeNodeDir(), "node.exe")
	info, ok := probeOneNode(exe, srcManaged)
	if !ok {
		return NodeInfo{}, fmt.Errorf("安装后复检未通过：%s", info.Reason)
	}

	report("done", fmt.Sprintf("Node v%s 安装完成", info.Version), 100)
	return info, nil
}

// dirExists 判断目录是否存在。
func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// dshPackage 是 npm 上的 dsh 包名。
const dshPackage = "@deepseek-ai/dsh"

// npmRegistry 返回 npm 源（国内优先）。
func npmRegistry() string {
	if r := strings.TrimSpace(os.Getenv("DSH_NPM_REGISTRY")); r != "" {
		return r
	}
	return "https://registry.npmmirror.com"
}

// npmAddedPattern 用于判定 npm 安装是否真的成功。
// npm 在安装过程中会刷出大量 warn 与 cleanup failed，那些都不代表失败，
// 唯一可靠的判据是最后一行有没有 `added ... packages`。
var npmAddedPattern = regexp.MustCompile(`\badded \d+ package`)

// installDshWith 用「配对的 node + 该 node 自带的 npm-cli.js」把 dsh 装到指定 prefix。
//
// 这里刻意不用裸 npm，也不用 PATH 里的 node：机器上可能同时存在多个 node，
// 裸 npm 的全局前缀未必指向我们想要的位置，装完会出现「装成功了但找不到」。
func installDshWith(ctx context.Context, node NodeInfo, prefix string, onProgress ProgressFn) error {
	if node.NpmCLI == "" {
		return errors.New("该 Node 未附带 npm，无法用它安装 dsh")
	}
	if !dirExists(prefix) {
		if err := os.MkdirAll(prefix, 0o755); err != nil {
			return err
		}
	}

	args := []string{
		node.NpmCLI, "install", "-g",
		"--prefix", prefix,
		"--registry", npmRegistry(),
		"--no-audit", "--no-fund",
		"--loglevel", "info",
		allowScriptsFlag,
		dshPackage,
	}

	cmd := exec.CommandContext(ctx, node.Path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	cmd.Env = append(os.Environ(), "npm_config_prefix="+prefix)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var (
		mu     sync.Mutex
		lastLn string
		added  bool
	)
	consume := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			mu.Lock()
			lastLn = line
			if npmAddedPattern.MatchString(line) {
				added = true
			}
			mu.Unlock()

			if onProgress != nil {
				onProgress(Progress{Step: stepDsh, Phase: "npm", Percent: -1, Note: line})
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); consume(stdout) }()
	go func() { defer wg.Done(); consume(stderr) }()
	wg.Wait()

	runErr := cmd.Wait()
	if ctx.Err() != nil {
		// 被取消：结束可能残留的 npm 子进程树
		killTree(cmd.Process.Pid)
		return ctx.Err()
	}

	mu.Lock()
	ok := added
	detail := lastLn
	mu.Unlock()

	if !ok {
		if runErr != nil {
			return fmt.Errorf("npm 安装失败：%v（最后一行：%s）", runErr, detail)
		}
		return fmt.Errorf("npm 没有报告安装结果，最后一行：%s", detail)
	}
	return nil
}
