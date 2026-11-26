# AGENTS.md — QuailCrate AI Collaboration Guide

This file provides essential architectural rules, repository layout, build/test workflows, and engineering conventions for AI coding agents working in **QuailCrate** (`鹌鹑工具箱`).

---

## 1. Project Overview & Tech Stack

- **Target Platform**: Windows 11 `amd64` (`GOOS=windows`, `GOARCH=amd64`, **`CGO_ENABLED=0`**).
- **Host Language**: Go `1.27+` (`module quailcrate`). All Windows syscalls and COM integrations in the host use pure Go (`golang.org/x/sys/windows` and `syscall`) without Cgo.
- **UI Architecture**:
  - **Gio (`gioui.org`)**: Renders settings views and declarative plugin widget/tab control trees (`internal/gioui`).
  - **Win32 Shell & Windowing (`internal/winui`, `internal/winshell`)**: Manages system tray, global hotkeys, desktop widget pinning/Z-order, DPI scaling, file drag-and-drop, native dialogs, and WebView2-based app tab containers.
- **Persistence**: Pure-Go SQLite (`modernc.org/sqlite`) and all runtime caches/logs/WebView2 profiles stored locally under `./data/` in the current working directory (`./data/plugins/<plugin-id>` for per-plugin data, `./data/user-plugins/` for third-party installed plugins, `./data/cache/` for caches). Do **not** default to `%LOCALAPPDATA%` on `C:`. (`internal/store`).
- **Plugin Runtimes**:
  1. **Lua (`runtime: "lua"`)**: In-process `github.com/yuin/gopher-lua` (Lua 5.1) state per plugin.
  2. **Native DLL (`runtime: "dll"`)**: Out-of-process Windows x64 C ABI DLL hosted by companion runner `QuailCrateNative.exe` over anonymous pipes.

---

## 2. Core Architectural Mandate (READ FIRST)

> **Authoritative Plan**: Read [docs/plugin-runtime-tiering-plan.md](docs/plugin-runtime-tiering-plan.md) before modifying plugin runtime, permissions, or host APIs.  
> *(Note: Older sections in `docs/architecture.md`, `docs/plugin-platform-architecture.md`, and `README*.md` still describe the legacy AppContainer DLL sandbox; `docs/plugin-runtime-tiering-plan.md` supersedes them.)*

### 2.1 Two-Tier Plugin Model
1. **Simple Plugins -> Lua (`runtime: "lua"`)**:
   - Keep lightweight, zero-compile, and sandboxed via `gopher-lua`.
   - Uses existing Host API v1/v2 declarative UI and standard host actions (`http.request`, private `files.*`, `timer.*`, etc.).
2. **Complex Plugins -> Full-Permission Native DLL (`runtime: "dll"`)**:
   - Native DLL plugins are meant to run **unsandboxed with full current-user OS permissions** inside an isolated `QuailCrateNative.exe` worker process (crash/lock isolation via Job Object `KILL_ON_JOB_CLOSE`, **not** permission restriction).
   - Native DLLs can directly call any Win32/COM API, open sockets/WebSockets, spawn child CLI processes, read/write local files, load companion `.dll` dependencies from their plugin folder, and run background threads that push updates to the host.
   - Security for third-party DLLs is handled via **explicit UI labeling + user risk-warning confirmation** (bound to DLL SHA-256) on enable/update—never by crippling OS capabilities.

### 2.2 Strict Host API Boundary Rule ("Shell Only, Not an OS Proxy")
- **DO NOT** expand the Go host with new OS, hardware, media-decoding, custom network protocol, database, clipboard/hook, or subprocess proxy APIs just to satisfy a specific plugin's business logic.
- **Host APIs are strictly for QuailCrate shell & collaboration surfaces**:
  - Launcher search providers (`quailcrate.search.provider`) & result context actions (`quailcrate.result.actions`)
  - Desktop widgets & declarative v2 UI trees (`pluginapi/v2`)
  - Tray items (`tray`) & commands (`commands`)
  - App window tabs & packaged HTML/WebView message bridge (`appTabs` / `PAGES`)
  - Inter-plugin services & pub/sub events (`provides`, `uses`, `publishes`, `listens`)
- If a feature exceeds Lua's existing standard toolbox, implement it as a **Native DLL plugin** (or a Native DLL service plugin exporting `provides` for Lua UI plugins to consume).

---

## 3. Repository Layout

| Path | Responsibility |
| --- | --- |
| `cmd/quailcrate/` | Main Windows GUI executable (`QuailCrate.exe`) entry point and dev-mode source builder hook. |
| `cmd/quailcrate-native/` | Companion worker executable (`QuailCrateNative.exe`) that loads a single native plugin DLL over stdio pipes. |
| `pluginapi/v1/` | Public Plugin API v1 types, manifest/request/response structs, C ABI header (`quailcrate_plugin.h`), and docs. |
| `pluginapi/v2/` | Public Plugin API v2 declarative UI tree (`ui.go`, `ui.lua`), services/contracts, tray, app pages (`PAGES.CN.md`), and toolbox (`TOOLS.CN.md`). |
| `internal/plugins/` | Plugin discovery, manifest/action validation, per-instance serial scheduler (`Manager`), Lua runtime, and Native DLL process/worker runtime. |
| `internal/host/` | Host coordinator routing UI events, search queries, commands, cross-plugin services, app pages, tray items, and state commits. |
| `internal/gioui/` | Gio-rendered settings window, plugin management UI, and v2 declarative control tree layout/painting. |
| `internal/winui/` | Win32 windowing, desktop widget host, launcher popup, tray menu, dialogs, drag-and-drop, and WebView container. |
| `internal/winshell/` | Windows app catalog enumeration, icons/favicons, URL/path openers, and `IDesktopWallpaper` integration. |
| `internal/pluginapps/` | Packaged HTML app page asset serving, virtual origin isolation, and revisioned page-to-plugin message bridge. |
| `internal/pluginio/`, `pluginaudio/`, `pluginfiles/`, `musicmedia/` | Existing host toolbox implementations for Lua plugins (HTTP/cookies, MediaFoundation audio, scoped files). **Frozen against scope creep.** |
| `internal/store/` | SQLite persistence for host settings, widget layouts, plugin enablement/grants, and plugin state. |
| `plugins/` | Bundled first-party plugins (`launcher`, `calendar`, `desktop-groups`, `wallpaper`, `workbench` in Lua; `music` as a Native DLL). |
| `examples/` | Standalone third-party reference plugins (`lua-widget`, `notes`). |
| `web/` | Frontend source projects (e.g. `web/music` built with TypeScript + React + Vite + `pnpm` into `plugins/music/web`). |
| `scripts/` | PowerShell build and packaging scripts (`build.ps1`, `build-native.ps1`). |
| `docs/` | Architecture, UI specs, contracts, and the active runtime tiering plan (`plugin-runtime-tiering-plan.md`). |

---

## 4. Build, Run & Test Commands

Run commands from repository root (`F:\Projects\QuailCrate`) in PowerShell (`pwsh`):

```powershell
# Run development build directly (uses source plugins with -bundle if needed)
go run ./cmd/quailcrate
go run ./cmd/quailcrate -bundle ./plugins -data-dir .\build\dev-data

# Run Go unit tests (keep CGO_ENABLED=0)
$env:CGO_ENABLED = '0'
go test ./...

# Run targeted package tests
go test -v ./internal/plugins/...
go test -v ./internal/host/...

# Build web frontend assets (when modifying web/music)
pnpm --dir web/music build

# Full release package build -> dist/QuailCrate-windows-amd64.zip
# (Requires MSVC cl.exe x64 toolchain for music.dll and QuailCrateNative.exe)
.\scripts\build.ps1

# Build release + run native DLL integration tests
.\scripts\build.ps1 -TestNative
```

---

## 5. Engineering & Coding Conventions

1. **Pure Go Host (`CGO_ENABLED=0`)**:
   - Never introduce `import "C"` in Go packages. Use `golang.org/x/sys/windows` or `windows.NewLazySystemDLL` for Win32/COM APIs.
   - Keep OS-specific files suffixed with `_windows.go` (with minimal `_other.go` stubs only where cross-platform type-checking requires it).
2. **Instance Generations & Concurrency**:
   - Every enabled plugin run receives a monotonic `generation` (`Manager.Generation` / `CallGeneration`).
   - Any asynchronous callback, service reply, page message, or native push notification **must** validate the originating `generation` (and page `revision` where applicable) and silently drop stale deliveries after plugin reload/disable.
   - Win32 HWND message loops and STA COM calls must stay pinned to their owning OS thread (`runtime.LockOSThread`).
3. **Plugin IDs & Namespaces**:
   - `quailcrate` and `quailcrate.*` plugin IDs are reserved strictly for bundled plugins (`pkg.Bundled == true`). Third-party plugins and examples must use external namespaces (e.g., `example.*`, author domain).
4. **Localization & Diagnostics**:
   - UI-facing strings must support `en-US` and `zh-CN` (via `locales/en-US.json`, `locales/zh-CN.json`, and manifest `localizations`).
   - Internal error codes, log keys, and developer diagnostic messages use concise English.
5. **Key Reference Docs**:
   - Runtime Tiering & Full-Permission DLL Plan: [docs/plugin-runtime-tiering-plan.md](docs/plugin-runtime-tiering-plan.md)
   - Plugin API v2 Overview & Services: [pluginapi/v2/README.CN.md](pluginapi/v2/README.CN.md)
   - Declarative UI v2 Controls: [pluginapi/v2/UI.CN.md](pluginapi/v2/UI.CN.md)
   - Packaged Web Pages Bridge: [pluginapi/v2/PAGES.CN.md](pluginapi/v2/PAGES.CN.md)
   - Lua Toolbox Actions: [pluginapi/v2/TOOLS.CN.md](pluginapi/v2/TOOLS.CN.md)
   - Plugin API v1 & C ABI Header: [pluginapi/v1/README.CN.md](pluginapi/v1/README.CN.md) · [pluginapi/v1/quailcrate_plugin.h](pluginapi/v1/quailcrate_plugin.h)
