package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// appDirName 是应用在用户数据目录下的文件夹名。
const appDirName = "DSH Desktop"

// dataDir 返回应用数据目录：%LOCALAPPDATA%\DSH Desktop。
// 配置、日志、自管的运行时全都放在这里。
func dataDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, appDirName)
}

// runtimeDir 返回应用自管运行时目录：<data>\runtime。
func runtimeDir() string { return filepath.Join(dataDir(), "runtime") }

// runtimeNodeDir 返回便携版 Node 的落位目录：<data>\runtime\node。
func runtimeNodeDir() string { return filepath.Join(runtimeDir(), "node") }

// runtimeNodeTmpDir 返回便携版 Node 解压时的临时目录（解压完成后再原子改名）。
func runtimeNodeTmpDir() string { return filepath.Join(runtimeDir(), "node.tmp") }

// runtimeNpmGlobal 返回应用私有的 npm 全局前缀：<data>\runtime\npm-global。
func runtimeNpmGlobal() string { return filepath.Join(runtimeDir(), "npm-global") }

// runtimeDownloadDir 返回下载缓存目录：<data>\runtime\download。
func runtimeDownloadDir() string { return filepath.Join(runtimeDir(), "download") }

// configPath 返回配置文件路径：<data>\config.json。
func configPath() string { return filepath.Join(dataDir(), "config.json") }

// Config 是向导写入的持久化配置。
//
// 注意：字段一律不带初始化默认值，默认值只在 ResolvedPort 等取值处显式兜底，
// 避免「new 一个 Config 再写回」时把默认值覆盖掉真实配置。
type Config struct {
	SetupCompleted bool   `json:"setupCompleted"`
	NodePath       string `json:"nodePath"`
	NodeVersion    string `json:"nodeVersion"`
	NodeSource     string `json:"nodeSource"`
	NpmPrefix      string `json:"npmPrefix"`
	DshBin         string `json:"dshBin"`
	DshKind        string `json:"dshKind"`
	DshVersion     string `json:"dshVersion"`
	DshSource      string `json:"dshSource"`
	Port           int    `json:"port"`
	Mirror         string `json:"mirror"`
	LastCheckAt    string `json:"lastCheckAt"`
}

// LoadConfig 读取配置。文件不存在或内容损坏时返回零值，并把损坏的文件改名保留，
// 保证下次启动能重新走一遍向导而不是反复报同一个错。
func LoadConfig() Config {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		writeFileLog("config: 解析 config.json 失败，已改名为 config.json.bad 并重新检测: " + err.Error())
		_ = os.Rename(configPath(), configPath()+".bad")
		return Config{}
	}
	return c
}

// Save 原子写入配置：先写 .tmp 再改名，避免写一半断电留下坏文件。
func (c Config) Save() error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// ResolvedPort 返回最终生效的端口，优先级：环境变量 DSH_WEB_PORT > 配置 > 默认。
// locked 为 true 表示端口被环境变量锁定，向导里应当禁用输入框。
func (c Config) ResolvedPort() (port int, locked bool) {
	if raw := strings.TrimSpace(os.Getenv("DSH_WEB_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && portInRange(p) {
			return p, true
		}
	}
	if portInRange(c.Port) {
		return c.Port, false
	}
	return defaultPort, false
}

// portInRange 判断端口号是否落在合法区间（宽松：允许 1~65535）。
// 环境变量与配置读到的值用它；向导 UI 另用 minWizardPort 收紧。
func portInRange(p int) bool { return p > 0 && p < 65536 }
