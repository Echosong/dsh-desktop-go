@echo off
setlocal

rem 依赖：Go 1.21+、Wails CLI v2.9+、gcc(MinGW-w64)
set "GO_BIN=E:\goapp\bin"
set "GO_PATH_BIN=E:\goproject\bin"
set "MINGW_BIN=%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin"

set "PATH=%GO_BIN%;%GO_PATH_BIN%;%MINGW_BIN%;%PATH%"

cd /d "%~dp0"

where go >nul 2>nul || (echo [ERROR] 未找到 go，请检查 GO_BIN 配置 & pause & exit /b 1)
where wails >nul 2>nul || (echo [ERROR] 未找到 wails，请执行 go install github.com/wailsapp/wails/v2/cmd/wails@latest & pause & exit /b 1)

if not exist "build\appicon.png" (
  echo [INFO] 生成图标 ...
  python scripts\make_icon.py
)

echo [INFO] wails build ...
wails build -clean
if errorlevel 1 (
  echo [ERROR] 编译失败
  pause
  exit /b 1
)

echo.
echo [OK] 产物: build\bin\DSH Desktop.exe
pause
