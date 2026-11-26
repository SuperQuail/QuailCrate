# 包内应用页与原生值提交

采用 `schemaVersion: 2`、`hostApiVersion: 2`、`uiVersion: 2` 的插件可在清单声明 `appTabs`。Lua 和原生 DLL 返回相同 JSON。一次 `init` 可同时注册两类应用页。

```json
{"appTabs":[{"id":"library","title":"资料库","page":"web/index.html","pageActions":["item.rename"]},{"id":"summary","title":"摘要"}]}
```

填写 `page` 的标签在 `tab_state` 查询中返回 `data.pageState`，内容为不超过 512 KiB 的 JSON 对象。省略 `page` 的标签继续返回原有的 `data.view`。每页最多声明 128 个操作名。界面从快照中只取得业务需要展示的字段，登录凭据应由登录会话管理。

## 页面消息

1. 窗口适配器从 `LoadTab` 获取 `Document.URL`、`Document.State` 和宿主签发的 `PageRef.Revision`。首次打开时载入入口，后续收到 `ObserveApps` 通知时重新查询快照，更新已打开页面的状态与修订号。
2. 页面向适配器提交请求，如 `{"requestId":"rename-1","revision":1,"operation":"item.rename","args":{"id":"item-7","name":"旅行"}}`。`revision` 回显所见快照的宿主修订号。`args` 为不超过 64 KiB 的 JSON 对象。请求 ID 和操作名使用与 v2 控件动作相同的 ID 格式。
3. 适配器使用当前页面引用及浏览器报告的顶层文档完整 URL 调用 `SendPageMessage`。宿主向插件投递 `event`，载荷示例如下。

```json
{"tabId":"library","event":{"type":"page.message","tabId":"library","actionId":"rename-1","controlId":"item.rename","detail":{"id":"item-7","name":"旅行"}}}
```

4. 插件处理操作，按需提交自身的 `data.state`、已授权的 `data.actions`，并可返回 64 KiB 内的 `data.pageReply` JSON 对象。宿主完成提交后返回带原请求 ID 和输入修订号的回执，如 `{"requestId":"rename-1","revision":1,"value":{"saved":true}}`。异步动作提交成功可用 `{"pending":true}` 表示等待，终态经后续快照更新。适配器按同一会话和请求 ID 投递回执，处理关闭或重开的情况。
5. 同一旧修订上的重复操作、失效会话和外部文档来源均会被拒绝。适配器重新获取当前快照，由用户决定是否再次提交业务操作。

普通网页消息一律走 `SendPageMessage`。当窗口通过原生输入或浏览器提供的可信用户激活信号确认**本条消息**由用户触发时，可选用 `SendPageUserMessage`。这个入口支持真正的点击发起原生选择框，例如已获 `files.directory` 权限的插件可在回调中提交 `files.pickDirectory`。浏览器无法证实具体的手势时走普通入口；网页传入的 `isTrusted`、时间戳等不能提供该证明。用户选定目录后才会产生授权引用，页面关闭会取消在途选择。

## 包资源和窗口接入

入口采用包内相对路径，扩展名为 `.html`。入口所在目录是该页面的资源根，可放本地 JS、CSS、图片和字体。用相对路径引用文件。把脚本和样式放在独立文件中。页面可从快照取得小尺寸受支持图片的 data URL，或用包内 JS 生成二维码。大图片应随包放置或缩小到快照限制以内。

Windows 窗口适配器对接 [PageService](../../internal/pluginapps/pages.go)，使用宿主为这次激活分配的虚拟 HTTPS URL。拦截本地页面资源，交由 `ReadPageAsset` 读取并原样应用 `PageAsset.Headers()`。单个资源上限 4 MiB。首次导航只允许宿主给定的入口。适配器在首次导航前拦截后续导航、子框架、弹窗、下载、外部协议及远程或文件资源，关闭原生浏览器权限、脚本宿主对象和跨插件共享存储。仅为顶层入口接入 JSON 消息，页面 JS 通过插件调用公共网络和音频动作。失效回调随会话丢弃。运行时缺失和资源错误由窗口展示重试入口，新会话重新取 URL。

窗口联调可复用 [pagetest.Browser](../../internal/pluginapps/pagetest/browser.go)，覆盖打开、消息回执、快照更新和关闭。将 `Unavailable` 设为真可以复现网页运行时缺失，切换为假后可再次打开。

### 原生数值与文本

可在原生 v2 树声明 `{"type":"slider","id":"position","props":{"value":0.4},"events":{"submit":"seek"}}`，或用 `{"type":"input","id":"title","events":{"submit":"rename"}}` 声明文本提交。窗口侧可按 `SubmitTabValue` 对接，提交数值或文本后插件收到 `tab.action`。桌面组件使用 `widget.action`，带上来源组件的代次及视图修订。载荷中的 `controlId` 是绑定动作，`inputValue` 为 0 到 1 的数值或 JSON 字符串。拖动过程中在本地预览，释放时提交一次。普通点击沿用现有事件格式。
