local function success(request, data)
    return { requestId = request.requestId, ok = true, data = data }
end

return function(request)
    local payload = request.payload or {}
    local strings = payload.strings or {}
    local title = strings.title or "Example Counter"
    local overview = strings.overview or "Overview"
    local counter = strings.counter or "Counter"
    if request.op == "init" then
        return success(request, {
            widgets = { { id = "counter", title = title, width = 250, height = 150, default = true } },
            appTabs = {
                { id = "overview", title = overview },
                { id = "counter", title = counter }
            }
        })
    end
    local count = type(payload.state) == "table" and payload.state.count or 0
    if request.op == "tab_state" or request.op == "widget_state" then
        local controls = {
            { id = "count", type = "text", text = (strings.count or "Current count") .. "  " .. tostring(count) }
        }
        if request.op == "tab_state" and payload.tabId == "overview" then
            return success(request, { view = { title = overview, controls = controls } })
        end
        controls[2] = { id = "minus", type = "button", text = strings.minus or "Decrease" }
        controls[3] = { id = "plus", type = "button", text = strings.plus or "Increase" }
        local viewTitle = request.op == "tab_state" and counter or title
        return success(request, { view = { title = viewTitle, controls = controls } })
    end
    local event = payload.event or {}
    local counterAction = event.type == "widget.action" or (event.type == "tab.action" and event.tabId == "counter")
    if request.op == "event" and counterAction then
        local delta = event.controlId == "plus" and 1 or (event.controlId == "minus" and -1 or 0)
        if delta ~= 0 then
            return success(request, {
                state = { count = count + delta },
                actions = { { type = "widget.refresh", widgetId = event.widgetId } }
            })
        end
    end
    return success(request, {})
end
