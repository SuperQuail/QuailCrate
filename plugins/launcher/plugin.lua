local translations = {
    ["en-US"] = { open = "Open application", title = "QuailCrate · Launcher",
        placeholder = "Type an app or command...", idle = "Type an app or command, or / to list commands", pending = "Searching…",
        indexing = "Preparing the application list…", empty = "No matching apps or commands",
        keys = "↑↓ Navigate   ·   ↵ Launch   ·   / Commands   ·   ESC Close", sources = { ["start-menu"] = "Start menu", packaged = "Packaged app" } },
    ["zh-CN"] = { open = "打开应用", title = "鹌鹑工具箱 · 快速启动",
        placeholder = "输入命令或应用名称...", idle = "输入应用或指令名称，输入 / 查看全部指令", pending = "正在搜索…",
        indexing = "正在准备应用列表", empty = "没有找到匹配的应用或指令",
        keys = "↑↓ 选择   ·   ↵ 启动   ·   / 仅看指令   ·   ESC 关闭", sources = { ["start-menu"] = "开始菜单", packaged = "打包应用" } }
}

local function launcher(strings)
    local row = { type = "row", id = "result", props = { height = 46, gap = 10 }, children = {
        { type = "icon", id = "appIcon", props = { field = "icon", width = 32, height = 32, text = ">" } },
        { type = "column", id = "details", props = { grow = 1 }, children = {
            { type = "text", id = "title", props = { field = "title" } },
            { type = "text", id = "source", props = { field = "subtitle", variant = "muted" } }
        } },
        { type = "text", id = "badge", props = { field = "badge", width = 68, height = 20 } },
        { type = "text", id = "enter", props = { field = "selection", width = 18, text = "↵", variant = "accent" } }
    } }
    return { uiVersion = 2, title = strings.title, indexing = strings.indexing, noResults = strings.empty,
        root = { type = "column", id = "launcher", props = { width = 620 }, children = {
        { type = "searchInput", id = "query", props = { height = 58, label = strings.placeholder, text = ">" },
            events = { change = "search.query", submit = "search.open" } },
        { type = "virtualList", id = "results", props = { source = "results", visibleRows = 5, label = strings.idle, text = strings.pending },
            events = { activate = "search.open", contextMenu = "search.actions" }, children = { row } },
        { type = "text", id = "shortcuts", props = { height = 28, text = strings.keys, variant = "caption" } }
    } } }
end

local function success(request, data)
    return { requestId = request.requestId, ok = true, data = data }
end

local function match_score(name, query)
    if name == query then
        return 1
    end
    local position = string.find(name, query, 1, true)
    if position == 1 then
        return 0.9
    end
    if position then
        return 0.7
    end
end

local function search(payload, strings)
    local query = string.lower(string.match(payload.text or "", "^%s*(.-)%s*$"))
    local limit = payload.limit or 8
    local results = {}
    if query == "" or limit <= 0 then
        return results
    end
    for _, app in ipairs(payload.apps or {}) do
        local score = match_score(string.lower(app.name), query)
        if score then
            results[#results + 1] = {
                id = app.id,
                title = app.name,
                item = { source = "quailcrate.apps", kind = "app", id = app.id },
                subtitle = strings.sources[app.source] or app.source,
                score = score,
                command = { id = "open_app", args = { appId = app.id } }
            }
        end
    end
    table.sort(results, function(left, right)
        if left.score ~= right.score then
            return left.score > right.score
        end
        if left.title ~= right.title then
            return left.title < right.title
        end
        return left.id < right.id
    end)
    for i = #results, limit + 1, -1 do
        results[i] = nil
    end
    return results
end

return function(request)
    local payload = request.payload or {}
    local strings = translations[payload.locale] or translations["en-US"]
    if request.op == "init" then
        return success(request, {
            search = true,
            launcher = launcher(strings),
            commands = { { id = "open_app", title = strings.open, location = "search" } }
        })
    end
    if request.op == "search" then
        local results = search(payload, strings)
        return success(request, #results > 0 and { results = results } or {})
    end
    if request.op == "command" and payload.commandId == "open_app" then
        local app_id = payload.args and payload.args.appId
        if type(app_id) ~= "string" or app_id == "" then
            return { requestId = request.requestId, ok = false,
                error = { code = "invalid_app_id", message = "Application ID is missing." } }
        end
        return success(request, {
            actions = { { type = "apps.launch", id = "launch", appId = app_id } }
        })
    end
    return success(request, {})
end
