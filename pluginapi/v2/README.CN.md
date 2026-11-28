# 插件 API v2

通用网络、登录会话、计时器、音频与文件操作见[宿主工具 API](TOOLS.CN.md)。

[包内 HTML 应用页、资源和原生数值/文本提交](PAGES.CN.md)

Windows x64 的 Lua 和 DLL 插件共用此协议。清单同时设置 `schemaVersion: 2` 和 `hostApiVersion: 2`，提供控件树时另加 `uiVersion: 2`。v1 包可以继续用原协议安装，DLL 的 C ABI 仍为 1。[UI 节点与公共构件](UI.CN.md) · [C ABI 头文件](../v1/quailcrate_plugin.h)

v1 和 v2 插件使用相同的 GitHub ZIP 打包格式及安装链接。[在 GitHub 发布插件](../v1/README.CN.md#在-github-发布插件)

## 从搜索保存到分组

1. 复制 `examples/notes` 整个目录到设置打开的用户插件目录，重新扫描并启用。工作区和发布包都提供此示例。
2. 在独立笔记窗口创建一条笔记。用快速启动搜索笔记文字或一个已安装应用，例如 OneNote。在选中的结果上打开右键菜单，选择“添加到桌面分组”，选已有分组或创建分组。
3. 从分组点击笔记即可请求笔记插件打开它。停用笔记插件后，已经保存的引用会显示来源不可用。重新启用后可继续使用。停用分组插件会撤下结果菜单项和笔记窗口的保存按钮。

[独立笔记示例](../../examples/notes/plugin.lua) · [桌面分组实现](../../plugins/desktop-groups/plugin.lua)

## 贡献与版本

| 入口 | 清单与返回值 |
| --- | --- |
| 搜索 | 在 `contributes` 声明 `quailcrate.search.provider`，`init` 返回 `search: true` 和可执行命令。搜索结果的 `command` 保存来源命令和参数。可选的 `item` 含 `source`、`kind`、`id`。展示与打开操作绑定查询修订和实例代次 |
| 结果菜单 | 声明 `quailcrate.result.actions`，`init.data.resultActions` 返回 `id,title,kinds,service,version,method`。可绑定本插件提供的服务。用户选择菜单项后宿主传入原结果的 `{item,title}` |
| 服务导出 | `provides` 数组中每项包含接口 `id`、精确的 `version` 和非空 `methods`。可导出本插件 ID 下的接口，或实现标准 `quailcrate.groups.items`、`quailcrate.items` |
| 服务使用 | `uses` 数组中每项含 `id,versionRange,methods,optional`。稳定版本使用三段整数，支持精确版本以及空格连接的 `>=`、`>`、`<=`、`<` 比较。方法授权随插件启用记录保存。可用绑定随请求的 `payload.services` 提供 |
| 通知 | 发布者在 `publishes` 声明本插件命名空间中的主题。订阅者在 `listens` 声明主题，启用时授权。发布动作含 `type:"event.publish",topic,args`。订阅者收到 `plugin.event`，其中含 `source,topic,revision,detail` |

结果来源使用插件自身 ID。具有 `apps.read` 授权的搜索提供者也能返回 Windows 应用目录引用 `{"source":"quailcrate.apps","kind":"app","id":"…"}`。宿主会核对当前目录。分组服务 1.0 支持 `app` 和 `note`，笔记源通过 `quailcrate.items/open` 解析自己的稳定 ID。

## 服务请求和回执

调用方在命令或事件响应的 `data.actions` 中提交以下动作。可选的 `provider` 绑定特定来源。没有指定来源并存在多个兼容提供者时，宿主在交互中让用户选择并记住绑定。

```json
{"type":"service.call","id":"save","service":"quailcrate.groups.items","method":"add","args":{"item":{"source":"example.notes","kind":"note","id":"1"},"title":"第一条笔记"}}
```

提供者收到 `op: "service"`。宿主填写 `payload.caller, service, serviceVersion, method, args`。提供者成功时可以随同自己的 `state` 返回 `data.reply`。宿主先提交状态，再投递回执。

```json
{"reply":{"status":"ok","value":{"groupId":"group-1","itemId":"item-1","added":true}}}
```

需要选择目标时，提供者第一轮只返回一个带 ID 的 `ui.pickOptions` 动作并释放执行权。选择后同一实例收到第二次 `service` 请求，原参数与调用者身份保持一致，`payload.event.items` 携带选项。取消直接向调用方返回 `canceled`。提供者第二轮须给出终态。单次插件执行上限为两秒，交互总时限为两分钟。

调用方收到的 `service.result` 事件含原 `actionId`、提供方 `source`、`status`，成功附 JSON `value`，失败附 `error`。常见错误码包括 `unavailable`、`incompatible_version`、`ambiguous_provider`、`permission_denied`、`timeout` 和 `call_cycle`。宿主按代次投递，旧实例的回执会被丢弃。写操作可用稳定业务键实现去重，宿主不会在丢失回执后自动重试。

## 托盘菜单

清单设置 `uiVersion: 2` 并声明 `contributes: ["tray"]`。插件在初始化时用当前语言填写 `data.tray`，每个插件最多提供 32 项。以下入口执行本插件同一份初始化响应中注册的命令，因此清单还要声明 `commands`。

```lua
local data = {
    commands = {{id="new", title="新建分组"}},
    tray = {{id="new-group", title="新建分组", icon="folder-plus",
             intent="command", command="new", order=20}}
}
```

菜单按 `order` 升序展示，范围为 −10000 到 10000，默认值为 0。同序号按插件 ID 排序，同一插件的同序号项保留声明顺序。`{separator=true, order=30}` 提供分隔线。系统意图支持 `launcher.open`、`launcher.choose`、`desktop.toggleEdit` 和 `settings.appearance`。快速启动入口显示当前快捷键，选择入口在有多个界面提供者时出现。桌面编辑入口可设置 `activeTitle` 作为编辑状态下的文字。宿主始终提供设置和退出入口。[托盘类型](tray.go) · [工作台插件](../../plugins/workbench/plugin.lua)

## 原生插件

每个原生实例运行在独立的 `QuailCrateNative.exe` Windows 工作进程中（仅保留 Job Object 退出清理与进程隔离，不再套用 AppContainer 权限沙箱），以当前用户完全系统权限原位加载入口 DLL 及同目录伴生动态库。可选导出 `quailcrate_bind_host` 允许插件从后台线程向宿主推送异步通知（`widget.refresh`、`page.notify`、`event.publish`、`state.set`）。随发布包的入口 DLL 和运行器继续校验 SHA-256；第三方原生插件使用作者自己的插件 ID，并在首次启用及二进制变更时由用户确认风险。详见[双层运行时规划](../../docs/plugin-runtime-tiering-plan.md)。

随包「桌面月历」（Lua）导出 `quailcrate.calendar.month`。其他插件可在清单中声明所需服务并调用其 `describe` 方法，传入可选的 `args.date`（`YYYY-MM-DD`）。省略日期时使用本地当天日期，成功回执的 `value` 含日期 `date` 和当月天数 `days`。

[原生音乐插件](../../plugins/music/native/music_plugin.cpp) · [发布与原生测试脚本](../../scripts/build.ps1)
