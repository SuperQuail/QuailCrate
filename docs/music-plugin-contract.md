# 音乐插件接口契约（A1）

本文档定义 `plugins/music/`（`id: "quailcrate.music"`）中页面、Lua 插件逻辑、宿主工具与桌面原生迷你播放器之间的四类消息约定。所有样例文件位于 [plugins/music/fixtures/](../plugins/music/fixtures/)，可直接被前端页面和自动化测试加载。

## 1. 所有权与生命周期

- **权威状态**：Lua 通过 `data.state` 持久化歌单、曲目来源标识（`bvid`、`aid`、`cid`、`page`）、队列、播放模式、音量、账号展示信息与当前播放元数据，并在 `tab_state` 返回不超过 512 KiB 的 `data.pageState` 快照。宿主管理音频文件和下载目录索引。播放先查本地下载、缓存和预缓冲，再按需解析 B 站地址。临时地址仅在内存中使用，Cookie 由宿主 `http.session` 通过 DPAPI 持久化。
- **页面临时状态**：前端只保存输入框文本、选中歌单/多选分 P 勾选态、抽屉展开状态、拖动中的本地预览进度；松手或提交时再通过消息桥发给 Lua。
- **后台播放**：关闭曲库窗口后音频会话继续运行，托盘与桌面迷你播放器共享同一状态；重开页面时自动向宿主同步实时进度。停用或重载插件时，宿主自动取消所有 HTTP、计时器并释放音频会话。

---

## 2. 四类消息约定

### 2.1 页面 → Lua（`page.message`）

页面经适配器发送 `{"requestId","revision","operation","args"}`；Lua 收到 `event.type == "page.message"`，其中 `event.controlId == operation`，`event.detail == args`。

| 操作名 (`operation`) | `args` 字段 | 说明 |
| --- | --- | --- |
| `library.refresh` | `{}` | 请求同步实时播放状态（如页面重开后校验位置） |
| `playlist.create` | `{name, desc?, color?}` | 创建歌单 |
| `playlist.update` | `{playlistId, name, desc?, color?}` | 编辑歌单名称、描述与色标 |
| `playlist.delete` | `{playlistId}` | 删除自定义歌单 |
| `playlist.addTracks` | `{playlistId, trackIds}` | 将已有曲目加入歌单（去重） |
| `playlist.removeTrack` | `{playlistId, trackId}` | 从歌单移除曲目 |
| `track.toggleFavorite` | `{trackId}` | 切换收藏（我喜欢的） |
| `track.delete` | `{trackId}` | 从曲库与各歌单移除曲目 |
| `view.select` | `{viewId, sortBy?}` | 记录当前视图（`smart_all`/`smart_fav`/`smart_recent`/歌单 ID）与排序（`custom`/`added`/`title`/`duration`） |
| `bilibili.loginStart` | `{}` | 申请新的 Bilibili 登录二维码并启动 2.5s 轮询 |
| `bilibili.loginCancel` | `{}` | 取消当前扫码轮询 |
| `bilibili.loginRefresh` | `{}` | 刷新已过期二维码或核验现有登录态 |
| `bilibili.logout` | `{}` | 清除宿主 `"bilibili"` Cookie 会话并退出登录 |
| `bilibili.setQuality` | `{quality}` | 设置偏好音质（`flac`/`192k`/`132k`/`64k`） |
| `bilibili.preview` | `{input}` | 解析 BV/av/视频链接/b23.tv/收藏夹，返回待选条目与部分错误 |
| `bilibili.import` | `{playlistId?, items}` | 仅导入用户选中的条目（按 `bvid + page/cid` 去重） |
| `bilibili.clearPreview` | `{}` | 清空导入预览态 |
| `playback.playTrack` | `{trackId, queueTrackIds?, toggleCurrent?}` | 曲目行传 `toggleCurrent: true`，同曲点按暂停或原位续播；选择另一首时可替换队列 |
| `download.start` / `download.remove` | `{trackId}` | 后台下载到应用私有目录，或移除本机下载。动作独立于当前播放会话 |
| `playback.playPlaylist` | `{viewId, shuffle?}` | 播放指定歌单或智能视图 |
| `playback.toggle` | `{}` | 播放 / 暂停 |
| `playback.next` / `playback.prev` / `playback.stop` | `{}` | 切歌或停止 |
| `playback.seek` | `{positionMs}` 或 `{ratio}` | 跳播（拖动结束单次提交） |
| `playback.setVolume` | `{volume, muted?}` | 调整音量（0–1）或静音 |
| `playback.cycleMode` / `playback.setMode` | `{mode?}` | 切换播放模式（`sequence`/`shuffle`/`repeat_one`） |
| `queue.add` / `queue.playNext` / `queue.remove` / `queue.clear` | `{trackId?}` | 队列追加、插为下一首、移除、清空 |

### 2.2 Lua → 页面（`pageState` 快照 & `pageReply` 回执）

- `pageState` 结构包含：`version`、`locale`、`activeView`、`sortBy`、`preferredQuality`、`playlists[]`、`tracks[]`、`recentIds[]`、`queue[]`、`account`、`login`、`importPreview`、`playback`（含 `trackId`/`status`/`positionMs`/`durationMs`/`seekable`/`volume`/`muted`/`mode`/`error`）、`notice`。
- `downloads` 以曲目 ID 为键，记录 `status`（`downloading`、`downloaded`、`error`）、可选的 `percent` 与错误信息。`smart_downloaded` 展示下载状态。手动下载长期保存，自动缓存按播放顺序保留最近 30 首，播放历史保留 50 首。仅预取的曲目在实际播放前不计入历史和缓存名额。
- `pageReply` 同步回执为小对象，如 `{"ok":true,"playlistId":"pl_..."}`、`{"pending":true}` 或 `{"ok":false,"code":"invalid_input","message":"..."}`。
- 可加载样例：
  - 成功态：[snapshot-success.json](../plugins/music/fixtures/snapshot-success.json)
  - 空曲库态：[snapshot-empty.json](../plugins/music/fixtures/snapshot-empty.json)
  - 异常恢复态：[snapshot-error.json](../plugins/music/fixtures/snapshot-error.json)

### 2.3 宿主 → Lua（工具回执与媒体事件）

- HTTP / 会话 / 音频控制回执：`event.type == "action.result"`，含 `actionId`、`status`（`ok`/`error`/`canceled`）、`code`、`detail`。
- 计时器到期：`event.type == "timer.fired"`，含 `actionId`（用于扫码轮询与播放进度同步；比对当前 `loginFlowId` / 播放会话 ID，过期直接丢弃）。
- 音频事件：`audio.stateChanged`、`audio.ended`、`audio.error`，`actionId == detail.sessionId`。非当前活动播放会话的迟到事件一律丢弃。

### 2.4 原生控件 → Lua（桌面迷你播放器 `widget.action`）

- 按钮点击：`event.type == "widget.action"`，`controlId` 为 `prev`、`play_pause`、`next`、`cycle_mode`、`open_library`。
- 进度滑块定位：`event.type == "widget.action"`，`controlId == "seek"`，`inputValue` 为 `0..1` 浮点数，拖动时本地预览，松手或方向键调整时提交一次。

完整四类消息 JSON 样例见 [messages-sample.json](../plugins/music/fixtures/messages-sample.json)。
