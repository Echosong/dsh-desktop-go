package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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
