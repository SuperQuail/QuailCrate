-- QuailCrate UI v2. These helpers only construct public JSON nodes.
local ui = {}

function ui.node(kind, id, props, events, children)
    return { type = kind, id = id, props = props, events = events, children = children }
end

function ui.toolbar(id, buttons, in_header)
    return ui.node("row", id, { gap = 4, placement = in_header and "header" or nil },
        nil, json.array(buttons))
end

function ui.button(id, label, icon, action, variant)
    return ui.node("button", id, { text = label, label = label, icon = icon, variant = variant },
        { activate = action })
end

function ui.menu_button(id, label, icon, menu, hover_only)
    return ui.node("button", id, { text = label, label = label, icon = icon,
        menu = menu, hoverOnly = hover_only })
end

return ui
