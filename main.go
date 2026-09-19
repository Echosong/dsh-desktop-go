package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "DSH Desktop",
		Width:     1280,
		Height:    860,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 1},
		OnStartup:        app.startup,
		OnDomReady:       app.onDomReady,
		OnShutdown:       app.shutdown,
		// 点窗口关闭按钮不退出，收进系统托盘；只有托盘菜单「退出」才真正结束
		OnBeforeClose: func(ctx context.Context) bool {
			if app.isQuitting() {
				return false
			}
			// 向导期间窗口就是全部交互界面，收起托盘会让用户以为装完了，
			// 所以这里直接退出；安装进行中先确认一次，避免误关中断安装。
			if app.inSetup() {
				if app.busyNow() {
					choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
						Type:          runtime.QuestionDialog,
						Title:         "安装尚未完成",
						Message:       "正在安装运行环境，退出会中断安装。确定要退出吗？",
						Buttons:       []string{"退出", "继续安装"},
						DefaultButton: "继续安装",
					})
					if err != nil || choice != "退出" {
						return true
					}
				}
				app.quitApp()
				return false
			}
			app.hideToTray()
			return true
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		writeFileLog("wails.Run 返回错误: " + err.Error())
		log.Fatal(err)
	}
}
