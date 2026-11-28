# QuailCrate Plugin API v1

[Simplified Chinese](README.CN.md)

All packages share the same request protocol, widget templates, and host actions. Use the Lua counter or the standalone native Music plugin as an example. The protocol types define the UTF-8 JSON fields. [Lua counter](../../examples/lua-widget/) · [Native Music plugin](https://github.com/SuperQuail/quailcrate-music) · [Protocol types](types.go)

## Package format

Give each plugin a directory with a UTF-8 `manifest.json` and a relative entry file.

```json
{
  "schemaVersion": 1,
  "id": "example.counter",
  "name": "Example Counter",
  "version": "1.0.0",
  "runtime": "lua",
  "entry": "plugin.lua",
  "hostApiVersion": 1,
  "contributes": ["widgets"],
  "permissions": []
}
```

- IDs use lowercase letters, digits, periods, hyphens, and underscores. Each dot-separated segment starts with a letter or digit. The full ID is unique. `quailcrate` and `quailcrate.*` are reserved for bundled plugins. Use semantic versioning for `version` and keep the same ID across releases.
- Declare the contribution categories used by `init` in `contributes`. The supported categories are `search`, `commands`, `widgets`, and `appTabs`.
- The optional `icon` selects a symbol for the plugin list. Supported values are `search`, `folder`, `calendar`, `image`, and `code`. The list uses a generic plugin symbol for other values.
- The optional `homepage` is an HTTPS URL of up to 512 bytes. The plugin list uses it for the **Homepage** button.
- The optional `minHostVersion` is a semantic version such as `0.3.0`. GitHub installation checks this minimum against the running QuailCrate release. Development builds allow installation and log the requirement.
- Supported permissions are `apps.read`, `apps.launch`, `paths.open`, and `wallpaper.set`. Users approve the permissions when they first enable a locally installed Lua plugin.
- Use **Open plugins folder** in the plugin settings to install a package manually. Copy the package directory into it, rescan, and enable the plugin. The default is `./data/user-plugins/`. Native plugins receive `./data/plugins/<plugin-id>` as their data directory. Lua plugins use [`files.*` for private files](../v2/TOOLS.CN.md).

## Publishing on GitHub

### Release ZIPs

Publish a Release and upload a ZIP containing `manifest.json`, the relative `entry` file, and any locales, assets, or other runtime dependencies. For native packages, compile the Windows x64 DLL and include any companion DLLs or executables. Build and include `web/` when the plugin uses packaged web pages. Lua-only plugins can also use this format.

Put `manifest.json` at the ZIP root or directly inside the single top-level directory. A wrapper directory must be the only top-level item after ignoring `.DS_Store` and `__MACOSX`. Use Windows-compatible paths with no symbolic links or paths that differ only by case. Each archive can contain up to 2,000 files, 128 MiB compressed, and 256 MiB extracted.

Name the uploaded asset `my-plugin.qcplugin.zip` for first priority. The installer next looks for `*-windows-amd64.zip` or `*_windows_amd64.zip`, then accepts any filename if the release has a single uploaded ZIP. If multiple assets tie for first choice, share the exact ZIP download link. You can also upload a sidecar named `<asset>.sha256` with the ZIP's 64-character hexadecimal SHA-256 as the first whitespace-delimited field. The installer verifies it when selecting an asset from Release metadata.

### Source archives for Lua plugins

For a Lua plugin that runs directly from source, keep `manifest.json` and the entry file in the repository root or in a dedicated plugin subdirectory. A repository link tries the latest Release and falls back to the default branch if the latest Release lookup returns 404. For repositories with multiple plugins, share a `/tree/` link that points to the directory containing the intended `manifest.json`.

### Install links

Share one of these links for **Settings → Plugins → Install from GitHub…**. The path examples below follow `https://github.com/owner/repo`.

| Link or suffix | Installs from |
| --- | --- |
| `owner/repo` or the repository URL | Latest Release, with default-branch fallback for a Lua source package |
| `/releases` or `/releases/latest` | Latest Release |
| `/releases/tag/v1.2.0` | Tagged Release, with tagged source fallback for a Lua package if the Release has no uploaded assets |
| `/releases/download/v1.2.0/my-plugin.qcplugin.zip` | Exact ZIP asset, with no Release API lookup |
| `/tree/main` | Source archive for a branch, tag, or commit, with no Release API lookup |
| `/tree/main/examples/lua-widget` | The named subdirectory in the source archive |

Use `/tree/refs/heads/<ref>` for an explicit branch and `/tree/refs/tags/<ref>` for a tag. Encode any slashes inside a ref as `%2F`, as in `/tree/refs/heads/feature%2Fwidgets/examples/lua-widget`.

## Interface language

The plugin list shows the manifest's `name` and optional single-line `description`. The optional `localizations` map accepts `name` and `description` per locale. The host supports `en-US` and `zh-CN` and falls back to the top-level fields individually.

Every request includes `payload.locale` and `payload.strings`. To use the shared translation loader, provide UTF-8 string maps in `locales/en-US.json` and `locales/zh-CN.json`. The host loads the English map and applies the selected locale over it. Use the merged map for contribution names, settings options, and widget labels. Keep identifiers stable between languages and write errors and diagnostics in English.

## Requests and responses

Each request has a `requestId`, an `op`, and a `payload`. Echo the request ID and return `ok: true` with `data`, or `ok: false` with an `error` containing `code` and `message`.

```lua
return function(request)
    local data = {}
    local title = request.payload.strings.title or "Example"
    if request.op == "init" then
        data.widgets = {
            { id = "sample", title = title, width = 240, height = 150, default = true }
        }
    elseif request.op == "widget_state" then
        data.view = { title = title, controls = {
            { id = "hello", type = "text", text = "Hello from a plugin" }
        } }
    end
    return { requestId = request.requestId, ok = true, data = data }
end
```

| Operation | Input and output |
| --- | --- |
| `init` | Receives `hostApiVersion` and plugin context. Return `search: true` for search and declare `commands`, `widgets`, `appTabs`, and `subscribe` as needed. |
| `search` | Receives `text` and `limit`. Return `results` with `id`, `title`, optional `subtitle`, `score`, and a `command` object with its declared `id` and optional `args`. |
| `command` | Receives `commandId` and optional `args`. Command definitions have an `id`, a `title`, and an optional `location`. Both `settings` and `desktop-edit` add the command to the Commands page. Use `search` or omit the location for a search-result command. |
| `widget_state` | Receives `widgetId` and `widgetType`. Return a `view` with a `title` and an ordered `controls` array. |
| `tab_state` | Receives a registered `tabId`. Return the current read-only `view` of this application tab. |
| `event` | Receives an `event` with its plugin context. Can return updated `state` and host `actions`. |
| `stop` | Runs after the host removes the plugin's contributions. |

The host supplies the plugin's own `state`, local `date` in `YYYY-MM-DD` form, and widget instances as `widgets: [{id, type}]`. The `apps.read` grant adds `apps: [{id, name, source}]`. The source keys are `start-menu` and `packaged`. Return `data.state` to replace stored state. Search and view requests are read-only.

The Lua entry returns one function that accepts and returns a table. Use string keys for objects and consecutive one-based integers for arrays. Represent an empty array with `json.array()` and a JSON null with `json.null`. An unmarked empty table represents an object. Host actions provide the operating-system operations.

## Widgets and events

Widget definitions have an `id`, `title`, `width`, and `height`. Sizes are in DIP. The optional `anchor` is `left` or `right`. Set `default: true` to create an instance on the first successful initialization. Its instance ID matches the definition ID. The host saves window positions.

| Control `type` | Fields |
| --- | --- |
| `text`, `button` | `id` and `text` |
| `list` | `id`, `items`, optional empty-state `text`, `drop`, and `reorder` |
| `grid` | `id`, `items`, and `columns` |

Items have an `id`, `text`, optional `hint`, and an optional visual `state` of `current`, `muted`, or `error`.

- `widget.action` supplies `widgetId`, `controlId`, and an optional `itemId`.
- `widget.drop` supplies the target widget and control IDs, a `paths` array, and a zero-based insertion index `to`.
- `widget.reorder` supplies the widget and control IDs, optional `itemId`, and zero-based `from` and `to` indices.
- Add `date.changed` or `apps.changed` to `subscribe` to receive notifications. Application catalog events require `apps.read`.

## Application tabs

Declare `"appTabs"` in the manifest and return the tabs in display order during `init`. For example, `{"appTabs":[{"id":"main","title":"Overview"}]}` is a valid `data` object. Each tab has a stable plugin-local `id`, a localized single-line `title`, and an optional `icon`. The host supports registration, view queries and interaction. The **Plugins → Applications** menu and independent window are pending UI integration. The bundled `examples/lua-widget` demonstrates two tabs.

On `tab_state`, read `payload.tabId` and return `data.view` with an ordered `controls` array. Tabs support text, buttons, lists, and grids. Use a button click or list/grid item click for interactions. Widget menus, drop, reorder, placement, and widget-only display flags are unsupported in tab views. Like a widget query, a tab query is read-only.

On `event`, a `tab.action` carries `event.tabId`, `event.controlId`, and (for an item click) `event.itemId`. Return state changes and host actions here. Text prompts and path pickers require a user interaction. All existing action permissions apply. Lua and native plugins use the same JSON fields.

## Host actions

Return actions in `data.actions`. Each action has a `type`. The host applies widget creation or deletion and state replacement from the same response in one transaction.

| Action | Fields and requirements |
| --- | --- |
| `widget.create` | New stable `widgetId` and a declared `widgetType` |
| `widget.delete`, `widget.refresh` | The plugin's own `widgetId` |
| `ui.promptText` | `id`, `title`, and optional initial `value`. Requires a user interaction. |
| `ui.pickPaths` | `id`, `title`, optional `extensions` without leading dots, `multiple`, and `allowDirectories`. Requires a user interaction. |
| `ui.message` | `title` and display text in `value`. Requires a user interaction. |
| `apps.launch` | `id` and catalog `appId`. Requires `apps.launch`. |
| `paths.open` | `id` and absolute `path`. Requires `paths.open`. |
| `wallpaper.set` | `id` and local JPG or PNG `path`. Requires `wallpaper.set`. |

Dialogs and system actions with an `id` return an `action.result` event with a matching `actionId`. The original interaction is available under `previous` when the action came from an event. Read text from `value`, chosen items from `paths`, cancellation from `canceled`, and failure information from `error`.

## Native Windows x64 ABI

A DLL manifest sets `runtime: "dll"`, `platform: "windows/amd64"`, and `abiVersion: 1` (or `2`). Export the C interface in the [public header](quailcrate_plugin.h). Requests and responses are UTF-8 JSON and are limited to 4 MiB each. Return zero from `quailcrate_invoke` when a protocol response is available. Allocate and report the exact response length. The host copies the response and calls `quailcrate_free` to release it. Plugins may also export the optional `quailcrate_bind_host(quailcrate_host_notify_fn)` symbol to receive a thread-safe push callback for emitting asynchronous `widget.refresh`, `page.notify`, `event.publish`, or `state.set` notifications from background threads.

Install a third-party DLL package in the same plugin directory as Lua packages, using your own ID namespace. Each enabled DLL runs in its own isolated `QuailCrateNative.exe` worker process (providing crash and file-lock isolation with kill-on-close Job Object teardown) with the current user's full Windows permissions. Workers launch with the package directory as working directory and resolve companion `.dll` dependencies alongside the entry DLL, exposing `QUAILCRATE_PLUGIN_ID`, `QUAILCRATE_PLUGIN_DIR`, and `QUAILCRATE_PLUGIN_DATA_DIR` in the environment. The host verifies SHA-256 for bundled DLLs and its native runner; third-party DLLs require explicit user risk confirmation on first enable and whenever their binary digest changes. Run `scripts/build.ps1 -TestNative` to build and test the native worker, companion DLL loading, async notifications, and process isolation.
