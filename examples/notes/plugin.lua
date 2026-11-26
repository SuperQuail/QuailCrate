local ui = require("quailcrate.ui.v2")
local source = "example.notes"

return function(request)
    local p = request.payload
    local zh = p.locale == "zh-CN"
    local function ok(data) return { requestId=request.requestId, ok=true, data=data or {} } end
    local state = p.state
    if type(state) ~= "table" or type(state.notes) ~= "table" then
        state = { nextId=2, notes={{id="1", text=zh and "第一条笔记" or "My first note"}} }
    end
    local title = zh and "笔记" or "Notes"
    local new = zh and "新建笔记" or "New note"
    local save = zh and "添加到分组" or "Add to group"
    local function find(id)
        for _,note in ipairs(state.notes) do if note.id == id then return note end end
    end
    local function ref(id) return {source=source,kind="note",id=id} end
    local function show(note) return {type="ui.message",title=title,value=note.text} end
    local function open(id)
        local n=find(id)
        return ok(n and {actions={show(n)}} or {})
    end
    if request.op=="init" then
        return ok({search=true,state=state,commands={{id="open",title=title,location="search"}},
            appTabs={{id="notes",title=title,icon="edit"}}})
    elseif request.op=="tab_state" then
        local items=json.array()
        local selected=state.selected or (state.notes[1] and state.notes[1].id)
        for _,n in ipairs(state.notes) do items[#items+1]={key=n.id,text=n.text,variant=n.id==selected and "selected" or nil} end
        local children={ui.button("new",new,nil,"new"),
            ui.node("virtualList","entries",{grow=1,items=items},{activate="select"})}
        for _,service in ipairs(p.services or {}) do
            if service.id=="quailcrate.groups.items" then
                children[#children+1]=ui.button("save",save,nil,"save")
                break
            end
        end
        return ok({view={uiVersion=2,title=title,root=ui.node("column","notes",{gap=6},nil,children)}})
    elseif request.op=="search" then
        local results=json.array()
        for _,n in ipairs(state.notes) do
            if string.find(string.lower(n.text),string.lower(p.text or ""),1,true) then
                results[#results+1]={id=n.id,title=n.text,subtitle=title,item=ref(n.id),score=0.8,command={id="open",args={id=n.id}}}
                if #results>=p.limit then break end
            end
        end
        return ok({results=results})
    elseif request.op=="command" then
        return open((p.args or {}).id)
    elseif request.op=="service" then
        local item=(p.args or {}).item or {}
        local n=item.source==source and item.kind=="note" and find(item.id)
        if not n then return ok({reply={status="error",code="unavailable"}}) end
        return ok({reply={status="ok",value={id=n.id}},actions={show(n)}})
    elseif request.op=="event" then
        local e=p.event
        if e.type=="tab.action" and e.controlId=="new" then
            return ok({actions={{type="ui.promptText",id="new",title=new}}})
        elseif e.type=="action.result" and e.actionId=="new" and not e.canceled and e.value and #e.value>0 then
            local n={id=tostring(state.nextId),text=e.value}
            state.notes[#state.notes+1]=n
            state.nextId=state.nextId+1
            state.selected=n.id
            return ok({state=state})
        elseif e.type=="tab.action" and e.controlId=="select" then
            local n=find(e.itemId)
            if n then state.selected=n.id;return ok({state=state,actions={show(n)}}) end
        elseif e.type=="tab.action" and e.controlId=="save" then
            local n=find(state.selected) or state.notes[1]
            if n then return ok({actions={{type="service.call",id="save",service="quailcrate.groups.items",
                method="add",args={item=ref(n.id),title=n.text}}}}) end
        elseif e.type=="service.result" and e.actionId=="save" and e.status=="error" then
            return ok({actions={{type="ui.message",title=title,value=zh and "分组服务不可用。" or "The groups service is unavailable."}}})
        end
    end
    return ok()
end
