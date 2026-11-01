# 实时消息通知（WebSocket）设计方案

日期：2026-11-01
状态：已评审（设计阶段）

## 1. 背景

### 1.1 现状

消息链路的前半段已经完备，实时这一段缺失：

| 环节 | 现状 |
|---|---|
| 事件产生 | `esports_outbox` 外盒事件表（幂等键、租约、退避、死信） |
| 收件人解析 | `notification_recipients.go` 解析出 `customer:<id>` / `player:<id>` / 管理员 UID |
| 落库 | `ent.Message`（表 `admin_messages`），按 `user_uid` 存收件地址 |
| 读取 | 管理端 `/admin/auth/messages`；客户 `/user/messages`；陪玩 `/user/player/messages` |
| 已读 | 对应 `POST .../messages/read`，更新条件含地址，越权安全 |
| 实时 | **仅管理端 SSE**（`/api/v1/admin/sse/connect`，ticket 5 分钟）；三端无任何推送 |

三端（customer / player / cs）只有 60s 陪玩心跳，没有推送通道。抢单大厅 `hall.vue` 只在 `onShow` 刷新，不自动更新。

### 1.2 问题

- 陪玩端收不到优先通知 / 派单邀请，只能靠切页面刷新
- 客户看不到订单状态变化，客服要靠手动刷新看板
- 抢单大厅的新单要重新进页面才可见

### 1.3 结论

不新建第二套消息系统。`outbox → ent.Message` 已经是「落库 + 未读 + 补拉」，缺的只是「有新消息」这一声提示。WebSocket 只承担这个提示。

## 2. 范围

通知范围（已确认）：

- 陪玩端：新单、优先通知、派单邀请、订单状态
- 用户端：订单状态、客服消息
- 客服端：新单、异常提醒
- 陪玩抢单大厅：新单实时刷新

投递方式：WebSocket 统一通道（`uni.connectSocket` 覆盖 H5 / App / 小程序）。
持久化：落库 + 未读 + 补拉（复用既有 `ent.Message`）。

## 3. 设计原则

1. **WS 只送信封，正文走补拉。** 不新建消息表，不通过 WS 传正文。断线期间的消息本来就在库里，重连后 `GET .../messages` 拿得到。
2. **address 是不透明字符串。** `customer:12`、`player:12`、管理员 UID 对框架层没有任何含义，框架层只负责「把消息送到这个字符串对应的连接」。
3. **分层归属**：cmf 只懂协议，go-admin 只懂「地址 + 消息表」，功能集只懂身份与业务事件。

## 4. 架构

依赖方向（单向，不允许反向）：

```
cmf/ws            （协议：hub / ticket / 升级 / 事件帧）
   ↑
go-admin/internal/ws   （装配：单例 / ticket 端点 / address provider / ent hook）
   ↑
go-admin/internal/apps/esports   （功能集：注册 provider + 业务广播）
```

全局只有一个 WS 升级端点和一个 hub 单例。address 是全局命名空间，靠前缀区分归属（esports 占 `customer:` / `player:`，其他功能集使用自己的前缀）。一条连接可绑定多个功能集的 address，前端只连一条。

### 4.1 为什么自己做房间，不用成熟库

| 候选 | 不适用的原因 |
|---|---|
| centrifugo / centrifuge | 独立进程 + connect proxy 鉴权 + 前端 SDK；当前单实例，收益为零 |
| googollee/go-socket.io | Socket.IO 协议需要 HTTP polling 再升级，`uni.connectSocket` 接不上 |
| olahol/melody | 底层 gorilla/websocket，Fiber v3 需套 HTTP adaptor；房间仍要自己建 |
| leandro-lugaresi/hub | 只省一把锁，连接生命周期仍要自己写 |

连接层（握手、帧解析、ping/pong、并发写限制）使用 `github.com/gofiber/contrib/v3/websocket`，不自造。房间层自写，约 70 行。

### 4.2 换用成熟方案的触发条件

满足任一条件时改用 centrifugo，替换点是 `cmf/ws` 的 `Hub` 实现：

1. 后端水平扩展到 2 个以上实例（进程内 hub 无法跨实例广播）
2. 需要客户端主动订阅频道（不再全是服务端定向推）
3. 需要 presence 或由推送库维护消息历史

## 5. `cmf/ws`（框架层）

新增包，与 `cmf/sse` 平级。依赖仅 `fiber` + 标准库 + `gofiber/contrib/v3/websocket`。

| 文件 | 内容 |
|---|---|
| `hub.go` | `rooms map[string]map[*client]struct{}`；`Join` / `Leave` / `Send(address)` / `Broadcast(room)` |
| `ticket.go` | 一次性 ticket，值为 `[]string`（多个 address），默认 2 分钟有效，后台定期清理 |
| `handler.go` | ticket 校验 → 升级 → 下发 `ready` → 读循环（ping / ReadDeadline） |
| `event.go` | `Event{Type string, Data any}`，JSON 编解码 |

注入点（框架层不碰数据库）：

```go
// 建连后 ready 帧需要的未读数，由应用层提供
UnreadCounter func(ctx context.Context, addresses []string) (int, error)
```

### 5.1 连接生命周期

- 客户端每 25s 发送 `{"type":"ping"}`；服务端 `SetReadDeadline(60s)`，两个周期未收到即关闭
- 每个连接一个独立写协程 + 有界 channel（容量 32）。channel 满即关闭连接，**不阻塞房间内其他连接**
- 关闭时从所有 address 房间移除（`Leave`）
- 同一 address 允许多条连接（多设备），全部收到
- 每个 address 最多 5 条连接，超出拒绝第 6 条（HTTP 429）

### 5.2 ticket 语义

- 一次性消费，2 分钟有效，只用于「拿到后立刻建连」
- 不用 JWT：`uni.connectSocket` 在 App / 小程序端无法设置自定义 header，放 query 会被日志记录
- **不改造 `cmf/sse.TicketStore`**（当前只支持单个 uid，管理端 SSE 正在使用）：新写 `ws.TicketStore`，约 60 行，零回归风险。两者的 payload 语义（单身份 vs 多身份）本就不同

## 6. `go-admin/internal/ws`（应用层）

不 import 任何功能集。四个文件，合计约 150 行。

| 文件 | 内容 |
|---|---|
| `hub.go` | `cmfws.Hub` 进程内单例 + `RealtimeHub()` 取用 |
| `ticket.go` | 两个 ticket 端点：`addresses = [uid] + providers(uid)` |
| `address.go` | `RegisterAddressProvider(fn func(ctx, uid) ([]string, error))` |
| `message_hook.go` | ent `Message.Create` hook → `RealtimeHub().Send(m.UserUID, message.created)` |

import 别名避免混淆：

```go
cmfws "github.com/wuwuseo/cmf/ws"
"go-admin/internal/ws"
```

### 6.1 路由

注册位置分两种（与现有代码一致）：

```go
// 1) 管理端：internal/router.go 的 adminAuth 组，直接注册
adminAuth.Post("/realtime/ticket", ws.AdminTicketHandler)

// 2) 通用用户端：按功能集路由表格式声明在 go-admin 侧（无需登录态的路由使用 Auth 零值）
{Method: "POST", Path: "/auth/realtime/ticket", Auth: cmfapp.AuthUser, Handler: ws.TicketHandler}
{Method: "GET",  Path: "/realtime",            Handler: ws.ConnectHandler} // Auth 留空即免认证，与 /payment-notify/:channel 同写法
```

WS 升级的鉴权来自 query 中的 ticket，不走 Auth 中间件：ticket 校验失败时返回 401，不升级。这里沿用项目已有的免认证写法，不新增 Auth 常量。

- 管理员与通用用户默认只需要自己的 uid 作为一个 address（管理端消息中心的地址规则就是 `Admin.UID`）
- 客户 / 陪玩的 `customer:<id>` / `player:<id>` 由功能集注册的 provider 补充
- 同一账号可能既是客户又是陪玩，ticket 返回 addresses 数组，连接一次性绑定全部

### 6.2 推送挂载点：ent `Message.Create` hook

**这是「go-admin 不包含功能集」的关键**：任何功能集只要写一条 `ent.Message`，就自动获得实时推送，接入成本为零。

```go
// 消息落库后推送信封：只带 id / type / 未读数，不传正文
// ponytail: hook 在事务内触发，回滚时会多推一条提示。前端收到后重新拉列表，
// 列表里没有该条、未读数由 UnreadCounter 重算，用户无感。若将来要求严格一致，
// 改为在事务提交后推送（需要显式事务包装点）。
```

已知边界：hook 覆盖单条 `Create`；若将来引入 `CreateBulk`，需先验证 hook 是否被覆盖，未覆盖则在调用点补推。

注册与注入都在启动阶段完成，共两处：

```go
// internal/init.go：ent client 初始化后挂 hook
db.Use(ws.MessageHook())

// internal/ws/hub.go：构造 hub 时注入未读统计（框架层不碰数据库）
cmfws.Config{
	UnreadCounter: func(ctx context.Context, addresses []string) (int, error) {
		return global.ORM.Message.Query().
			Where(message.UserUIDIn(addresses...), message.ReadAtIsNil()).
			Count(ctx)
	},
}
```

### 6.3 大厅广播

功能集直接调用通用 hub：

```go
RealtimeHub().Broadcast("hall", cmfws.Event{Type: "hall.order_created", Data: ...})
```

`"hall"` 只是字符串，框架层与 go-admin 层都不认识它。

**服务端不逐个判断「这个陪玩能不能抢」**：`playerCanGrabNewOrder` 已经在大厅列表接口中执行，广播再判断一次会形成第二套口径。前端收到事件后调用 `load()`，结果以列表接口为准。

### 6.4 现有代码处置

| 现有 | 处置 |
|---|---|
| `apps/esports/client_messages.go`（`/user/messages`、`/user/player/messages`） | **保留**。客户端已接入，且依赖 esports 的客户 / 陪玩身份解析；不新增、不上收 |
| `apps/esports/notification_outbox.go` | 移除推送职责，站内信写入逻辑不变；推送由 hook 接管 |
| 管理端 SSE（`/api/v1/admin/sse/*`、`global.Hub`） | **完全不动**，管理后台已跑通 |

## 7. 事件格式

服务端 → 客户端：

```json
{"type":"ready","data":{"addresses":["player:12"],"unread":3}}
{"type":"message.created","data":{"id":123,"category":"order.seat_invited","unread":4}}
{"type":"hall.order_created","data":{"order_id":9}}
{"type":"pong"}
```

客户端 → 服务端：

```json
{"type":"ping"}
```

约定：

- `category` 即 `ent.Message.type`。前端据此决定弹条还是静默红点，**新增业务通知类型时前端无需改代码**
- `unread` 随每次推送下发，红点不必再发请求
- `ready` 在建连成功时下发一次，带上本次绑定的 address 与当前未读
- 带 `id` 不带正文，前端点击时走 `GET .../messages` 取详情
- `unread` 由服务端计算，避免前端自行累计导致的红点与列表不一致

## 8. 前端消费

共享包新增 `packages/esports-shared/src/api/realtime.ts`：

```ts
ensureRealtime()            // 登录后调用，幂等；先 POST ticket 再 connectSocket
onRealtime(type, fn)        // 返回取消函数
onRealtimeReconnect(fn)     // 断线恢复后触发，用于补拉
realtimeUnread()            // reactive 未读数
closeRealtime()             // 登出时调用
```

三个必须做对的点：

1. **只使用 `SocketTask` 实例的 onMessage**，不使用全局 `uni.onSocketMessage`。全局事件在 uni-app 多页面下会被重复绑定，同一条消息处理 N 次
2. **重连 1s → 2s → 4s → 8s → 16s → 30s 封顶，±20% 抖动**；连续失败 10 次进入 60s 冷却
3. **`App.onShow` 时若 socket 已断则立即重连，不等退避**。移动端切后台会掐连接，回前台必须立刻恢复

### 8.1 页面落点

现有 API 已就绪（`esports.ts` 的 `fetchMessages` / `markMessagesRead`，`player.ts` 的陪玩版），缺的是页面：

- 客户端 / 陪玩端 / 客服端各新增一个约 80 行的消息列表页：倒序 + 未读高亮 + 「全部已读」
- **不做跨端共享 UI 组件**：三端展示字段与跳转目标不同，抽象后需要传一堆 props
- 消费规则：
  - 消息页在前台 → 收到 `message.created` 直接 `load()`
  - 其他页面 → 只更新 `realtimeUnread()` 红点
  - 弹条白名单（共享包常量）：`order.priority_notice` / `order.seat_invited` / `order.grab_attempt_lost`，其余静默
  - `hall.vue` 收到 `hall.order_created` → 300ms 防抖后 `load()`

现有 `player/pages/player/notices.vue` 是公告页（不读 messages 接口），不复用为消息中心。

### 8.2 断线不丢消息的闭环

重连成功 → 服务端下发 `ready`（含最新未读）→ 前端触发 `onRealtimeReconnect` → 活跃页 `load()`。这就是完整的补偿链路，不额外做离线队列。

## 9. 错误处理

| 场景 | 处理 |
|---|---|
| 某个连接写阻塞（客户端卡顿） | 有界 channel 满即断开该连接，不影响同房间其他连接；客户端重连后靠补拉恢复 |
| 单 address 连接过多 | 最多 5 条，超出返回 429 |
| ticket 过期 / 已使用 / 不存在 | 拒绝升级（401），客户端重新申请 ticket |
| WS 升级前鉴权失败 | 不升级，直接 401 |
| 服务重启 | 全部连接断开，客户端指数退避重连 + 补拉，无消息丢失 |
| 消息事务回滚 | 可能多推一条提示；前端重拉列表即可，红点数由服务端重算 |
| 多实例部署 | **当前不支持**。进程内 hub 无法跨实例；触发条件见 4.2 |
| 推送失败（连接已关闭） | 静默丢弃。消息已落库，实时性为尽力而为，不进入 outbox 重试 |

## 10. 测试

不引入测试框架，只留能失败的检查。

`cmf/ws`：

- 同一 address 的多条连接都收到广播
- ticket 只能消费一次；过期后消费失败
- 写 channel 满时连接被关闭，且同房间其他连接不受影响

`go-admin/internal/ws`：

- `Message.Create` 触发后，fake hub 收到 `message.created`，地址等于 `m.UserUID`
- ticket 汇总结果为 `[uid]` + provider 返回值

`esports`：

- provider 对客户返回 `customer:<id>`，对陪玩返回 `player:<id>`，双身份时两者都在

## 11. 实施顺序

1. `cmf/ws`：hub、ticket、handler、event + 单元检查
2. `go-admin/internal/ws` + 三条路由 + ent hook
3. `esports` 注册 provider + 大厅广播 + 移除 outbox 的推送职责
4. 共享包 `realtime.ts` + 三端消息页与红点 + `hall.vue` 实时刷新

`cmf` 联调方式：本地 clone → `go.mod` 加 `replace github.com/wuwuseo/cmf => ../cmf` → 完成后打 v1.5.0 → 删除 `replace` 并升版本。

## 12. 明确不做

- 多实例广播（触发条件见 4.2）
- WS 传输消息正文
- 离线消息队列（`ent.Message` 就是队列）
- 改造 `cmf/sse.TicketStore`
- 跨端共享消息 UI 组件
- 自动化 e2e 测试（前端手工验证）

## 13. 已知上限（ponytail 记录）

| 上限 | 位置 | 升级路径 |
|---|---|---|
| hub 单实例，不支持水平扩展 | `cmf/ws` `Hub` | 换 centrifugo 或加 Redis pub/sub，只替换 `Hub` 实现 |
| hook 事务内推送，回滚会多推一条 | `internal/ws/message_hook.go` | 事务提交后推送 |
| 每 address 最多 5 连接 | `cmf/ws` `handler.go` | 按配置调整 |
| 大厅事件全量广播，不做收件人过滤 | `esports` 广播点 | 需要时在广播前按可抢条件过滤 |
