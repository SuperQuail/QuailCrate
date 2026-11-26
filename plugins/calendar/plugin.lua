local ui = require("quailcrate.ui.v2")
local browsing = {}
local selected = {}
local days_by_month = { 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31 }
local offsets = { 0, 3, 2, 5, 0, 3, 5, 1, 4, 6, 2, 4 }
local translations = {
    ["en-US"] = {
        calendar = "Desktop Calendar", waiting = "Syncing local clock...",
        previous = "Previous month", next = "Next month", today = "Jump to today",
        weekdays = { "MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN" },
        month_title = function(year, month)
            local names = { "January", "February", "March", "April", "May", "June",
                "July", "August", "September", "October", "November", "December" }
            return string.format("%s %d", names[month], year)
        end
    },
    ["zh-CN"] = {
        calendar = "桌面月历", waiting = "正在同步本地日期...",
        previous = "上一月", next = "下一月", today = "回到今天",
        weekdays = { "一", "二", "三", "四", "五", "六", "日" },
        month_title = function(year, month)
            return string.format("%d 年 %d 月", year, month)
        end
    }
}

local function success(request, data)
    return { requestId = request.requestId, ok = true, data = data }
end

local function local_date(value)
    local year, month, day = string.match(value or "", "^(%d%d%d%d)%-(%d%d)%-(%d%d)$")
    if not year then
        return nil
    end
    return { year = tonumber(year), month = tonumber(month), day = tonumber(day) }
end

local function leap_year(year)
    return year % 4 == 0 and (year % 100 ~= 0 or year % 400 == 0)
end

local function month_length(year, month)
    if month == 2 and leap_year(year) then
        return 29
    end
    return days_by_month[month]
end

local function shift_month(year, month, offset)
    local absolute = year * 12 + month - 1 + offset
    return { year = math.floor(absolute / 12), month = absolute % 12 + 1 }
end

local function weekday_index(year, month, day)
    local y = year
    if month < 3 then
        y = y - 1
    end
    local sunday_zero = (y + math.floor(y / 4) - math.floor(y / 100)
        + math.floor(y / 400) + offsets[month] + day) % 7
    return (sunday_zero + 6) % 7
end

local function day_of_year(year, month, day)
    local total = day
    for m = 1, month - 1 do
        total = total + month_length(year, m)
    end
    return total
end

local function iso_week(year, month, day)
    local doy = day_of_year(year, month, day)
    local dow = weekday_index(year, month, day) + 1
    local week = math.floor((doy - dow + 10) / 7)
    if week < 1 then
        return iso_week(year - 1, 12, 31)
    end
    if week == 53 then
        local jan1 = weekday_index(year, 1, 1) + 1
        local has53 = (jan1 == 4) or (leap_year(year) and jan1 == 3)
        if not has53 then
            return 1
        end
    end
    return week
end

local function serial_day(year, month, day)
    local y = year
    if month <= 2 then
        y = y - 1
    end
    local era = math.floor(y / 400)
    local yoe = y - era * 400
    local doy = math.floor((153 * (month + (month > 2 and -3 or 9)) + 2) / 5) + day - 1
    local doe = yoe * 365 + math.floor(yoe / 4) - math.floor(yoe / 100) + doy
    return era * 146097 + doe
end

local function same_day(a, b)
    return a and b and a.year == b.year and a.month == b.month and a.day == b.day
end

local function month_grid(year, month, today, picked, weekdays)
    local grid = {}
    for column, name in ipairs(weekdays) do
        grid[#grid + 1] = { key = "weekday-" .. column, text = name, role = "columnheader",
            variant = column >= 6 and "secondaryAccent" or "muted" }
    end
    local first = weekday_index(year, month, 1)
    local days = month_length(year, month)
    local previous = shift_month(year, month, -1)
    local previous_days = month_length(previous.year, previous.month)
    local following = shift_month(year, month, 1)
    for cell = 1, 42 do
        local offset = cell - first
        local cy, cm, cd, state
        local is_weekend = ((cell - 1) % 7) >= 5
        if offset < 1 then
            cy, cm, cd = previous.year, previous.month, previous_days + offset
            state = "muted"
        elseif offset > days then
            cy, cm, cd = following.year, following.month, offset - days
            state = "muted"
        else
            cy, cm, cd = year, month, offset
            if cy == today.year and cm == today.month and cd == today.day then
                state = "accent"
            elseif picked and cy == picked.year and cm == picked.month and cd == picked.day then
                state = "selected"
            elseif is_weekend then
                state = "secondaryAccent"
            end
        end
        grid[#grid + 1] = {
            key = string.format("day-%04d-%02d-%02d", cy, cm, cd),
            label = string.format("%04d-%02d-%02d", cy, cm, cd),
            text = tostring(cd),
            variant = state
        }
    end
    return grid
end

local function telemetry_info(today, picked)
    local target = picked or today
    local week = iso_week(target.year, target.month, target.day)
    local doy = day_of_year(target.year, target.month, target.day)
    local total = leap_year(target.year) and 366 or 365
    local pct = math.floor(doy * 100 / total + 0.5)
    local iso = string.format("%04d-%02d-%02d", target.year, target.month, target.day)
    local text
    if picked and not same_day(picked, today) then
        local delta = serial_day(picked.year, picked.month, picked.day) - serial_day(today.year, today.month, today.day)
        local sign = delta > 0 and "+" or ""
        text = string.format("%s  ·  %s%dd  ·  W%02d  ·  D%03d/%d", iso, sign, delta, week, doy, total)
    else
        text = string.format("%s  ·  W%02d  ·  D%03d/%d  ·  %d%%", iso, week, doy, total, pct)
    end
    local summary = string.format("%04d.%02d.%02d · W%02d", today.year, today.month, today.day, iso_week(today.year, today.month, today.day))
    return text, doy / total, summary
end

local function view(instance, today, strings)
    if not today then
        return { uiVersion = 2, title = strings.calendar,
            root = { type = "text", id = "waiting", props = { text = strings.waiting } } }
    end
    local shown = browsing[instance] or today
    local footer, progress, summary = telemetry_info(today, selected[instance])
    local toolbar = ui.toolbar("navigation", {
        ui.button("back", strings.previous, "chevron-left", "previous"),
        ui.button("jump", strings.today, "today", "today", "accent"),
        ui.button("forward", strings.next, "chevron-right", "next"),
    }, true)
    return {
        uiVersion = 2,
        title = strings.month_title(shown.year, shown.month),
        summary = summary,
        menu = {
            { id = "today", type = "button", text = strings.today, icon = "today" },
            { id = "previous", type = "button", text = strings.previous, icon = "chevron-left" },
            { id = "next", type = "button", text = strings.next, icon = "chevron-right" }
        },
        root = { type = "column", id = "month", props = { gap = 3 },
            events = { wheelUp = "previous", wheelDown = "next" },
            children = {
                toolbar,
                { type = "grid", id = "dates", props = { columns = 7, grow = 1,
                    items = month_grid(shown.year, shown.month, today, selected[instance], strings.weekdays) },
                    events = { activate = "days" } },
                { type = "text", id = "summary", props = { text = footer, variant = "caption", height = 19 } },
                { type = "progress", id = "year-progress", props = { value = progress, height = 2 } }
            }
        }
    }
end

local function set_browsing(widget_id, target, today)
    if target.year == today.year and target.month == today.month then
        browsing[widget_id] = nil
    else
        browsing[widget_id] = target
    end
end

return function(request)
    local payload = request.payload or {}
    local strings = translations[payload.locale] or translations["en-US"]
    if request.op == "init" then
        return success(request, {
            widgets = { { id = "month", title = strings.calendar, width = 330, height = 312,
                default = true, anchor = "right" } },
            subscribe = { "date.changed" }
        })
    end
    local today = local_date(payload.date)
    if request.op == "service" then
        local date = local_date((payload.args or {}).date) or today
        local days = date and month_length(date.year, date.month)
        if not days or date.day < 1 or date.day > days then
            return success(request, { reply = { status = "error", code = "invalid_argument" } })
        end
        local month = { date = string.format("%04d-%02d-%02d", date.year, date.month, date.day), days = days }
        return success(request, { reply = { status = "ok", value = month } })
    end
    if request.op == "widget_state" then
        return success(request, { view = view(payload.widgetId, today, strings) })
    end
    if request.op == "event" then
        local event = payload.event or {}
        if event.type == "date.changed" then
            local actions = {}
            for _, widget in ipairs(payload.widgets or {}) do
                actions[#actions + 1] = { type = "widget.refresh", widgetId = widget.id }
            end
            return success(request, #actions > 0 and { actions = actions } or {})
        end
        local instance = event.widgetId
        if event.type == "widget.action" and instance and today then
            local shown = browsing[instance] or today
            if event.controlId == "today" then
                browsing[instance] = nil
                selected[instance] = nil
            elseif event.controlId == "previous" and (shown.year > 1 or shown.month > 1) then
                set_browsing(instance, shift_month(shown.year, shown.month, -1), today)
            elseif event.controlId == "next" and (shown.year < 9999 or shown.month < 12) then
                set_browsing(instance, shift_month(shown.year, shown.month, 1), today)
            elseif event.controlId == "days" and type(event.itemId) == "string" then
                local y, m, d = string.match(event.itemId, "^day%-(%d%d%d%d)%-(%d%d)%-(%d%d)$")
                if not y then
                    return success(request, {})
                end
                local picked = { year = tonumber(y), month = tonumber(m), day = tonumber(d) }
                if picked.year ~= shown.year or picked.month ~= shown.month then
                    set_browsing(instance, { year = picked.year, month = picked.month }, today)
                end
                if same_day(picked, today) or same_day(picked, selected[instance]) then
                    selected[instance] = nil
                else
                    selected[instance] = picked
                end
            else
                return success(request, {})
            end
            return success(request, { actions = { { type = "widget.refresh", widgetId = event.widgetId } } })
        end
    end
    return success(request, {})
end
