local ui = require("quailcrate.ui.v2")
local failed = {}
local pending_context = {}

local translations = {
    ["en-US"] = {
        group = "Desktop Groups",
        add = "Add",
        add_files = "Add files…",
        add_folders = "Add folders / projects…",
        add_apps = "Add installed apps…",
        add_url = "Add web URL…",
        new_tab = "New tab…",
        new_group = "New desktop group…",
        rename = "Rename group",
        rename_title = "Rename group",
        rename_tab = "Rename tab",
        rename_tab_title = "Rename tab",
        delete_tab = "Delete tab",
        delete_tab_title = "Delete tab",
        delete_tab_body = "Remove tab \"%s\" and its shortcuts? Local files are not deleted.",
        empty = "Drop files, folders, or URLs here, or click +",
        open_item = "Open",
        open_with = "Set opener tool…",
        open_terminal = "Open in Terminal",
        reveal = "Show in Explorer",
        run_admin = "Run as administrator",
        refresh_icon = "Refresh website icon",
        rename_item = "Rename alias…",
        rename_item_title = "Rename item alias",
        move_item = "Move to another group / tab…",
        move_item_title = "Move item to",
        remove = "Remove",
        view_list = "Switch to compact list",
        view_grid = "Switch to icon grid",
        tab_opener = "Default project opener…",
        tab_opener_title = "Default opener for this tab",
        item_opener_title = "Choose opener for this item",
        sort_name = "Sort by name",
        delete = "Delete group",
        delete_title = "Delete desktop group",
        delete_body = "Remove group \"%s\" from the desktop? Your local files will not be deleted.",
        add_files_title = "Add files or shortcuts",
        add_folders_title = "Add folders or projects",
        add_apps_title = "Select installed applications or games",
        add_url_title = "Add web URL",
        new_title = "New desktop group",
        new_name = "Quick Launch",
        new_tab_title = "New tab name",
        new_tab_default = "Projects",
        custom_tool = "Custom executable (.exe)…",
        custom_tool_title = "Select opener executable",
        system_default = "System default",
        explorer_name = "Explorer",
        terminal_name = "Terminal",
        launch_item = "Launch desktop group item",
        source_start = "Start menu",
        source_packaged = "Packaged app",
        kind_web = "Web",
        kind_app = "App",
        kind_game = "Game",
        count_fmt = "%d ITEMS",
        open_error = "File not found or could not be opened."
    },
    ["zh-CN"] = {
        group = "桌面分组",
        add = "添加",
        add_files = "添加文件…",
        add_folders = "添加文件夹 / 项目…",
        add_apps = "添加已安装应用…",
        add_url = "添加网址…",
        new_tab = "新建标签页…",
        new_group = "新建桌面分组…",
        rename = "重命名分组",
        rename_title = "重命名分组",
        rename_tab = "重命名标签页",
        rename_tab_title = "重命名标签页",
        delete_tab = "删除此标签页",
        delete_tab_title = "删除标签页",
        delete_tab_body = "确定移除标签页「%s」及其中的快捷引用吗？不会删除本地文件。",
        empty = "拖入文件、文件夹、网址，或点 + 添加",
        open_item = "打开",
        open_with = "设置打开方式…",
        open_terminal = "在终端中打开",
        reveal = "打开所在位置",
        run_admin = "以管理员身份运行",
        refresh_icon = "刷新网站图标",
        rename_item = "重命名别名…",
        rename_item_title = "修改显示名称",
        move_item = "移动到其他分组 / 标签…",
        move_item_title = "移动条目到",
        remove = "移除",
        view_list = "切换为紧凑列表",
        view_grid = "切换为图标网格",
        tab_opener = "设置本页默认打开方式…",
        tab_opener_title = "选择本页默认打开工具",
        item_opener_title = "选择此条目的打开工具",
        sort_name = "按名称排序",
        delete = "删除分组",
        delete_title = "删除桌面分组",
        delete_body = "确定从桌面移除分组「%s」吗？本地原始文件不会被删除。",
        add_files_title = "选择要添加的文件或快捷方式",
        add_folders_title = "选择要添加的文件夹或项目",
        add_apps_title = "选择已安装的应用或游戏",
        add_url_title = "输入网址",
        new_title = "新建桌面分组",
        new_name = "快捷启动",
        new_tab_title = "新建标签页名称",
        new_tab_default = "项目",
        custom_tool = "自定义程序 (.exe)…",
        custom_tool_title = "选择打开工具程序",
        system_default = "系统默认",
        explorer_name = "资源管理器",
        terminal_name = "终端",
        launch_item = "启动桌面分组条目",
        source_start = "开始菜单",
        source_packaged = "商店 / UWP 应用",
        kind_web = "网页",
        kind_app = "应用",
        kind_game = "游戏",
        count_fmt = "%d 项",
        open_error = "File not found or could not be opened."
    }
}

local function success(request, data)
    return { requestId = request.requestId, ok = true, data = data }
end

local function trim(text)
    return string.match(text or "", "^%s*(.-)%s*$")
end

local function path_name(path)
    local clean = string.gsub(path or "", "[/\\]+$", "")
    local base = string.match(clean, "([^/\\]+)$") or clean
    local stripped = string.gsub(base, "%.[Ll][Nn][Kk]$", "")
    stripped = string.gsub(stripped, "%.[Uu][Rr][Ll]$", "")
    if stripped ~= "" then
        return stripped
    end
    return base
end

local function url_scheme(raw)
    local s = string.match(raw or "", "^([%a][%w%+%-%.]*):")
    return s and string.lower(s) or ""
end

local function url_host(raw)
    local host = string.match(raw or "", "^[%a][%w%+%-%.]*://([^/%?#]+)")
    if not host then
        return raw or ""
    end
    host = string.gsub(host, "^www%.", "")
    return host
end

local function is_web_url(raw)
    local s = url_scheme(raw)
    return s == "http" or s == "https"
end

local function normalize_user_url(raw)
    local v = trim(raw)
    if v == "" then
        return ""
    end
    if url_scheme(v) ~= "" then
        return v
    end
    local lower = string.lower(v)
    if string.match(lower, "^localhost[:/]") or lower == "localhost"
        or string.match(lower, "^%d+%.%d+%.%d+%.%d+")
        or string.match(lower, "^[%w%-]+:%d+") then
        return "http://" .. v
    end
    return "https://" .. v
end

local function default_url_title(raw)
    if not is_web_url(raw) then
        return raw
    end
    local host = url_host(raw)
    local without_port = string.match(host, "^([^:]+)") or host
    if without_port == "localhost" then
        return host
    end
    local first = string.match(without_port, "^([^%.]+)")
    if first and #first > 0 and not string.match(without_port, "^%d+%.%d+%.%d+%.%d+$") then
        return string.upper(string.sub(first, 1, 1)) .. string.sub(first, 2)
    end
    return host
end

local function ensure_v2_state(value)
    if type(value) ~= "table" or type(value.groups) ~= "table" then
        return { schemaVersion = 2, nextGroup = 1, nextTab = 1, nextItem = 1, groups = {} }
    end
    value.schemaVersion = 2
    value.nextGroup = value.nextGroup or 1
    value.nextTab = value.nextTab or 1
    value.nextItem = value.nextItem or 1
    for _, group in pairs(value.groups) do
        if type(group) == "table" then
            if type(group.tabs) ~= "table" or #group.tabs == 0 then
                local migrated = {}
                for _, item in ipairs(group.items or {}) do
                    if type(item) == "table" and item.path then
                        migrated[#migrated + 1] = {
                            id = item.id,
                            kind = "path",
                            path = item.path
                        }
                    end
                end
                local tab_id = "tab-" .. value.nextTab
                value.nextTab = value.nextTab + 1
                group.tabs = {
                    {
                        id = tab_id,
                        name = group.name or "Group",
                        viewMode = "grid",
                        items = #migrated > 0 and migrated or json.array()
                    }
                }
                group.activeTabId = tab_id
                group.items = nil
            else
                for _, tab in ipairs(group.tabs) do
                    if type(tab.items) ~= "table" then
                        tab.items = json.array()
                    end
                    if tab.viewMode ~= "list" and tab.viewMode ~= "grid" then
                        tab.viewMode = "grid"
                    end
                    for _, item in ipairs(tab.items) do
                        if not item.kind then
                            if item.url then
                                item.kind = "url"
                            elseif item.appId then
                                item.kind = "app"
                            else
                                item.kind = "path"
                            end
                        end
                    end
                end
                if not group.activeTabId then
                    group.activeTabId = group.tabs[1].id
                end
            end
        end
    end
    return value
end

local function find_tab(group, tab_id)
    if not group or type(group.tabs) ~= "table" or #group.tabs == 0 then
        return nil, nil
    end
    if tab_id and tab_id ~= "" then
        for i, tab in ipairs(group.tabs) do
            if tab.id == tab_id then
                return tab, i
            end
        end
    end
    for i, tab in ipairs(group.tabs) do
        if tab.id == group.activeTabId then
            return tab, i
        end
    end
    return group.tabs[1], 1
end

local function item_index(items, id)
    for index, item in ipairs(items or {}) do
        if item.id == id then
            return index
        end
    end
end

local function tool_label(tool, strings)
    if not tool then
        return nil
    end
    if tool.id == "explorer" then
        return strings.explorer_name
    end
    if tool.id == "terminal" then
        return strings.terminal_name
    end
    return tool.name
end

local function opener_label(opener, tools, strings)
    if type(opener) ~= "table" then
        return nil
    end
    if opener.toolId and opener.toolId ~= "" and opener.toolId ~= "system" then
        if opener.toolId == "explorer" then
            return strings.explorer_name
        end
        if opener.toolId == "terminal" then
            return strings.terminal_name
        end
        for _, t in ipairs(tools or {}) do
            if t.id == opener.toolId then
                return t.name
            end
        end
        return opener.label or opener.toolId
    end
    if opener.toolPath and opener.toolPath ~= "" then
        return opener.label or path_name(opener.toolPath)
    end
    return nil
end

local function item_display_name(item)
    if item.name and trim(item.name) ~= "" then
        return item.name
    end
    if item.kind == "url" then
        return default_url_title(item.url)
    end
    if item.kind == "app" then
        return item.appName or item.appId or "App"
    end
    return path_name(item.path)
end

local function build_app_lookup(apps)
    local by_id = {}
    for _, app in ipairs(apps or {}) do
        by_id[app.id] = app
    end
    return by_id
end

local function item_icon_key(item, app_map)
    if item.cachedIcon and item.cachedIcon ~= "" then
        return item.cachedIcon
    end
    if item.kind == "url" then
        return "favicon:" .. (item.url or "")
    end
    if item.kind == "app" then
        local app = app_map and app_map[item.appId]
        if app and app.icon and app.icon ~= "" then
            return app.icon
        end
        if item.appIcon and item.appIcon ~= "" then
            return item.appIcon
        end
        return item.appId or ""
    end
    return item.path or ""
end

local function item_badge(item, tab, tools, strings)
    if item.kind == "url" then
        local s = url_scheme(item.url)
        if s == "http" or s == "https" then
            return "WEB"
        end
        if s == "steam" then
            return "STEAM"
        end
        return string.upper(strings.kind_game)
    end
    if item.kind == "app" then
        return "APP"
    end
    local opener = item.opener or (tab and tab.defaultOpener)
    return opener_label(opener, tools, strings)
end

local function item_hint(item, widget_id)
    local failure = failed[widget_id] and failed[widget_id][item.id]
    if failure then
        return failure
    end
    if item.subtitle and item.subtitle ~= "" then
        return item.subtitle
    end
    if item.kind == "url" then
        return is_web_url(item.url) and url_host(item.url) or item.url
    end
    if item.kind == "app" then
        return item.appName or item.appId or ""
    end
    return item.path or ""
end

local function refresh(widget_id, state)
    return { state = state, actions = { { type = "widget.refresh", widgetId = widget_id } } }
end

local function refresh_many(widget_ids, state)
    local actions = {}
    for _, id in ipairs(widget_ids) do
        actions[#actions + 1] = { type = "widget.refresh", widgetId = id }
    end
    return { state = state, actions = actions }
end

local function is_duplicate(tab, kind, key)
    local lower = string.lower(key or "")
    for _, item in ipairs(tab.items or {}) do
        if item.kind == kind then
            local existing = kind == "path" and item.path or (kind == "url" and item.url or item.appId)
            if string.lower(existing or "") == lower then
                return true
            end
        end
    end
    return false
end

local function add_entries(state, widget_id, tab_id, paths, urls, imported, position)
    local group = state.groups[widget_id]
    if not group then
        return {}
    end
    local tab = find_tab(group, tab_id)
    if not tab then
        return {}
    end
    if type(tab.items) ~= "table" then
        tab.items = {}
    end
    local index = math.max(1, math.min((position and position >= 1) and position or (#tab.items + 1), #tab.items + 1))

    for _, imp in ipairs(imported or {}) do
        if type(imp) == "table" and type(imp.url) == "string" and imp.url ~= "" and not is_duplicate(tab, "url", imp.url) then
            local item_id = "item-" .. state.nextItem
            state.nextItem = state.nextItem + 1
            local entry = {
                id = item_id,
                kind = "url",
                name = trim(imp.name),
                url = imp.url,
                cachedIcon = imp.cachedIcon
            }
            if entry.name == "" then
                entry.name = default_url_title(imp.url)
            end
            table.insert(tab.items, index, entry)
            index = index + 1
        end
    end

    for _, raw_url in ipairs(urls or {}) do
        local u = normalize_user_url(raw_url)
        if u ~= "" and not is_duplicate(tab, "url", u) then
            local item_id = "item-" .. state.nextItem
            state.nextItem = state.nextItem + 1
            table.insert(tab.items, index, {
                id = item_id,
                kind = "url",
                name = default_url_title(u),
                url = u
            })
            index = index + 1
        end
    end

    for _, path in ipairs(paths or {}) do
        if type(path) == "string" and path ~= "" and not is_duplicate(tab, "path", path) then
            local item_id = "item-" .. state.nextItem
            state.nextItem = state.nextItem + 1
            table.insert(tab.items, index, {
                id = item_id,
                kind = "path",
                path = path
            })
            index = index + 1
        end
    end
    return refresh(widget_id, state)
end

local function reorder(state, widget_id, item_id, destination)
    local group = state.groups[widget_id]
    local tab = find_tab(group)
    if not tab then
        return {}
    end
    local items = tab.items
    local from = item_index(items, item_id)
    if not from or not destination then
        return {}
    end
    local target = math.max(1, math.min(destination, #items))
    if from == target then
        return {}
    end
    local item = table.remove(items, from)
    table.insert(items, target, item)
    return refresh(widget_id, state)
end

local function transfer_item(state, src_widget_id, item_id, dst_widget_id, dst_tab_id)
    local src_group = state.groups[src_widget_id]
    local dst_group = state.groups[dst_widget_id]
    if not src_group or not dst_group then
        return {}
    end
    local src_tab = find_tab(src_group)
    local dst_tab = find_tab(dst_group, dst_tab_id)
    if not src_tab or not dst_tab or src_tab == dst_tab then
        return {}
    end
    local idx = item_index(src_tab.items, item_id)
    if not idx then
        return {}
    end
    local item = table.remove(src_tab.items, idx)
    if type(dst_tab.items) ~= "table" then
        dst_tab.items = {}
    end
    table.insert(dst_tab.items, item)
    if failed[src_widget_id] then
        failed[src_widget_id][item_id] = nil
    end
    if src_widget_id == dst_widget_id then
        return refresh(src_widget_id, state)
    end
    return refresh_many({ src_widget_id, dst_widget_id }, state)
end

local function launch_action_for(item, tab, force_admin)
    if item.kind == "reference" and item.reference then
        return { type = "service.call", id = "open", service = "quailcrate.items",
            method = "open", provider = item.reference.source, args = { item = item.reference } }
    end
    if item.kind == "url" then
        return { type = "urls.open", id = "open", url = item.url }
    end
    if item.kind == "app" then
        return { type = "apps.launch", id = "open", appId = item.appId }
    end
    local opener = item.opener or (tab and tab.defaultOpener)
    local run_admin = force_admin or item.runAsAdmin or false
    if type(opener) == "table" and ((opener.toolId and opener.toolId ~= "" and opener.toolId ~= "system") or (opener.toolPath and opener.toolPath ~= "")) then
        return {
            type = "paths.openWith",
            id = "open",
            path = item.path,
            toolId = opener.toolId,
            toolPath = opener.toolPath,
            runAsAdmin = run_admin
        }
    end
    return { type = "paths.open", id = "open", path = item.path, runAsAdmin = run_admin }
end

local function view(widget_id, group, payload, strings)
    if not group then
        return { title = strings.group, uiVersion = 2, root = { type = "column", id = "body" } }
    end
    local tab = find_tab(group)
    if not tab then
        return { title = group.name or strings.group, uiVersion = 2, root = { type = "column", id = "body" } }
    end
    local app_map = build_app_lookup(payload.apps)
    local items = {}
    local available = {}
    for _,service in ipairs(payload.services or {}) do
        if service.id=="quailcrate.items" then available[service.provider]=true end
    end
    for _, item in ipairs(tab.items or {}) do
        local failure = failed[widget_id] and failed[widget_id][item.id]
        local unavailable = item.reference and not available[item.reference.source]
        items[#items + 1] = {
            key = item.id,
            text = item_display_name(item),
            hint = unavailable and (payload.locale=="zh-CN" and "来源不可用" or "Source unavailable") or item_hint(item, widget_id),
            badge = item_badge(item, tab, payload.tools, strings),
            icon = item_icon_key(item, app_map),
            variant = (failure or unavailable) and "warning" or nil
        }
    end
    local cols = tab.viewMode == "list" and 1 or 4
    local root = { type = "column", id = "group", children = {
        ui.toolbar("toolbar", {
            ui.menu_button("add", strings.add, "plus", "add", true),
            ui.menu_button("menu", strings.menu or group.name, "more", "context", true)
        }, true),
        { id = "items", type = "virtualList", events = { activate = "items" },
            props = { columns = cols, text = #items == 0 and strings.empty or nil, grow = 1,
                items = #items > 0 and items or nil, drop = true, reorder = true } }
    } }

    local view_toggle_text = tab.viewMode == "list" and strings.view_grid or strings.view_list
    local view_toggle_icon = tab.viewMode == "list" and "grid" or "list"

    local menu = {
        -- Quick-add menu (triggered by header '+')
        { id = "quick_add_files", type = "button", text = strings.add_files, icon = "plus", placement = "add" },
        { id = "quick_add_folders", type = "button", text = strings.add_folders, icon = "folder-plus", placement = "add" },
        { id = "quick_add_apps", type = "button", text = strings.add_apps, icon = "apps", placement = "add" },
        { id = "quick_add_url", type = "button", text = strings.add_url, icon = "globe", placement = "add" },
        { id = "sep_add", type = "separator", placement = "add" },
        { id = "quick_new_tab", type = "button", text = strings.new_tab, icon = "tab-plus", placement = "add" },
        { id = "quick_new_group", type = "button", text = strings.new_group, icon = "layout", placement = "add" },

        -- Tab header right-click menu
        { id = "rename_tab", type = "button", text = strings.rename_tab, icon = "edit", placement = "tab" },
        { id = "new_tab_ctx", type = "button", text = strings.new_tab, icon = "tab-plus", placement = "tab" },

        -- Item context menu
        { id = "open_item", type = "button", text = strings.open_item, icon = "rocket", itemOnly = true },
        { id = "open_with", type = "button", text = strings.open_with, icon = "code", itemOnly = true },
        { id = "open_terminal", type = "button", text = strings.open_terminal, icon = "terminal", itemOnly = true },
        { id = "reveal", type = "button", text = strings.reveal, icon = "open-folder", itemOnly = true },
        { id = "run_admin", type = "button", text = strings.run_admin, icon = "shield", itemOnly = true },
        { id = "refresh_icon", type = "button", text = strings.refresh_icon, icon = "refresh", itemOnly = true },
        { id = "sep_item", type = "separator", itemOnly = true },
        { id = "rename_item", type = "button", text = strings.rename_item, icon = "edit", itemOnly = true },
        { id = "move_item", type = "button", text = strings.move_item, icon = "move", itemOnly = true },
        { id = "remove", type = "button", text = strings.remove, icon = "trash", itemOnly = true, danger = true },

        -- Panel menu
        { id = "add_files", type = "button", text = strings.add_files, icon = "plus" },
        { id = "add_folders", type = "button", text = strings.add_folders, icon = "folder-plus" },
        { id = "add_apps", type = "button", text = strings.add_apps, icon = "apps" },
        { id = "add_url", type = "button", text = strings.add_url, icon = "globe" },
        { id = "sep_view", type = "separator" },
        { id = "toggle_view", type = "button", text = view_toggle_text, icon = view_toggle_icon },
        { id = "tab_opener", type = "button", text = strings.tab_opener, icon = "code" },
        { id = "sort_name", type = "button", text = strings.sort_name, icon = "sort" },
        { id = "sep_struct", type = "separator" },
        { id = "new_tab", type = "button", text = strings.new_tab, icon = "tab-plus" },
        { id = "new_group", type = "button", text = strings.new_group, icon = "layout" },
        { id = "rename", type = "button", text = strings.rename, icon = "edit" },
        { id = "delete", type = "button", text = strings.delete, icon = "trash", danger = true }
    }
    if #group.tabs > 1 then
        table.insert(menu, 10, { id = "delete_tab", type = "button", text = strings.delete_tab, icon = "trash", placement = "tab", danger = true })
    end

    local out = {
        title = group.name,
        summary = string.format(strings.count_fmt, #items),
        uiVersion = 2,
        root = root,
        menu = menu
    }
    if #group.tabs > 1 then
        local tabs = {}
        for _, t in ipairs(group.tabs) do
            tabs[#tabs + 1] = { id = t.id, title = t.name }
        end
        out.tabs = tabs
        out.activeTab = tab.id
    end
    return out
end

local function opener_options(tools, current_opener, strings)
    local cur_id = type(current_opener) == "table" and current_opener.toolId or ""
    local cur_path = type(current_opener) == "table" and current_opener.toolPath or ""
    local opts = {
        { id = "system", text = strings.system_default, selected = (cur_id == "" or cur_id == "system") and cur_path == "" }
    }
    for _, t in ipairs(tools or {}) do
        opts[#opts + 1] = {
            id = "tool:" .. t.id,
            text = tool_label(t, strings),
            subtitle = t.kind,
            selected = cur_id == t.id
        }
    end
    opts[#opts + 1] = {
        id = "custom",
        text = strings.custom_tool,
        subtitle = cur_path ~= "" and cur_path or nil,
        selected = cur_path ~= ""
    }
    return opts
end

local function handle_action(state, payload, event, strings)
    local widget_id = event.widgetId
    local group = state.groups[widget_id]
    local tab = find_tab(group)
    if not group or not tab then
        return {}
    end

    local cid = event.controlId
    if cid == "quick_add_files" then cid = "add_files"
    elseif cid == "quick_add_folders" then cid = "add_folders"
    elseif cid == "quick_add_apps" then cid = "add_apps"
    elseif cid == "quick_add_url" then cid = "add_url"
    elseif cid == "quick_new_tab" then cid = "new_tab"
    elseif cid == "quick_new_group" then cid = "new_group"
    end

    if cid == "items" then
        local idx = item_index(tab.items, event.itemId)
        if not idx then
            return {}
        end
        return { actions = { launch_action_for(tab.items[idx], tab, false) } }
    end

    if cid == "add_files" or cid == "add" then
        return { actions = { { type = "ui.pickPaths", id = "add_paths", title = strings.add_files_title,
            multiple = true, allowDirectories = false } } }
    end
    if cid == "add_folders" then
        return { actions = { { type = "ui.pickPaths", id = "add_paths", title = strings.add_folders_title,
            multiple = true, allowDirectories = true, directoriesOnly = true } } }
    end
    if cid == "add_apps" then
        local opts = {}
        for _, app in ipairs(payload.apps or {}) do
            opts[#opts + 1] = {
                id = app.id,
                text = app.name,
                subtitle = app.source == "packaged" and strings.source_packaged or strings.source_start,
                icon = app.icon or ""
            }
        end
        return { actions = { { type = "ui.pickOptions", id = "add_apps", title = strings.add_apps_title,
            multiple = true, options = opts } } }
    end
    if cid == "add_url" then
        return { actions = { { type = "ui.promptText", id = "add_url", title = strings.add_url_title, value = "https://" } } }
    end
    if cid == "new_group" then
        return { actions = { { type = "ui.promptText", id = "new", title = strings.new_title, value = strings.new_name } } }
    end
    if cid == "new_tab" or cid == "new_tab_ctx" then
        return { actions = { { type = "ui.promptText", id = "new_tab", title = strings.new_tab_title, value = strings.new_tab_default } } }
    end
    if cid == "rename" then
        return { actions = { { type = "ui.promptText", id = "rename", title = strings.rename_title, value = group.name } } }
    end
    if cid == "rename_tab" then
        local target_tab = find_tab(group, event.tabId)
        if not target_tab then
            return {}
        end
        pending_context[widget_id] = { tabId = target_tab.id }
        return { actions = { { type = "ui.promptText", id = "rename_tab", title = strings.rename_tab_title, value = target_tab.name } } }
    end
    if cid == "delete_tab" then
        if #group.tabs <= 1 then
            return {}
        end
        local target_tab = find_tab(group, event.tabId)
        if not target_tab then
            return {}
        end
        pending_context[widget_id] = { tabId = target_tab.id }
        return { actions = { {
            type = "ui.confirm",
            id = "delete_tab",
            title = strings.delete_tab_title,
            body = string.format(strings.delete_tab_body, target_tab.name)
        } } }
    end
    if cid == "toggle_view" then
        tab.viewMode = tab.viewMode == "list" and "grid" or "list"
        return refresh(widget_id, state)
    end
    if cid == "tab_opener" then
        pending_context[widget_id] = { tabId = tab.id }
        return { actions = { {
            type = "ui.pickOptions",
            id = "tab_opener",
            title = strings.tab_opener_title,
            multiple = false,
            options = opener_options(payload.tools, tab.defaultOpener, strings)
        } } }
    end
    if cid == "sort_name" then
        table.sort(tab.items, function(a, b)
            return string.lower(item_display_name(a)) < string.lower(item_display_name(b))
        end)
        return refresh(widget_id, state)
    end
    if cid == "delete" then
        return { actions = { {
            type = "ui.confirm",
            id = "delete",
            title = strings.delete_title,
            body = string.format(strings.delete_body, group.name)
        } } }
    end

    -- Item-level actions
    local idx = item_index(tab.items, event.itemId)
    if not idx then
        return {}
    end
    local item = tab.items[idx]
    if event.controlId == "open_item" then
        return { actions = { launch_action_for(item, tab, false) } }
    end
    if event.controlId == "run_admin" then
        if item.kind == "path" then
            return { actions = { launch_action_for(item, tab, true) } }
        end
        return { actions = { launch_action_for(item, tab, false) } }
    end
    if event.controlId == "open_terminal" then
        if item.kind == "path" then
            return { actions = { { type = "paths.openWith", id = "open", path = item.path, toolId = "terminal" } } }
        end
        return {}
    end
    if event.controlId == "reveal" then
        if item.kind == "path" then
            return { actions = { { type = "paths.reveal", id = "reveal", path = item.path } } }
        end
        return {}
    end
    if event.controlId == "refresh_icon" then
        if item.kind == "url" and is_web_url(item.url) then
            return { actions = { { type = "icons.refresh", url = item.url, iconKey = "favicon:" .. item.url } } }
        end
        return {}
    end
    if event.controlId == "open_with" then
        if item.kind ~= "path" then
            return {}
        end
        pending_context[widget_id] = { tabId = tab.id, itemId = item.id }
        return { actions = { {
            type = "ui.pickOptions",
            id = "item_opener",
            title = strings.item_opener_title,
            multiple = false,
            options = opener_options(payload.tools, item.opener, strings)
        } } }
    end
    if event.controlId == "rename_item" then
        pending_context[widget_id] = { tabId = tab.id, itemId = item.id }
        return { actions = { { type = "ui.promptText", id = "rename_item", title = strings.rename_item_title, value = item_display_name(item) } } }
    end
    if event.controlId == "move_item" then
        local opts = {}
        for gid, g in pairs(state.groups) do
            for _, t in ipairs(g.tabs or {}) do
                if not (gid == widget_id and t.id == tab.id) then
                    local label = #g.tabs > 1 and (g.name .. " / " .. t.name) or g.name
                    opts[#opts + 1] = { id = gid .. "|" .. t.id, text = label }
                end
            end
        end
        table.sort(opts, function(a, b) return a.text < b.text end)
        if #opts == 0 then
            return {}
        end
        pending_context[widget_id] = { tabId = tab.id, itemId = item.id }
        return { actions = { {
            type = "ui.pickOptions",
            id = "move_item",
            title = strings.move_item_title,
            multiple = false,
            options = opts
        } } }
    end
    if event.controlId == "remove" then
        table.remove(tab.items, idx)
        if failed[widget_id] then
            failed[widget_id][item.id] = nil
        end
        return refresh(widget_id, state)
    end
    return {}
end

local function handle_result(state, payload, event, strings)
    local widget_id = event.widgetId
    local group = state.groups[widget_id]
    local item_id = event.itemId or (event.previous and event.previous.itemId)
    if event.actionId == "open" and group and item_id then
        failed[widget_id] = failed[widget_id] or {}
        failed[widget_id][item_id] = event.error and strings.open_error or nil
        return refresh(widget_id)
    end
    if event.canceled or event.error then
        return {}
    end

    local val = trim(event.value)
    if event.actionId == "new" and val ~= "" then
        local id = "group-" .. state.nextGroup
        state.nextGroup = state.nextGroup + 1
        local tab_id = "tab-" .. state.nextTab
        state.nextTab = state.nextTab + 1
        state.groups[id] = {
            name = val,
            activeTabId = tab_id,
            tabs = {
                { id = tab_id, name = val, viewMode = "grid", items = json.array() }
            }
        }
        return { state = state, actions = { { type = "widget.create", widgetId = id, widgetType = "group" } } }
    end
    if not group then
        return {}
    end
    local tab = find_tab(group)
    local ctx = pending_context[widget_id] or {}

    if event.actionId == "rename" and val ~= "" then
        group.name = val
        if #group.tabs == 1 then
            group.tabs[1].name = val
        end
        return refresh(widget_id, state)
    end
    if event.actionId == "new_tab" and val ~= "" then
        local tab_id = "tab-" .. state.nextTab
        state.nextTab = state.nextTab + 1
        group.tabs[#group.tabs + 1] = { id = tab_id, name = val, viewMode = "grid", items = json.array() }
        group.activeTabId = tab_id
        return refresh(widget_id, state)
    end
    if event.actionId == "rename_tab" and val ~= "" then
        local target_tab = find_tab(group, ctx.tabId)
        if target_tab then
            target_tab.name = val
            if #group.tabs == 1 then
                group.name = val
            end
            return refresh(widget_id, state)
        end
        return {}
    end
    if event.actionId == "delete_tab" then
        if #group.tabs <= 1 then
            return {}
        end
        local _, idx = find_tab(group, ctx.tabId)
        if idx then
            table.remove(group.tabs, idx)
            local next_idx = math.max(1, math.min(idx, #group.tabs))
            group.activeTabId = group.tabs[next_idx].id
            return refresh(widget_id, state)
        end
        return {}
    end
    if event.actionId == "add_paths" or event.actionId == "add" then
        return add_entries(state, widget_id, tab and tab.id, event.paths, event.urls, event.imported)
    end
    if event.actionId == "add_url" and val ~= "" then
        return add_entries(state, widget_id, tab and tab.id, nil, { val }, nil)
    end
    if event.actionId == "add_apps" and tab then
        local app_map = build_app_lookup(payload.apps)
        if type(tab.items) ~= "table" then
            tab.items = {}
        end
        for _, app_id in ipairs(event.items or {}) do
            local app = app_map[app_id]
            if app and not is_duplicate(tab, "app", app.id) then
                local new_id = "item-" .. state.nextItem
                state.nextItem = state.nextItem + 1
                tab.items[#tab.items + 1] = {
                    id = new_id,
                    kind = "app",
                    name = app.name,
                    appId = app.id,
                    appName = app.name,
                    appIcon = app.icon
                }
            end
        end
        return refresh(widget_id, state)
    end
    if event.actionId == "rename_item" and val ~= "" then
        local target_tab = find_tab(group, ctx.tabId)
        local idx = target_tab and item_index(target_tab.items, ctx.itemId)
        if idx then
            target_tab.items[idx].name = val
            return refresh(widget_id, state)
        end
        return {}
    end
    if event.actionId == "move_item" then
        local chosen = event.items and event.items[1]
        if chosen and ctx.itemId then
            local dst_w, dst_t = string.match(chosen, "^([^|]+)|(.+)$")
            if dst_w and dst_t then
                return transfer_item(state, widget_id, ctx.itemId, dst_w, dst_t)
            end
        end
        return {}
    end
    if event.actionId == "tab_opener" or event.actionId == "item_opener" then
        local chosen = event.items and event.items[1]
        if not chosen then
            return {}
        end
        if chosen == "custom" then
            pending_context[widget_id] = {
                mode = event.actionId,
                tabId = ctx.tabId or (tab and tab.id),
                itemId = ctx.itemId
            }
            return { actions = { {
                type = "ui.pickPaths",
                id = "custom_opener_exe",
                title = strings.custom_tool_title,
                extensions = { "exe", "cmd", "bat" },
                multiple = false,
                allowDirectories = false
            } } }
        end
        local opener = nil
        if string.sub(chosen, 1, 5) == "tool:" then
            local tid = string.sub(chosen, 6)
            opener = { toolId = tid, label = opener_label({ toolId = tid }, payload.tools, strings) }
        end
        if event.actionId == "tab_opener" then
            local target_tab = find_tab(group, ctx.tabId)
            if target_tab then
                target_tab.defaultOpener = opener
                return refresh(widget_id, state)
            end
        else
            local target_tab = find_tab(group, ctx.tabId)
            local idx = target_tab and item_index(target_tab.items, ctx.itemId)
            if idx then
                target_tab.items[idx].opener = opener
                return refresh(widget_id, state)
            end
        end
        return {}
    end
    if event.actionId == "custom_opener_exe" then
        local exe = event.paths and event.paths[1]
        if not exe or exe == "" then
            return {}
        end
        local opener = { toolPath = exe, label = path_name(exe) }
        local target_tab = find_tab(group, ctx.tabId)
        if ctx.mode == "tab_opener" and target_tab then
            target_tab.defaultOpener = opener
            return refresh(widget_id, state)
        elseif target_tab and ctx.itemId then
            local idx = item_index(target_tab.items, ctx.itemId)
            if idx then
                target_tab.items[idx].opener = opener
                return refresh(widget_id, state)
            end
        end
        return {}
    end
    if event.actionId == "delete" then
        state.groups[widget_id] = nil
        failed[widget_id] = nil
        pending_context[widget_id] = nil
        return { state = state, actions = { { type = "widget.delete", widgetId = widget_id } } }
    end
    return {}
end

local function match_score(haystack, query, exact_s, prefix_s, contains_s)
    if not haystack or haystack == "" then
        return nil
    end
    local lower = string.lower(haystack)
    if lower == query then
        return exact_s
    end
    local pos = string.find(lower, query, 1, true)
    if pos == 1 then
        return prefix_s
    end
    if pos then
        return contains_s
    end
    return nil
end

local function search_items(state, payload, strings)
    local query = string.lower(trim(payload.text))
    local limit = payload.limit or 8
    if query == "" or limit <= 0 then
        return {}
    end
    local app_map = build_app_lookup(payload.apps)
    local results = {}
    for widget_id, group in pairs(state.groups) do
        for _, tab in ipairs(group.tabs or {}) do
            for _, item in ipairs(tab.items or {}) do
                local title = item_display_name(item)
                local score = match_score(title, query, 1.05, 0.95, 0.78)
                local extra_text = (item.subtitle or "") .. " " .. (item.url or "") .. " " .. (item.path or "") .. " " .. (tab.name or "") .. " " .. (group.name or "")
                local sec = match_score(extra_text, query, 0.68, 0.68, 0.65)
                if sec and (not score or sec > score) then
                    score = sec
                end
                if score then
                    local scope = #group.tabs > 1 and (group.name .. " · " .. tab.name) or group.name
                    local badge = item_badge(item, tab, payload.tools, strings)
                    local sub = scope
                    if item.kind == "url" and is_web_url(item.url) then
                        sub = strings.kind_web .. " · " .. url_host(item.url)
                    elseif badge and badge ~= "" then
                        sub = scope .. " · " .. badge
                    end
                    results[#results + 1] = {
                        id = widget_id .. ":" .. tab.id .. ":" .. item.id,
                        title = title,
                        subtitle = sub,
                        icon = item_icon_key(item, app_map),
                        score = score,
                        command = {
                            id = "launch_item",
                            args = { widgetId = widget_id, tabId = tab.id, itemId = item.id }
                        }
                    }
                end
            end
        end
    end
    table.sort(results, function(a, b)
        if a.score ~= b.score then
            return a.score > b.score
        end
        if a.title ~= b.title then
            return a.title < b.title
        end
        return a.id < b.id
    end)
    for i = #results, limit + 1, -1 do
        results[i] = nil
    end
    return results
end

local function add_reference(state, payload, strings)
    local args = payload.args or {}
    local ref = args.item
    local function failure(code) return { reply = { status = "error", code = code } } end
    if type(ref) ~= "table" or type(ref.id) ~= "string" or ref.id == "" or #ref.id > 2048 then
        return failure("invalid_argument")
    end
    local app = ref.kind == "app" and ref.source == "quailcrate.apps" and build_app_lookup(payload.apps)[ref.id]
    if not app and (ref.kind ~= "note" or ref.source ~= payload.caller) then return failure("invalid_argument") end
    local choice = payload.event and payload.event.items and payload.event.items[1]
    if not choice then
        local options = {}
        for id, group in pairs(state.groups) do options[#options+1] = { id=id, text=group.name } end
        table.sort(options, function(a,b) return a.id < b.id end)
        options[#options+1] = { id="create", text=strings.new_title }
        return { actions = {{ type="ui.pickOptions", id="target", title=strings.group, options=options }} }
    end
    local effects = {}
    if choice == "create" then
        local created = handle_result(state,payload,{actionId="new",value=strings.new_name},strings)
        effects = created.actions
        choice = effects[1].widgetId
    end
    local group = state.groups[choice]
    if not group then return failure("unavailable") end
    local tab = find_tab(group)
    for _, item in ipairs(tab.items) do
        local existing = item.reference
        if (app and item.kind=="app" and item.appId==ref.id) or
            (existing and existing.source==ref.source and existing.kind==ref.kind and existing.id==ref.id) then
            return {reply={status="ok",value={groupId=choice,itemId=item.id,added=false}}}
        end
    end
    local item = { id="item-"..state.nextItem, kind="reference", reference=ref,
        name=type(args.title)=="string" and args.title or ref.id, subtitle=ref.source }
    if app then
        item.kind, item.appId, item.name, item.appName, item.appIcon = "app",app.id,app.name,app.name,app.icon
        item.reference, item.subtitle = nil, nil
    end
    state.nextItem = state.nextItem+1
    tab.items[#tab.items+1] = item
    effects[#effects+1] = {type="event.publish",topic="quailcrate.desktop-groups.changed",args={groupId=choice,itemId=item.id}}
    return {state=state,actions=effects,reply={status="ok",value={groupId=choice,itemId=item.id,added=true}}}
end

return function(request)
    local payload = request.payload or {}
    local strings = translations[payload.locale] or translations["en-US"]
    if request.op == "init" then
        return success(request, {
            search = true,
            tray = {{id="new-group", title=strings.new_title, icon="folder-plus", intent="command", command="new", order=20}},
            resultActions = {{id="add", title=payload.locale=="zh-CN" and "添加到桌面分组" or "Add to desktop group",
                service="quailcrate.groups.items",version="1.0.0",method="add",kinds={"app","note"}}},
            commands = {
                { id = "new", title = strings.new_title, location = "desktop-edit" },
                { id = "launch_item", title = strings.launch_item, location = "search" }
            },
            widgets = { { id = "group", title = strings.group, width = 396, height = 276, anchor = "left" } }
        })
    end
    local state = ensure_v2_state(payload.state)
    if request.op == "service" and payload.service == "quailcrate.groups.items" and payload.method=="add" then
        return success(request,add_reference(state,payload,strings))
    end
    if request.op == "search" then
        local results = search_items(state, payload, strings)
        return success(request, #results > 0 and { results = results } or {})
    end
    if request.op == "command" then
        if payload.commandId == "new" then
            return success(request, {
                actions = { { type = "ui.promptText", id = "new", title = strings.new_title, value = strings.new_name } }
            })
        end
        if payload.commandId == "launch_item" and type(payload.args) == "table" then
            local group = state.groups[payload.args.widgetId]
            local tab = find_tab(group, payload.args.tabId)
            local idx = tab and item_index(tab.items, payload.args.itemId)
            if idx then
                return success(request, { actions = { launch_action_for(tab.items[idx], tab, false) } })
            end
            return success(request, {})
        end
    end
    if request.op == "widget_state" then
        return success(request, { view = view(payload.widgetId, state.groups[payload.widgetId], payload, strings) })
    end
    if request.op == "event" then
        local event = payload.event or {}
        if event.type == "service.result" and event.actionId == "open" and event.status == "error" then
            return success(request,{actions={{type="ui.message",title=strings.launch_item,
                value=payload.locale=="zh-CN" and "条目来源不可用。" or "The item provider is unavailable."}}})
        end
        if event.type == "action.result" then
            return success(request, handle_result(state, payload, event, strings))
        end
        local widget_id = event.widgetId
        if not state.groups[widget_id] then
            return success(request, {})
        end
        if event.type == "widget.tabSelect" then
            local group = state.groups[widget_id]
            local target = find_tab(group, event.tabId)
            if target and group.activeTabId ~= target.id then
                group.activeTabId = target.id
                return success(request, refresh(widget_id, state))
            end
            return success(request, {})
        end
        if event.type == "widget.action" then
            return success(request, handle_action(state, payload, event, strings))
        end
        if event.type == "widget.drop" then
            local pos = (event.to and event.to >= 0) and (event.to + 1) or nil
            return success(request, add_entries(state, widget_id, event.targetTabId, event.paths, event.urls, event.imported, pos))
        end
        if event.type == "widget.reorder" and event.to then
            local tab = find_tab(state.groups[widget_id])
            local items = tab and tab.items or {}
            local old_item = event.from and items[event.from + 1]
            return success(request, reorder(state, widget_id, event.itemId or (old_item and old_item.id), event.to + 1))
        end
        if event.type == "widget.transfer" then
            return success(request, transfer_item(state, widget_id, event.itemId, event.targetWidgetId, event.targetTabId))
        end
    end
    return success(request, {})
end
