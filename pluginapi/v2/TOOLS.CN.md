# 宿主工具 API

Lua 和 Windows DLL 使用同一套 v2 消息。清单在 `permissions` 声明所需权限；`init` 时的 `payload.toolActions` 列出当前实例可用的工具动作。旧宿主缺少此字段。v2 请求同时带有 `payload.unixTime`（Unix 秒）。插件在可产生副作用的命令或事件响应中提交 `data.actions`，每项使用 `{type,id,args}`，同一批次的 ID 唯一。

## 动作表

| 动作 | 清单权限 | `args` 与结果 |
| --- | --- | --- |
| `http.request` | `network.http` | 必填 `url`；可选 `method,headers,body,session,timeoutMs,responseType,scope`。成功的 `detail` 含 `status,headers`，以及 `body,encoding` 或解析后的 `json`。HTTP 错误状态也返回响应，网络或解码失败才返回动作错误。 |
| `http.cancel` | `network.http` | `target` 是原请求 ID。结果为是否命中了请求；已受理的请求另收一次终态。 |
| `http.session.create`、`http.session.clear` | `network.http` | `session` 是本插件选定的标识。请求传相同标识即可复用。宿主管理并按域发送 Cookie，Windows 用户凭据保护持久 Cookie。退出登录时清除此会话。 |
| `timer.after`、`timer.cancel` | 实例自有 | 传 `delayMs`（10 毫秒至 24 小时）和可选 `scope`。到期收到 `timer.fired`，`actionId` 为原 ID。取消传 `target` 并返回布尔值。 |
| `audio.play` | `audio.play`、`network.http` | 传 `url`，可选 `headers,session`。同时提供 `cacheKey,quality` 可边播边缓存，`label` 描述实际音质。受理后返回 `sessionId,label`。每个实例只有一个活动播放会话。 |
| `audio.playCached` | `audio.play`、`network.http` | 传 `cacheKey,quality`。优先使用手动下载、整首缓存或预缓冲中的曲目，返回 `sessionId,label`。缺失返回 `not_found`，插件随后可解析在线地址。 |
| `audio.prefetch` | 同上 | 同一实例可预取一首。传 `cacheKey,quality` 查询本地来源，附 `url,headers?,session?,label?` 启动预取，传空对象撤销。预取本身不会播放或计入最近缓存。 |
| `audio.download` | 同上 | 传 `cacheKey,quality` 将已有媒体转为应用内下载，未命中返回 `not_found`。附 `url,headers?,session?,label?` 可从网络获取。传输完成后发送最终回执。 |
| `audio.downloads`、`audio.removeDownload` | `audio.play` | 列表传 `{}`，返回 `items`，含 `cacheKey,quality,label,status,bytes,percent?`。移除传 `cacheKey`。只管理本插件的下载文件。 |
| `audio.getState`、`audio.pause`、`audio.resume`、`audio.stop` | `audio.play` | `target` 为宿主返回的播放会话 ID。结果含 `sessionId,status,positionMs,durationMs,seekable,volume`。 |
| `audio.seek`、`audio.setVolume` | `audio.play` | 除 `target` 外分别传 `positionMs` 或 `volume`（0–1）。定位前检查 `seekable`。 |
| `files.list`、`files.read` | 私有目录自动可用，外部目录需 `files.directory` 和目录授权 | `path` 为相对路径，目录分隔符为 `/`，`root` 省略或为 `private` 时使用插件私有目录。列举返回含 `name,directory` 的数组。读取返回 `body,encoding`（`utf8` 或 `base64`）。 |
| `files.mkdir`、`files.write`、`files.remove` | 同上；外部目录还需写授权 | `path` 指向一个文件或目录。写入传 `body` 和可选 `encoding`，完整替换文件。删除仅处理单个文件或空目录。单文件上限 512 KiB，私有目录总量上限 16 MiB。 |
| `files.pickDirectory` | `files.directory` 和用户选择 | `access` 为 `read` 或 `write`（可读写）。由可信用户交互触发系统选择器。成功返回目录授权的 `id,path,access`；后续操作传 `root: id`。 |
| `files.directories`、`files.revokeDirectory` | `files.directory` | 前者列出本插件的目录授权，后者传 `root` 撤销指定引用。宿主设置也可查看和撤销。 |

## 请求示例

```json
{"actions":[{"type":"http.request","id":"feed-1","args":{"url":"https://example.org/feed.json","responseType":"json","scope":"page"}}]}
```

```json
{"type":"action.result","actionId":"feed-1","status":"ok","detail":{"status":200,"headers":{"Content-Type":["application/json"]},"json":{"items":[]}}}
```

```json
{"type":"action.result","actionId":"feed-1","status":"error","code":"permission_denied","error":"permission_denied"}
```

结构化回执用 `detail`；旧对话框的字符串 `value` 保持原约定。插件可按 `status` 处理 `ok`、`error`、`canceled`，按 `code` 区分 `timeout`、`busy`、`not_found` 等原因。`scope` 默认是 `instance`；来源为标签页时可指定 `page`，关闭或切换该页即取消请求/计时器，并丢弃旧页回执。停用插件会取消本代所有任务。

## 网络与媒体边界

- 访问本机或内网需额外声明并取得 `network.local`。请求和重定向逐跳检查目标。请求头支持 `Authorization`、`Referer` 等；Cookie 由登录会话维护。跨来源重定向剥离敏感头。
- HTTP 超时默认 15 秒、上限 60 秒，最多 5 次重定向。普通 HTTP 请求体和解压后响应体各限 256 KiB。每个实例最多 8 个网络流、32 个计时器。`responseType` 可取 `text`、`json`、`base64`。
- 音频资源通过同一网络代理读取，支持 HTTP Range。标准 16 位 PCM WAV 和受支持的分片 AAC 可在下载过程中起播。其他支持的格式在完整校验后交给 Media Foundation。单音源上限 128 MiB。宿主按插件隔离音频文件，最近缓存上限为 30 首不同曲目，手动下载单独持久保存。
- 播放状态变化、自然结束和失败分别投递 `audio.stateChanged`、`audio.ended`、`audio.error`。事件的 `actionId` 和 `detail.sessionId` 都是播放会话 ID；`audio.play` 的受理回执关联插件自己提交的动作 ID。关闭曲库窗口后继续播放，停用插件时释放设备和临时缓存。
