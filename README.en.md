![DSH Desktop](docs/banner.png)

# DSH Desktop (Go)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-4D6BFE.svg)](#requirements)
[![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8.svg)](https://go.dev)
[![Wails](https://img.shields.io/badge/Wails-v2-DF0000.svg)](https://wails.io)

**Turns the DeepSeek Harness web UI into a double-click Windows desktop app.**

**The first run sets up its own environment** — it checks for Node and dsh, installs whatever is missing (portable Node, no admin rights), lets you pick a port, then starts. A few clicks, no terminal. Every run after that goes straight to the UI.

No terminal, no command to remember, no console window hanging around. Closing the window just tucks it into the system tray; you quit from the tray menu.

[简体中文](README.md) · English · [GitHub](https://github.com/Echosong/dsh-desktop-go) · [Gitee](https://gitee.com/hn-1024_0/dsh-desktop-go)

---

## The problem

To use dsh you run `dsh web` in a terminal, then copy the token-bearing URL it prints into a browser. Every time. You can't just bookmark that URL, because the token in it is per-process — it changes on every dsh restart.

So this shell does it for you: double-click the icon, it launches dsh, waits until the UI has actually connected, then swaps the window over. Nothing visible runs in the background, and nothing is left behind when you quit.

## Features

- **Double-click and go** — launches `dsh web`, waits until the Web UI has really connected before showing the window. No white flash, no error page.
- **First run configures itself** — detects Node and dsh, installs what's missing (portable Node, **no admin rights**), lets you confirm a port, then starts. See [First-run setup](#first-run-setup).
- **Leaves your existing setup alone** — if a working dsh is already there it just uses it; nothing is ever added to the system `PATH`.
- **No console windows** — dsh and every internal command run with `CREATE_NO_WINDOW`; you won't see a black window in Task Manager.
- **Lives in the tray** — the close button just hides the window; left-click the tray icon to bring it back, right-click to quit.
- **No orphan processes** — a Windows Job Object guarantees dsh dies with the app, even if the app crashes or is force-killed.
- **Never touches your API key** — it neither reads nor stores any model credentials (see [Credentials](#credentials)).

> **This is more than a dsh wrapper.** If you want to package **any** web service running on `localhost` (Ollama, Jupyter, a local dashboard…) as a desktop app, the startup sequencing, the SameSite pitfall, the child-process management and the tray implementation here are all directly reusable — see [How it works](#how-it-works).

![Screenshot](docs/screenshot.png)

## Quick start

Grab the exe from [Releases](https://github.com/Echosong/dsh-desktop-go/releases) and double-click it.

**You don't need Node or dsh installed beforehand.** The first launch opens a four-step wizard
(detect Node → detect dsh → confirm port → start) that installs whatever is missing — a portable
build, no admin rights required. After that, every launch goes straight to the UI.

Building from source:

```bash
git clone https://github.com/Echosong/dsh-desktop-go
cd dsh-desktop-go
wails build -clean
# output: build/bin/DSH Desktop.exe
```

## First-run setup

On first launch — and whenever you pick "环境设置… / Environment settings" from the tray menu — the app checks
the runtime environment before starting, in four steps:

| Step | What it does |
| --- | --- |
| 1 · Detect Node | Enumerates every Node on the machine (every `PATH` entry plus the usual install locations and npm prefixes) and probes each one; offers to download a portable build if none is usable |
| 2 · Detect dsh | Enumerates all dsh installs and **actually runs** `dsh --version` to confirm it works; installs it with the paired Node if missing |
| 3 · Confirm port | Defaults to `3388`, editable; live-detects whether it's taken, shows the occupying process and PID, and can kill it or auto-pick a free port |
| 4 · Start | Shows the final Node / dsh / port selection, then launches `dsh web` and swaps the window over |

Each step is an idempotent detect → report → install-if-needed → re-check cycle: satisfied steps show a green
check and can be skipped, every step can be re-checked, and every path can be set by hand. The wizard only
suggests — you confirm what actually gets used.

**The Node version floor is a hard requirement.** dsh relies on `import.meta.main`, which landed in Node 22.14.
Below that (especially 22.0–22.13) every dsh command **exits silently with no output**, which looks like a broken
install. So the wizard marks anything under 22.14 as unusable rather than letting you discover it the hard way.

Configuration lives in `%LOCALAPPDATA%\DSH Desktop\config.json` (the chosen Node path, dsh entry, port, …).
To re-run the wizard: tray menu → Environment settings, or delete that file and start again.

## Requirements

**Running** (end users):

| Dependency | Notes |
| --- | --- |
| Windows | 10 / 11 (x64) |
| WebView2 Runtime | Bundled with Win10/11; the wizard doesn't install it |
| Node.js | **≥ 22.14** — a portable build is installed by the wizard if missing or too old |
| dsh | `@deepseek-ai/dsh` — installed by the wizard if missing |

In other words, **a brand-new machine only needs the exe**: Node and dsh are the wizard's job.

**Building** (development):

| Dependency | Notes |
| --- | --- |
| Go | 1.21+ |
| Wails CLI | `v2.9.x` |
| gcc | Needed for the WebView2 binding (MinGW-w64 is fine) |

Tests: `go test .` for the probe-layer assertions; `go test -run TestDiagSetup -v .` prints the detected
environment so you can diff it against `where node` / `where dsh` / `netstat`.

## How it works

### The startup sequence

`dsh web` prints a one-shot entry URL to stdout:

```
dsh web: http://127.0.0.1:3388/?token=xxxxxxxx
```

The app scrapes that URL from the child process's stdout (the bare root path returns `401`, so you can't just hardcode `localhost:3388`).

**The catch:** you cannot navigate to that token URL from the splash page. dsh's session cookie is `HttpOnly; SameSite=Strict`, and the Wails splash page lives on `http://wails.localhost` — a *different site* from `http://127.0.0.1:3388` (SameSite only looks at scheme + registrable domain; **the port doesn't count**). A cross-site navigation drops the cookie, and you land on dsh's `401` page.

A `302` redirect doesn't help either — Chromium keeps the original initiator for the whole redirect chain.

So the app navigates in two steps, and **hides the window while doing it**:

1. Navigate to `http://127.0.0.1:<port>/` — this puts the document on dsh's own site;
2. Re-issue the token navigation *from that same-site document* — now the cookie is set and sent back properly.

The second step only fires if the page really is the auth-failure page (it checks `document.body.innerText`), so an already-authenticated session isn't disturbed. The window is hidden for roughly 1.5 s and then reappears showing the loaded UI — the user never sees the `401`.

Readiness is detected by checking whether the process listening on the port has any `ESTABLISHED` connections (the dsh UI opens a WebSocket once it loads). Note: **don't compare against `cmd.Process.Pid`** — on Windows the command goes through `cmd.exe`, and the actual listener is the child `node` process.

### Child process management

- Launched with `CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW` and `HideWindow`, so the `cmd.exe` shim never shows a console, and the whole tree can be killed with `taskkill /T /F`.
- Wrapped in a **Job Object** with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so the process tree is reaped no matter how the app dies.
- Exit is monitored, so if dsh dies unexpectedly the window is pulled back to the splash page with a retry button instead of leaving a dead page.

### System tray

Wails v2 has no tray API (checked v2.9 through v2.12), so this uses [`energye/systray`](https://github.com/energye/systray). Two things to watch: the package's `init()` only locks the main thread, so the tray goroutine needs its own `runtime.LockOSThread()`; and tray callbacks run on the tray's message-loop thread, so they're dispatched onto separate goroutines with a `recover`.

### Environment detection

**You cannot use `exec.LookPath` here.** `LookPath("node")` returns only the *first* hit on `PATH`, and machines
routinely carry several Node installs (a toolchain's bundled copy may sit earlier on `PATH`). Taking the first one
can produce the nonsense conclusion "Node is at A but dsh lives under B" — that exact situation exists on the
machine this project was developed on (two Nodes, different versions). So the wizard enumerates every `PATH` entry,
the usual install locations, and npm prefixes, then hands the list to the user.

**Whether a Node works is decided by a runtime probe, not by its version number.** dsh's `bin.js` uses
`import.meta.main`, added in Node **22.14**. Below that, every dsh command **exits silently with no output** —
no error, no log, just "clicking does nothing", which is brutal to diagnose. So on top of the version check the
wizard runs:

```bash
node --input-type=module -e "console.log(import.meta.main)"   # must print true
```

A single failed execution doesn't prove a Node is unusable either (AV scanning or a first-run block can make it
fail sporadically), so a failure is retried once before being reported — otherwise a perfectly good Node gets
flagged as broken and the user is pushed toward reinstalling it.

**dsh is detected by "it runs", not "the file exists".** A broken shim, a mismatch with the selected Node, or a
missing native dependency only shows up when you actually execute `dsh --version`.

**Two non-negotiables when installing dsh:**

```bash
{node}\node.exe {node}\node_modules\npm\bin\npm-cli.js install -g \
  --prefix "<prefix>" --registry=https://registry.npmmirror.com \
  --allow-scripts=@deepseek-ai/dsh-subprocess-local,koffi,node-pty,@google/genai,protobufjs \
  @deepseek-ai/dsh
```

Call the paired `node.exe + npm-cli.js` directly to bypass `PATH`; and keep `--allow-scripts`, or the postinstall
steps for native modules like `node-pty` / `koffi` never run — the install looks successful but terminals and
subprocesses are broken. Success is judged by the trailing `added N packages` line; the `npm warn` and
`cleanup failed` noise along the way is normal and must not be treated as failure.

**Portable Node stays in user space.** Download from a mirror (npmmirror first, official feed as fallback) →
verify the sha256 from `SHASUMS256.txt` → extract to a temp directory → atomic rename into place. No admin
rights at any point, deleting the folder uninstalls it, and downloads resume if interrupted.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `DSH_WEB_PORT` | `3388` | Port for `dsh web` to listen on. **Setting it locks the port** — the wizard's port field is disabled |
| `DSH_BIN` | auto-detected | Path to `dsh.cmd` / `dsh.exe` / `bin.js` |
| `DSH_DEBUG` | unset | Set to `1` to write a liveness heartbeat every 5 s |

Port precedence: `DSH_WEB_PORT` > `config.json` > default `3388`.

`%LOCALAPPDATA%\DSH Desktop\config.json` is written by the first-run wizard (delete it to re-run the wizard):

```json
{
  "setupCompleted": true,
  "nodePath": "D:\\soft\\nodejs\\node.exe",
  "nodeVersion": "22.23.2",
  "nodeSource": "existing",
  "npmPrefix": "D:\\soft\\nodejs",
  "dshBin": "D:\\soft\\nodejs\\node_modules\\@deepseek-ai\\dsh\\lib\\bin.js",
  "dshKind": "js",
  "dshVersion": "0.1.5-rc.1",
  "dshSource": "existing",
  "port": 3388,
  "mirror": "npmmirror"
}
```

Once the wizard has run, dsh is launched from **the exact `nodePath + dshBin` pair recorded here** instead of
guessing from `PATH` — which is precisely what keeps multiple Node installs from being mixed up.

Log file: `%LOCALAPPDATA%\DSH Desktop\app.log` — first place to look when something goes wrong.

## Credentials

This project **does not touch DeepSeek API keys**. Nothing here reads, stores or forwards model credentials; the key is managed by dsh itself (under `~/.dsh/`, whose `settings.yaml` only contains UI preferences).

The only "token" in the codebase is the **local session token** dsh prints at startup. It is valid only on `127.0.0.1`, is regenerated on every dsh restart, and exists only in runtime memory and the local log file. `//go:embed` embeds exactly two things: `frontend/dist` and `icon.ico`.

## Troubleshooting

**The wizard calls my Node "unusable" and says the version is too low** — dsh needs **Node ≥ 22.14**. Below that
(especially 22.0–22.13) every dsh command **exits silently with no output**, which looks like a broken install, so
the wizard blocks it deliberately. Hit "install Node automatically" for a portable build, or point it at a
`node.exe` that is 22.14 or newer.

**I have several Node installs — which one did the wizard pick?** — The wizard lists **every** node it finds on
`PATH`, in the usual install locations, and under the npm prefixes (it does not just take the first one) and probes
each. If dsh lives under a particular Node's global prefix, that one is selected automatically so the two can't be
mismatched; you can switch at any time from the list.

**"Install dsh automatically" failed** — the wizard streams the full npm log at the bottom. On failure it hands you
a copy-pasteable command; two things matter: call `npm-cli.js` with the **paired** node (never bare `npm` — with
several Nodes around, its global prefix may not be the one you want), and keep `--allow-scripts=...` (without it
the install looks fine but terminals and subprocesses are broken).

**Will the wizard mess up my existing setup?** — No. The rule is "exactly one dsh per machine": if a working dsh
already exists it is used as-is and nothing gets installed; if you have Node but no dsh, dsh goes into that Node's
own global prefix (alongside your other global packages); only when the wizard downloads its own portable Node does
it install under `%LOCALAPPDATA%\DSH Desktop\runtime\`, which uninstalls by deleting the folder. Nothing is ever
added to the system `PATH` and no existing install is modified.

**Re-running the wizard** — tray menu → Environment settings, or delete `%LOCALAPPDATA%\DSH Desktop\config.json`
and start the app.

**"dsh web authentication required"** — this page is never shown during normal startup (the window is hidden while switching). If you hit it: quit from the tray and reopen the app; if that fails, delete the WebView2 user data directory `%APPDATA%\DSH Desktop.exe` and start again.

**"Port already in use"** — a previous dsh is still running. Click retry on the splash page and it will terminate the occupant first.

## Design documents

| Document | Contents |
| --- | --- |
| `docs/setup-wizard-plan.md` | The wizard plan: form factor choice, the four steps, install strategy, pitfalls (Chinese) |
| `docs/setup-wizard-design.md` | Implementation-level design: data structures, method signatures, state machine, event protocol, error-code table, test checklist; §15 records where the implementation deviated and why (Chinese) |

## License

[MIT](LICENSE)
