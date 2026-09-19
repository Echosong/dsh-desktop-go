"""验证 HTTP 302 重定向能否规避 dsh 的 SameSite=Strict 限制。

思路：
  跨站「脚本导航」（location.replace）会让 Cookie 不回传，而标准规定
  重定向链（redirect count != 0）应改用**当前 URL 的 site** 作为 site-for-cookies。
  若 Chrome/WebView2 遵守这一点，那么：

    启动页 --同源导航--> /go（应用自己的路径） --302--> dsh 的 token 地址

  就等价于「从 dsh 站点内部发起导航」，Cookie 能正常种下并回传，
  全程不会出现 "dsh web authentication required" 页面。

判定：dsh 端口上是否出现 ESTABLISHED 长连接（真实 UI 会建 WebSocket）。

用法：python diag_redirect.py
"""

import http.server
import os
import re
import socketserver
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _diag_env import find_browser, find_dsh, node_dir  # noqa: E402

PORT = 3399
MOCK_PORT = 9999
EDGE = find_browser()
DSH = find_dsh()


def start_dsh():
    holder = {}
    env = dict(os.environ)
    extra = node_dir()
    if extra:
        env["PATH"] = extra + os.pathsep + env.get("PATH", "")

    proc = subprocess.Popen(
        ["cmd", "/c", DSH, "web", "--port", str(PORT), "--no-open"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, bufsize=1, errors="replace", env=env,
        cwd=os.path.expanduser("~"),
    )

    def reader():
        for line in proc.stdout:
            m = re.search(r"https?://[^\s]*token=[^\s]*", line)
            if m and "url" not in holder:
                holder["url"] = m.group(0).strip()

    threading.Thread(target=reader, daemon=True).start()
    for _ in range(160):
        if "url" in holder:
            break
        time.sleep(0.5)
    return proc, holder.get("url")


def stop_dsh(proc):
    subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
    time.sleep(1.5)


def count_established(port):
    try:
        raw = subprocess.run(["netstat", "-ano", "-p", "tcp"],
                             capture_output=True, timeout=20).stdout
    except Exception:
        return -1
    if not raw:
        return -1
    n = 0
    for line in raw.decode("gbk", errors="ignore").splitlines():
        f = line.split()
        if len(f) >= 5 and f[0].upper() == "TCP" and f[3].upper() == "ESTABLISHED":
            if f[1].endswith(f":{port}") or f[2].endswith(f":{port}"):
                n += 1
    return n


class ReusableTCPServer(socketserver.TCPServer):
    allow_reuse_address = True


def serve_mock(token_url):
    """宿主页与 /go 同源；/go 用 302 跳到 dsh 的 token 地址。"""

    class H(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith("/go"):
                self.send_response(302)
                self.send_header("Location", token_url)
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
                return
            body = (
                "<html><body>mock-host-page<script>"
                "location.replace('/go')"
                "</script></body></html>"
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    srv = ReusableTCPServer(("127.0.0.1", MOCK_PORT), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def probe(tag, target, wait=25):
    ud = tempfile.mkdtemp(prefix="edge-diag-")
    print("=" * 24, tag)
    print("   target:", target)
    p = subprocess.Popen(
        [EDGE, "--headless=new", "--disable-gpu", "--no-first-run",
         "--no-default-browser-check", f"--user-data-dir={ud}", target],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    est = 0
    for i in range(int(wait / 5)):
        time.sleep(5)
        est = count_established(PORT)
        print(f"   t={5 * (i + 1):>2}s  端口 {PORT} 长连接 = {est}")
        if est > 0:
            break
    try:
        p.kill()
    except Exception:
        pass
    ok = est > 0
    print(f"   >>> {'成功：302 重定向可行，UI 已加载' if ok else '失败：仍停在 401'}\n")
    return ok


def main():
    if not DSH:
        print("找不到 dsh 命令，请安装 @deepseek-ai/dsh 或用环境变量 DSH_BIN 指定")
        return 1
    if not EDGE:
        print("找不到 Chromium 内核浏览器，可用环境变量 DSH_EDGE 指定")
        return 1

    proc, url = start_dsh()
    if not url:
        print("!! 拿不到 token 地址")
        return 1
    print("token:", url, "\n")
    srv = serve_mock(url)
    try:
        # 宿主页在 localhost（异 site），但 /go 是它自己的同源路径
        probe("D 同源页面 -> /go -> 302 -> dsh token 地址",
              f"http://localhost:{MOCK_PORT}/")
    finally:
        srv.shutdown()
        stop_dsh(proc)
    return 0


if __name__ == "__main__":
    sys.exit(main())
