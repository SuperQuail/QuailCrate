return function(request)
    if request.op ~= "init" then return {requestId=request.requestId,ok=true,data={}} end
    local zh=request.payload.locale=="zh-CN"
    local function label(english,chinese) return zh and chinese or english end
    local function card(kind,english,chinese) return {type=kind,title=label(english,chinese)} end
    local function section(id,title,description,icon,cards)
        return {id=id,title=title,description=description,icon=icon,cards=cards}
    end
    local settings = {uiVersion=2,title=label("Settings","设置"),appsTitle=label("Apps","应用"),sections={
        section("general",label("General","常规"),label("Shortcut and interface language","快捷键和界面语言"),"settings",{
            card("hotkey","Launcher shortcut","快速启动快捷键"),card("language","Interface language","界面语言")}),
        section("appearance",label("Appearance","外观"),label("Colors, themes, opacity, radius and icon size","颜色、主题、透明度、圆角与图标尺寸"),"palette",{
            card("target","Component","组件"),card("presets","Theme presets","预设主题"),
            card("opacity","Opacity","不透明度"),card("radius","Corners & accents","圆角与装饰"),
            card("iconSize","Icon size","图标尺寸"),card("colors","Custom colors","自定义颜色")}),
        section("desktop",label("Desktop","桌面"),label("Arrange desktop widgets","排列桌面控件"),"layout",{
            card("desktopLayout","Desktop layout","桌面布局")}),
        section("commands",label("Commands","指令"),label("Commands from enabled plugins","已启用插件提供的指令"),"terminal",{
            card("commands","Plugin commands","插件指令")}),
        section("plugins",label("Plugins","插件"),label("Manage installed plugins and applications","管理已安装的插件与应用"),"puzzle",{
            card("plugins","Installed plugins","已安装的插件")})
    }}
    local tray={
        {id="open",title=label("Open launcher","打开快速启动"),icon="rocket",intent="launcher.open",order=10},
        {id="pick",title=label("Choose launcher interface","选择快速启动界面"),intent="launcher.choose",order=15},
        {separator=true,order=30},
        {id="edit",title=label("Edit desktop layout","编辑桌面布局"),activeTitle=label("Finish editing layout","完成布局编辑"),
            icon="layout",intent="desktop.toggleEdit",order=40},
        {id="appearance",title=label("Appearance and themes","外观与组件配色"),icon="palette",intent="settings.appearance",order=50}
    }
    return {requestId=request.requestId,ok=true,data={settings=settings,tray=tray}}
end
