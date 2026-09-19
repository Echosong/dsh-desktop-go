"""验证「同站导航」能否修复 dsh 的 SameSite=Strict 鉴权问题。

结论背景：
  dsh 会话 Cookie 为 `HttpOnly; SameSite=Strict`。Wails 启动页在 http://wails.localhost，
  与 dsh 的 http://127.0.0.1:3388 不是同一个 site，因此从启动页跳转过去时 Cookie 被丢弃，
  页面落到 "dsh web authentication required"。

SameSite 判定只看 scheme + 可注册域，**端口不参与**。所以如果先让文档落到
127.0.0.1（哪怕只是 401 文本页），再发起指向 127.0.0.1:PORT 的带令牌导航，就属于同站导航，
Cookie 会被正常种下并回传。

实验：
  A) 宿主页 http://localhost:9999（异 site）  → token 地址：预期 401
  C) 宿主页 http://127.0.0.1:9999（同 site） → token 地址：预期成功拿到 dsh 页面
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
    """启动 dsh web 并返回 (进程, token 地址)。"""
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


class ReusableTCPServer(socketserver.TCPServer):
    allow_reuse_address = True


def serve_mock(token_url):
    class H(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = (
                "<html><body>mock-host-page<script>"
                f"location.replace({token_url!r})"
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


def count_established(port):
    """统计监听端口上的 ESTABLISHED 连接数（dsh UI 加载成功会建立 WebSocket 长连接）。"""
    try:
        raw = subprocess.run(["netstat", "-ano", "-p", "tcp"],
                             capture_output=True, timeout=20).stdout
    except Exception:
        return -1
    if not raw:
        return -1
    text = raw.decode("gbk", errors="ignore")
    n = 0
    for line in text.splitlines():
        f = line.split()
        if len(f) >= 5 and f[0].upper() == "TCP" and f[3].upper() == "ESTABLISHED":
            if f[1].endswith(f":{port}") or f[2].endswith(f":{port}"):
                n += 1
    return n


def probe(tag, target, wait=25):
    """打开页面并观察端口连接状态：出现长连接=真实 UI 加载成功。"""
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
        print(f"   t={5 * (i + 1):>2}s  端口 {PORT} 上的 ESTABLISHED 连接数 = {est}")
        if est > 0:
            break
    try:
        p.kill()
    except Exception:
        pass
    print(f"   >>> 结果: {'UI 已加载（存在长连接）' if est > 0 else '无长连接 —— 应停在 401 文本页'}\n")
    return est


def main():
    if not DSH:
        print("找不到 dsh 命令，请安装 @deepseek-ai/dsh 或用环境变量 DSH_BIN 指定")
        return 1
    if not EDGE:
        print("找不到 Chromium 内核浏览器，可用环境变量 DSH_EDGE 指定")
        return 1

    only = None
    if len(sys.argv) > 2 and sys.argv[1] == "--only":
        only = sys.argv[2].upper()

    # --- 实验 A：异 site 跳转 ---
    if only in (None, "A"):
        proc, url = start_dsh()
        if not url:
            print("!! 拿不到 token 地址")
            return 1
        print("A 阶段 token:", url)
        srv = serve_mock(url)
        probe("A 从 localhost:9999（异 site）跳转", f"http://localhost:{MOCK_PORT}/")
        srv.shutdown()
        stop_dsh(proc)

    # --- 实验 C：同 site 跳转（换新实例、新 token） ---
    if only in (None, "C"):
        proc, url = start_dsh()
        if not url:
            print("!! 拿不到 token 地址")
            return 1
        print("C 阶段 token:", url)
        srv = serve_mock(url)
        probe("C 从 127.0.0.1:9999（同 site，模拟先落到 dsh 源）跳转",
              f"http://127.0.0.1:{MOCK_PORT}/")
        srv.shutdown()
        stop_dsh(proc)

    return 0


if __name__ == "__main__":
    sys.exit(main())
