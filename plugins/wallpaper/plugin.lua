local function success(request, data)
    return { requestId = request.requestId, ok = true, data = data }
end

return function(request)
    local payload = request.payload or {}
    local strings = payload.strings or {}
    if request.op == "init" then
        return success(request, {
            commands = { { id = "choose", title = strings.settings or "Set system wallpaper", location = "settings" } }
        })
    end
    if request.op == "command" and payload.commandId == "choose" then
        return success(request, {
            actions = { { type = "ui.pickPaths", id = "pick_wallpaper", title = strings.picker or "Choose system wallpaper",
                extensions = { "jpg", "png" } } }
        })
    end
    if request.op == "event" then
        local event = payload.event or {}
        if event.type == "action.result" and event.actionId == "pick_wallpaper" and not event.canceled
            and not event.error and event.paths and event.paths[1] then
            return success(request, {
                actions = { { type = "wallpaper.set", id = "apply_wallpaper", path = event.paths[1] } }
            })
        end
        if event.type == "action.result" and event.actionId == "apply_wallpaper" then
            return success(request, {
                actions = { { type = "ui.message",
                    title = event.error and "Could not set wallpaper" or (strings.updated or "Wallpaper updated"),
                    value = event.error or strings.done or "The desktop wallpaper was set." } }
            })
        end
    end
    return success(request, {})
end
