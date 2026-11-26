# 插件 UI 协议 v2

Lua 与 Windows DLL 可以提交相同的 JSON 控件树。插件负责组织内容、提供本地化文案和处理动作。宿主负责窗口、DPI 换算、控件绘制、命中测试和输入分发。随包的「桌面月历」用工具栏和七列网格绘制桌面组件。[Lua 月历](../../plugins/calendar/plugin.lua)

## 声明与返回视图

在清单中设置 `schemaVersion: 2`、`hostApiVersion: 2` 和 `uiVersion: 2`。`contributes` 可声明 `widgets`、`appTabs` 或 `launcher`。桌面组件与应用页在 `init` 注册实例类型或标签页，随后分别在 `widget_state` 和 `tab_state` 返回 `data.view`。视图由 `uiVersion`、`title` 和一个 `root` 组成。桌面还可提供 `summary`、`tabs`、`activeTab` 和 `menu`。宿主继续接收 v1 插件的平铺 `controls`。[旧版消息格式](../v1/README.CN.md)

下例是可同时用于桌面和应用页的视图响应。

```json
{
  "requestId": "42",
  "ok": true,
  "data": {
    "view": {
      "uiVersion": 2,
      "title": "导入进度",
      "root": {
        "type": "column",
        "id": "import-page",
        "props": { "gap": 6 },
        "children": [
          { "type": "text", "id": "summary", "props": { "text": "已导入 12 / 20 项" } },
          { "type": "progress", "id": "completion", "props": { "value": 0.6, "height": 3 } },
          { "type": "button", "id": "again", "props": { "text": "重试" }, "events": { "activate": "import.retry" } }
        ]
      }
    }
  }
}
```

用户按下按钮后，插件接收 `widget.action` 或 `tab.action`，其中 `controlId` 为 `import.retry`。同一插件可在两个窗口中使用 `again` 作为节点 ID，按 `widgetId` 或 `tabId` 区分各自的状态。应用页的动作还会经过宿主的页面修订检查。

## 基础控件

| 节点 | 用法 |
| --- | --- |
| `row`、`column` | 按顺序布置 `children`。`gap` 为子控件间距，`grow` 按权重分配主轴的剩余空间。行内使用 `width`，列内使用 `height` 指定固定尺寸。尺寸和间距以逻辑像素计。 |
| `text` | `props.text` 是显示文字。文字变体 `accent`、`muted`、`warning` 使用主题颜色，`caption` 适合摘要。 |
| `icon` | `props.icon` 使用宿主图标名，例如 `calendar`、`folder`、`plus`。 |
| `button` | 提供 `text` 或 `label`，可带 `icon`、`variant: "accent"`。使用 `events.activate` 绑定动作，桌面按钮也可以通过 `props.menu` 打开一个菜单。 |
| `grid` | `columns` 为 1–32。可用 `children` 排列任意基础节点，也可用 `props.items` 显示带稳定键的格子。带项目的网格通过 `events.activate` 绑定动作。 |
| `virtualList` | `props.items` 提供文字、图标、提示和徽标，`events.activate` 处理激活。桌面用 `columns` 大于 1 切换图标平铺模式，宿主按可用宽度排列。应用页使用可滚动的项目行。桌面列表可声明 `drop`、`reorder`。 |
| `progress` | `props.value` 是 0 到 1 之间的数。使用独立节点能把进度条放在任意容器中。 |
| `searchInput` | 快速启动浮层中的原生搜索输入框，由宿主管理输入焦点与 IME。 |

每个业务项在所属集合内有唯一的 `key`。可提供 `text`、`label`、`hint`、`badge` 和 `icon`。网格表头使用 `role: "columnheader"`。项目的 `variant` 可取 `accent`、`selected`、`muted`、`secondaryAccent` 或 `warning`。项目激活事件包含绑定的 `controlId` 和原始 `key` 作为 `itemId`。拖放、重排沿用桌面事件中的节点 ID、项目 ID 以及位置字段。[分组插件](../../plugins/desktop-groups/plugin.lua)

节点 ID 和绑定的动作名使用 ASCII 字母、数字、连字符、下划线和分段点号。每段以字母或数字开头，最多 128 字节。ID 在整棵树中唯一。每棵树最多 32 层、512 个节点和 8192 个业务项，每个集合最多 2048 项。插件应在业务动作后返回新的视图，沿用仍然有效的节点 ID 和项目键。[协议类型与校验](ui.go)

## 桌面工具栏、菜单与滚轮

在根容器的直接子节点中放一个 `row`，并设置 `props.placement = "header"`，即可将其中的按钮放进原生窗口标题栏。标题栏按钮支持 `hoverOnly`。普通应用页将同一个 `row` 放在内容区。桌面视图可用根节点的 `events.wheelUp` 和 `events.wheelDown` 绑定动作。滚轮指向列表时优先滚动该列表。

`view.menu` 是菜单项数组。`type` 支持 `button` 和 `separator`。按钮使用 `id`、`text` 和可选的 `icon`、`checked`、`danger`。默认条目出现在组件右键菜单中。`itemOnly` 条目出现在项目菜单中，`placement: "tab"` 条目出现在标签菜单中。其他 `placement` 值由插件命名，与触发按钮的 `props.menu` 匹配。`props.menu: "context"` 打开组件上下文菜单。用户选中条目时，`widget.action.controlId` 为菜单项 ID。

## 快速启动浮层

启动器在 `init` 中注册 `data.launcher`，其中包括 `uiVersion: 2`、窗口标题、根 `column` 和可选的 `indexing`、`noResults` 提示。根节点的 `props.width` 为 240–2048，最多 12 个直接子节点。完整实现可参照内置启动器。[启动器插件](../../plugins/launcher/plugin.lua) · [启动器校验](launcher.go)

| 组成 | 约定 |
| --- | --- |
| 一个 `searchInput` | 高度 40–128，`label` 提供占位文案，`text` 提供前缀。将 `change` 绑定到 `search.query`，可将 `submit` 绑定到 `search.open`。 |
| 一个 `virtualList` | 设置 `source: "results"` 与 `visibleRows`（1–16）。将 `activate` 绑定到 `search.open`。可用 `contextMenu: "search.actions"` 启用结果菜单。 |
| 结果模板 | 列表包含一个高度为 24–128 的 `row`，可嵌套行和列。文字的 `field` 可绑定 `title`、`subtitle`、`badge` 或 `selection`，也可显示静态 `text`。最多一个图标节点绑定 `field: "icon"`。 |
| 附加文字 | 根下的 `text` 节点高度为 12–128，可提供快捷键说明等插件文案。 |

## Lua 构造函数与 DLL

### 设置与托盘

管理插件可在清单的 `contributes` 声明 `settings` 和 `tray`。初始化响应的 `data.settings` 提供 `uiVersion: 2`、窗口标题 `title`、应用菜单标题 `appsTitle` 和有序的 `sections`。每个分区包含 `id,title,description,icon,cards`。卡片的 `type` 选择原生设置控件，`title` 由插件提供。可用类型为 `hotkey`、`language`、`target`、`presets`、`opacity`、`colors`、`desktopLayout`、`commands`、`plugins`。宿主优先使用随包工作台。停用后会选择按插件 ID 排序的第一个可用提供者，并始终保留恢复插件管理的入口。

`data.tray` 接收有序菜单项，插件可声明本地化的 `title`、`icon`、`order` 和点击意图 `intent`。命令项用 `intent: "command"` 与本插件已注册的 `command` 关联。宿主合并多个来源并在点击时验证实例，设置与退出入口由宿主提供。可复用的清单和页面见[默认工作台](../../plugins/workbench/plugin.lua)和[桌面分组](../../plugins/desktop-groups/plugin.lua)。

### 构造控件树

Lua 插件可以用 `require("quailcrate.ui.v2")` 引入宿主随附的模块。模块提供 `node(kind, id, props, events, children)`、`toolbar(id, buttons, in_header)`、`button(id, label, icon, action, variant)` 和 `menu_button(id, label, icon, menu, hover_only)`。它们返回普通 Lua 表，可以继续组合、修改或作为局部组件复用。[模块源码](ui.lua)

Windows DLL 在原有 ABI 的响应缓冲区输出相同的 UTF-8 JSON。DLL 可以独立使用 C/C++ 的 JSON 库或构造函数。包清单的 `abiVersion` 为 1 或 2，UI 协议版本由 `uiVersion` 指定。[ABI 头文件](../v1/quailcrate_plugin.h) · [原生音乐插件入口](../../plugins/music/native/music_plugin.cpp)

## 用户安装插件的图标资源权限

搜索结果、网格和列表项目、选项选择框中的 `icon` 都可以留空。宿主在接收 Lua 或 DLL 的响应时检查图标，再将获准的资源交给图标缓存。缓存中已经存在的图标也遵循同一授权。按钮和菜单使用的 `calendar`、`folder` 等符号名由宿主本地绘制。

| 资源 | 所需授权与格式 |
| --- | --- |
| 符号图标 | 无需额外授权。名称以 ASCII 字母开头，随后可使用字母、数字、连字符和下划线，总长不超过 64 字节。 |
| 已安装应用 | `apps.read`。可返回本次请求中 `payload.apps` 给出的 `icon` 或应用 `id`。宿主将 ID 解析为对应的目录图标，包括已登记的 `shell:AppsFolder` 条目。 |
| 本地文件图标与图像 | `paths.open`。使用绝对盘符路径，例如 `C:\Icons\sample.ico`，或带 `imgfile:` 的本地图像路径。用户安装的插件可使用本地盘符路径及宿主发出的目录图标。 |
| 网站图标 | `urls.open`。使用 `favicon:https://example.org/page` 或 HTTP 地址，网址需包含主机且不带身份凭据。`icons.refresh` 的资源键也经过校验。 |

缺少资源授权时，该次响应会得到 `permission_denied`。仅使用宿主发放的应用图标时，声明 `apps.read` 即可。[管理器授权检查](../../internal/plugins/icon_permissions.go)
