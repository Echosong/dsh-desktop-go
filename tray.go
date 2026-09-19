package main

import (
	_ "embed"
	"fmt"
	"runtime"

	"github.com/energye/systray"
)

//go:embed build/windows/icon.ico
var trayIconBytes []byte

// startTray 在独立线程里运行系统托盘。
//
// systray 需要自己的 Windows 消息循环，并且必须固定在某一个 OS 线程上；
// 它的包级 init 只锁定了主线程，所以这里显式再锁一次当前 goroutine。
func (a *App) startTray() {
	go func() {
		runtime.LockOSThread()
		systray.Run(a.onTrayReady, a.onTrayExit)
	}()
}

func (a *App) onTrayReady() {
	systray.SetIcon(trayIconBytes)
	systray.SetTitle("DSH Desktop")
	systray.SetTooltip("DSH Desktop — DeepSeek Harness")

	mShow := systray.AddMenuItem("显示主窗口", "显示 DSH Desktop 窗口")
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("在浏览器中打开", "用默认浏览器打开 dsh web")
	mReload := systray.AddMenuItem("重新加载页面", "")
	mRestart := systray.AddMenuItem("重启 dsh web", "")
	systray.AddSeparator()
	mSetup := systray.AddMenuItem("环境设置…", "重新检测并配置运行环境（会结束当前 dsh 会话）")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出 DSH Desktop 并结束 dsh 进程")

	// 左键单击托盘图标直接唤回窗口
	systray.SetOnClick(func(systray.IMenu) { a.trayAction(a.showWindow) })

	mShow.Click(func() { a.trayAction(a.showWindow) })
	mOpen.Click(func() { a.trayAction(a.OpenInBrowser) })
	mReload.Click(func() { a.trayAction(a.Reload) })
	mRestart.Click(func() { a.trayAction(func() { a.RestartDSH() }) })
	mSetup.Click(func() { a.trayAction(a.reopenSetup) })
	mQuit.Click(func() { a.trayAction(a.quitApp) })

	writeFileLog("tray: 系统托盘已就绪")
}

// trayAction 把托盘回调从托盘自己的消息循环线程挪到独立 goroutine 执行。
// 托盘回调运行在 systray 的线程上，直接在里面做窗口/运行时操作容易和 Wails 主线程互相影响，
// 这里统一隔离一层，并兜住 panic，保证托盘不会因为某个动作失败而失效。
func (a *App) trayAction(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				writeFileLog("tray: 动作执行异常: " + fmt.Sprint(r))
			}
		}()
		fn()
	}()
}

func (a *App) onTrayExit() {
	writeFileLog("tray: 系统托盘已退出")
}

// setTrayTooltip 更新托盘提示文字，让用户知道窗口当前是显示还是收起了。
func (a *App) setTrayTooltip(text string) {
	defer func() { _ = recover() }()
	systray.SetTooltip(text)
}

// stopTray 结束托盘（应用退出时调用）。
func (a *App) stopTray() {
	systray.Quit()
}
