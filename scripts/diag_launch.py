"""以「双击」的方式启动 DSH Desktop，用于验证真实使用场景下是否还有 cmd 黑窗。

从 bash / cmd 里启动 exe 时，进程会继承已有控制台，子进程（cmd.exe → dsh）就不会另开窗口，
掩盖问题。用 DETACHED_PROCESS 启动可以复现用户双击 exe 的场景：进程完全没有控制台。

用法：python diag_launch.py
"""

import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _diag_env import find_exe, node_dir  # noqa: E402

DETACHED_PROCESS = 0x00000008
CREATE_NEW_PROCESS_GROUP = 0x00000200


def main():
    exe = find_exe()
    if not exe:
        print("找不到编译产物，请先 wails build，或用环境变量 DSH_EXE 指定")
        return 1

    env = dict(os.environ)
    extra = node_dir()
    if extra:
        env["PATH"] = extra + os.pathsep + env.get("PATH", "")

    p = subprocess.Popen(
        [exe],
        cwd=os.path.dirname(exe),
        env=env,
        creationflags=DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP,
        close_fds=True,
    )
    print(f"已以无控制台方式启动 {os.path.basename(exe)}，pid={p.pid}")
    time.sleep(1)
    return 0


if __name__ == "__main__":
    sys.exit(main())
