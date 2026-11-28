# 鹌鹑工具箱公共插件 API v1

[English](README.md)

随包插件和本地安装的插件使用相同的消息、组件模板和宿主动作。Lua 开发者可以从 [示例计数器](../../examples/lua-widget/) 开始，Windows x64 原生插件可参考随包 [音乐插件](../../plugins/music/)。[协议字段](types.go) 使用 UTF-8 JSON 编码。

## 插件包

每个插件目录包含 UTF-8 的 `manifest.json` 和相对入口文件。以下是一个可以放入用户插件目录的最小清单。

```json
{
  "schemaVersion": 1,
  "id": "example.counter",
  "name": "示例计数器",
  "version": "1.0.0",
  "runtime": "lua",
  "entry": "plugin.lua",
  "hostApiVersion": 1,
  "contributes": ["widgets"],
  "permissions": []
}
```

- ID 使用小写字母、数字、点、连字符或下划线。各段以字母或数字开头，整个 ID 保持唯一。`quailcrate` 和 `quailcrate.*` 保留给随包插件。`version` 采用语义化版本，同一插件的各个版本使用相同 ID。
- `contributes` 支持 `search`、`commands`、`widgets` 和 `appTabs`。在清单中声明插件通过 `init` 返回的贡献类别。
- 可选的 `icon` 用于插件列表，支持 `search`、`folder`、`calendar`、`image` 和 `code`。其他值使用通用插件图标。
- 可选的 `homepage` 为不超过 512 字节的 HTTPS 地址。插件列表据此提供「主页」按钮。
- 可选的 `minHostVersion` 为最低宿主版本，采用语义化版本格式，例如 `0.3.0`。从 GitHub 安装时会检查当前正式版是否满足要求。开发构建默认放行，并将版本要求写入日志。
- `permissions` 支持 `apps.read`、`apps.launch`、`paths.open` 和 `wallpaper.set`。用户安装的 Lua 插件在首次启用时授权。
- 从托盘打开插件设置中的「打开插件目录」，把插件包目录复制到该目录，再重新扫描并启用。默认安装目录为 `./data/user-plugins/`。原生插件的数据目录为 `./data/plugins/<插件ID>`，Lua 插件通过 [`files.*` 访问私有文件](../v2/TOOLS.CN.md)。

## 在 GitHub 发布插件

### Release ZIP 附件

发布 GitHub Release，并上传包含 `manifest.json`、`entry` 指定的相对入口文件、语言文件、资源和运行依赖的 ZIP。原生插件需编译 Windows x64 DLL，并打包所需的伴生 DLL 或可执行文件。使用包内网页的插件需构建并打包 `web/`。纯 Lua 插件也可以用此格式发布。

将 `manifest.json` 放在 ZIP 根目录或唯一的顶层文件夹内。忽略 `.DS_Store` 和 `__MACOSX` 后，包裹文件夹应是唯一的顶层项目。采用 Windows 兼容路径，避免符号链接或仅大小写不同的路径。每个 ZIP 最多包含 2,000 个文件，压缩包上限为 128 MiB，解压上限为 256 MiB。

优先使用 `my-plugin.qcplugin.zip` 作为附件名，其次为 `*-windows-amd64.zip` 或 `*_windows_amd64.zip`。若 Release 只上传了一个 ZIP，则可使用任意文件名。若多个附件的最高优先级相同，请分享目标 ZIP 的下载直链。可以同时上传名为 `<附件名>.sha256` 的校验文件，将 ZIP 的 SHA-256 摘要写在以空白分隔的首个字段，使用 64 个十六进制字符。安装器通过 Release 元数据选择附件时会校验该摘要。

### Lua 插件源码归档

可直接运行的 Lua 插件可将 `manifest.json` 和入口文件放在仓库根目录或专用子目录。通过仓库链接安装时，先查询最新 Release，收到 404 后再从默认分支的源码归档安装。仓库内含多个插件时，请分享指向目标 `manifest.json` 所在目录的 `/tree/` 链接。

### 安装链接

用户可以在 **设置 → 插件 → 从 GitHub 安装…** 中输入下列链接。表中的路径示例接在 `https://github.com/owner/repo` 后面。

| 链接或路径后缀 | 安装来源 |
| --- | --- |
| `owner/repo` 或仓库主页 | 最新 Release，纯 Lua 插件可回退到默认分支源码 |
| `/releases` 或 `/releases/latest` | 最新 Release |
| `/releases/tag/v1.2.0` | 指定 Release，未上传任何附件时纯 Lua 插件可回退到对应标签的源码 |
| `/releases/download/v1.2.0/my-plugin.qcplugin.zip` | 指定 ZIP 附件，无需查询 Release API |
| `/tree/main` | 分支、标签或提交的源码归档，无需查询 Release API |
| `/tree/main/examples/lua-widget` | 源码归档内的指定子目录 |

可以用 `/tree/refs/heads/<ref>` 指定分支，或用 `/tree/refs/tags/<ref>` 指定标签。将引用名称内的斜杠编码为 `%2F`，例如 `/tree/refs/heads/feature%2Fwidgets/examples/lua-widget`。

## 界面语言

插件列表显示清单中的 `name` 和可选单行 `description`。可通过 `localizations` 为 `en-US` 和 `zh-CN` 分别提供名称和说明。某个字段缺少翻译时，宿主使用清单顶层的对应字段。

```json
"localizations": {
  "en-US": { "name": "Example Counter" },
  "zh-CN": { "name": "示例计数器" }
}
```

宿主在每次请求中提供当前 `payload.locale`。插件可在包目录内提供可选的 `locales/en-US.json` 和 `locales/zh-CN.json`，每个文件为 UTF-8 字符串键值对象。宿主以英文文件为基础，用当前语言的同名键覆盖，并在 `payload.strings` 中提供结果。该映射可同时用于 `init` 的贡献标签、设置命令和组件内容。插件切换语言后继续使用相同的命令和组件 ID，错误与诊断消息使用英文。

## 请求与响应

每个请求由 `requestId`、`op`、`payload` 组成。响应回传相同的 `requestId`，以 `ok: true` 提交 `data`，或以 `ok: false` 返回含 `code` 和 `message` 的 `error`。以下是 `init` 的 Lua 实现。

```lua
return function(request)
    local data = {}
    if request.op == "init" then
        data.widgets = {
            { id = "sample", title = "示例", width = 240, height = 150, default = true }
        }
    elseif request.op == "widget_state" then
        data.view = { title = "示例", controls = {
            { id = "hello", type = "text", text = "来自插件" }
        } }
    end
    return { requestId = request.requestId, ok = true, data = data }
end
```

| 操作 | 请求和返回 |
| --- | --- |
| `init` | 接收 `hostApiVersion` 和插件上下文。返回 `search: true` 启用搜索，或返回 `commands`、`widgets`、`appTabs`、`subscribe` 声明各项贡献。 |
| `search` | 接收 `text` 和 `limit`。返回 `results`，每项包含 `id`、`title`、可选 `subtitle`、`score` 以及引用已声明命令的 `command: {id, args}`。 |
| `command` | 接收 `commandId` 和可选 `args`。命令定义包含 `id`、`title` 和可选的 `location`。`settings` 与 `desktop-edit` 均将命令列在「指令」页。搜索结果中的命令使用 `search` 或省略该字段。 |
| `widget_state` | 接收 `widgetId`、`widgetType`。返回 `view`，其中 `title` 为窗口标题，`controls` 为顺序排列的控件。 |
| `tab_state` | 接收已注册的 `tabId`，返回该应用标签页当前的只读 `view`。 |
| `event` | 接收 `event` 及相关上下文。可返回更新的 `state` 和宿主动作 `actions`。 |
| `stop` | 插件贡献撤下后调用，用于结束该实例。 |

宿主提供插件自己的 `state`、本地日历日期 `date`（`YYYY-MM-DD`）及组件实例列表 `widgets: [{id, type}]`。具备 `apps.read` 的插件可读取 `apps: [{id, name, source}]`。应用来源使用稳定的 `start-menu` 和 `packaged` 标识，插件按界面语言生成标签。返回顶层 `data.state` 时替换插件原有状态，省略该字段时保留。搜索和组件视图请求用于查询，用户动作及事件用来提交变更。

Lua 入口返回单一函数，入参和响应都是表。普通对象用字符串键，数组用从 1 开始的连续整数键。空表默认为对象，用 `json.array()` 表示空数组，`json.null` 表示 JSON 空值。宿主限定可用 Lua 库及调用时间，系统操作由公共动作提供。

## 声明式组件

组件定义包含 `id`、`title`、`width`、`height`。尺寸使用 DIP。可选 `anchor` 为 `left` 或 `right`。为 `default` 设置 `true` 可由宿主创建默认实例，该实例 ID 等于组件定义 ID。每个实例的布局和移动由宿主管理。

| 控件 `type` | 可用字段 |
| --- | --- |
| `text` | `id`、`text`。 |
| `button` | `id`、`text`，点击时返回控件 ID。 |
| `list` | `id`、可选空列表提示 `text`、`items`、`drop`、`reorder`。 |
| `grid` | `id`、`items`、`columns`。 |

每个条目包含 `id`、`text`，可附加 `hint`，并用 `state` 的 `current`、`muted` 或 `error` 表示视觉状态。用稳定的控件和条目 ID 处理交互。

- `widget.action` 包含 `widgetId`、`controlId` 和可选的 `itemId`。
- `widget.drop` 包含目标 `widgetId`、`controlId`、路径数组 `paths` 及从零开始的插入位置 `to`。
- `widget.reorder` 包含 `widgetId`、`controlId`、可选 `itemId` 及从零开始的 `from` 和 `to`。
- 在 `subscribe` 中声明 `date.changed` 或 `apps.changed` 后收到对应通知。应用通知要求 `apps.read`。

## 应用标签页

在清单中声明 `"appTabs"`，并在 `init` 的 `data` 中按展示顺序返回标签页。例如，`{"appTabs":[{"id":"main","title":"概览"}]}` 注册一个入口。每页使用插件内唯一且稳定的 `id`、当前语言的单行标题 `title` 和可选的 `icon`。宿主现已支持注册、查询和交互；「插件 → 应用」菜单与独立窗口待 UI 接入。随包的 `examples/lua-widget` 提供双 Tab 示例。

收到 `tab_state` 时，按 `payload.tabId` 返回 `data.view`，其中 `controls` 按显示顺序排列。应用标签页支持文本、按钮、列表和网格。按钮、列表项和网格项均可点击。组件专用的菜单、拖放、重排、标题栏布局及悬停显示等选项只适用于桌面组件。标签页查询为只读操作。

用户点击时，宿主发送 `event` 请求。事件类型为 `tab.action`，包含 `event.tabId` 和 `event.controlId`。点击列表项或网格项时还包含 `event.itemId`。插件在该事件响应中提交状态和宿主动作。文本输入和文件选择对话框要求用户交互，系统操作沿用既有权限检查。Lua 与原生插件使用相同的 JSON 字段。

## 宿主动作

将动作放在 `data.actions`。每项包括 `type` 及下表字段。组件增删和同一响应中的状态替换由宿主一并提交。

| 动作 | 字段和权限 |
| --- | --- |
| `widget.create` | 传入分配给新实例的稳定 `widgetId` 及已声明的 `widgetType`。 |
| `widget.delete`、`widget.refresh` | 传入插件自己拥有的 `widgetId`。 |
| `ui.promptText` | `id`、`title` 及可选的初始 `value`。从用户触发的命令或组件交互请求。 |
| `ui.pickPaths` | `id`、`title`、可选 `extensions`、`multiple` 和 `allowDirectories`。扩展名省略前导点。 |
| `ui.message` | `title` 和显示内容 `value`。由用户触发的操作可用它报告结果。 |
| `apps.launch` | 动作 `id` 和应用目录中的 `appId`。要求 `apps.launch`。 |
| `paths.open` | 动作 `id` 和 `path`。要求 `paths.open`。 |
| `wallpaper.set` | 动作 `id` 和本地 JPG 或 PNG 的 `path`。要求 `wallpaper.set`。 |

对话框和带 `id` 的系统动作通过 `action.result` 回传。用 `actionId` 对应同名动作的 `id`。宿主保留原始交互的组件、控件和条目 ID，并在 `previous` 中提供原事件。文本结果在 `value`，选中的项目在 `paths`，用 `canceled` 区分取消，用 `error` 读取失败说明。插件诊断消息使用英文。

## Windows x64 原生 ABI

DLL 清单填写 `runtime: "dll"`、`platform: "windows/amd64"` 和 `abiVersion: 1`（或 `2`）。用 [公共头文件](quailcrate_plugin.h) 导出 C 接口。输入和输出均为 UTF-8 JSON，`quailcrate_invoke` 返回零表示得到协议响应，非零表示调用失败。插件分配响应缓冲区、填写确切字节长度，宿主复制后用 `quailcrate_free` 释放。请求和响应上限均为 4 MiB。插件还可导出可选的 `quailcrate_bind_host(quailcrate_host_notify_fn)` 接收线程安全的宿主异步推送回调，从而在后台线程主动发送 `widget.refresh`、`page.notify`、`event.publish` 或 `state.set` 通知。

第三方 DLL 与 Lua 使用同一插件目录，使用作者自己的 ID 命名空间。每个已启用的 DLL 独占一个独立的 `QuailCrateNative.exe` 工作进程（用于崩溃与文件锁隔离，并随宿主 Job Object 自动清理子进程树），以当前桌面用户的完全系统权限在插件目录原位运行，可直接访问网络、文件系统、Win32/COM 接口、启动子进程及加载同目录伴生 `.dll` 依赖。宿主会通过环境变量 `QUAILCRATE_PLUGIN_ID`、`QUAILCRATE_PLUGIN_DIR` 和 `QUAILCRATE_PLUGIN_DATA_DIR` 提供上下文。随包 DLL 和运行器在发布版中校验 SHA-256；第三方 DLL 在首次启用及二进制哈希变更时需经用户风险确认。运行 `scripts/build.ps1 -TestNative` 可构建并验证原生运行器、全权限能力、伴生库加载、异步推送与进程隔离。
