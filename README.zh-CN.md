# Beak Agent Lark Connector SDK

[English](README.md)

这是一个 Go SDK 包，用于把 Beak Channel Gateway 接入飞书/Lark bot account。

本仓库提供的是可被 Beak host `import` 的库代码，不是命令行工具。SDK 不读取用户编写的运行时配置文件，不维护本地状态目录，不拥有数据库持久化，也不要求用户登录服务器修改文件。Beak host 负责客户端 UI、credential 持久化、account state 持久化、webhook 路由、session 创建、message 写入、agent stream 订阅和 connector runtime 打包。SDK 只负责飞书/Lark connector 逻辑：credential schema、事件挑战处理、文本 webhook 标准化、消息去重，以及通过飞书/Lark Open API 发送文本消息。

## 范围

v1 支持：

- 通过 `beaklark.NewConnector()` 暴露通用 `sdk.Connector` 实现。
- 基于 credential 的飞书/Lark bot account 接入。
- 由 Beak host 保存 credential 和 connector state。
- 文本 `im.message.receive_v1` webhook 事件入站到 Beak session。
- Beak agent 文本输出通过 `im/v1/messages` 回发到飞书/Lark。
- 单聊和群聊标准化。
- 一个已连接 bot account 中的一个群聊对应一个 Beak session。
- 一个已连接 bot account 中的一个单聊对应一个 Beak session。
- 如果同一个群里接入多个 bot account，每个 bot account 都创建或复用自己的 Beak session。
- 每个 bot account 连接创建一个 channel-link session，但不创建 task。

v1 不支持：

- media、voice、typing status。
- interactive cards。
- 在 SDK 内解密加密事件 payload。
- SDK 自己维护 WebSocket 事件客户端。
- 修改 Beak host 代码。
- 把飞书 connector 做成 CLI。
- 让 SDK 维护本地配置文件或本地状态目录。

## 包结构

- `sdk`：通用 Beak Connector Plugin SDK 接口和消息类型。
- 根包：飞书/Lark connector 实现。
- `internal/lark`：飞书/Lark Open API HTTP client 和 webhook 事件模型。
- `state`：account 维度的 connector state helper。
- `examples/basic`：最小 host-side import skeleton。

## 公开入口

```go
import (
	beaklark "beak-agent-lark"
	"beak-agent-lark/sdk"
)

func LarkConnector() sdk.Connector {
	return beaklark.NewConnector()
}
```

该 connector 同时实现 `beaklark.WebhookConnector`：

```go
type WebhookConnector interface {
	sdk.Connector
	HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.WebhookResult, error)
}
```

Beak host 在路由飞书/Lark 事件回调 endpoint 时，可以 type assert 这个接口。

## Credential Schema

`connector.CredentialSchema(ctx)` 要求 Beak UI 采集：

- `app_id`：必填，飞书/Lark 自建应用 app id。
- `app_secret`：必填，敏感字段。
- `verification_token`：可选，事件订阅 verification token。
- `encrypt_key`：预留字段；v1 要求明文事件，或由 Beak host 先解密再调用 SDK。
- `brand`：可选，`feishu` 或 `lark`，默认 `feishu`。
- `base_url`：可选，Open API base URL override。
- `bot_open_id`：可选，用于过滤 bot 自己发出的 self echo 消息。

Beak host 必须在入库前加密 credential JSON。SDK 不把 credential 或 state 写入本地文件。

## Runtime 边界

Beak host 注入 `sdk.Runtime`：

```go
runtime := sdk.Runtime{
	WorkspaceUUID: workspaceUUID,
	Channel: sdk.Channel{
		UUID:          channelUUID,
		WorkspaceUUID: workspaceUUID,
		Platform:      "lark",
	},
	Accounts:     accounts,
	Gateway:      gateway,
	AccountStore: accountStore,
}
```

`Start(ctx, runtime)` 负责校验 account wiring，并为每个 account 创建或复用 channel-link session。飞书/Lark 入站事件由 Beak host 的 webhook endpoint 收到后调用 `HandleWebhook`。`Start` 不启动 CLI，不读取配置文件，也不拥有本地事件服务器。

## Webhook 处理

Beak host 应暴露一个 HTTPS callback endpoint 给飞书/Lark 事件订阅使用，加载对应 `channel_account` 后，把原始 request body 传给 SDK：

```go
connector := beaklark.NewConnector()
result, err := connector.HandleWebhook(ctx, runtime, account, requestBody)
if err != nil {
	return err
}
if result.Challenge != "" {
	// 向飞书/Lark 返回 {"challenge": result.Challenge}
}
```

`HandleWebhook` 支持：

- URL verification challenge。
- 明文 `im.message.receive_v1` 文本事件。
- 配置了 `verification_token` 时进行 token 校验。
- 配置了 `bot_open_id` 时过滤 self echo。
- 按 message id 或 event id 去重。
- 通过 `sdk.Gateway.EnsureChatSession` 创建或复用 session。
- 通过 `sdk.Gateway.CreateMessage` 写入 Beak message。

v1 不在 SDK 内解密加密事件 payload。如果应用开启了事件加密，Beak host 必须先解密，再调用 `HandleWebhook`；或者该 connector account 暂时关闭事件加密。

## 发送文本

Gateway 可以通过 `connector.Send` 把 agent 输出发回飞书/Lark：

```go
_, err := connector.Send(ctx, runtime, sdk.OutboundMessage{
	AccountUUID: accountUUID,
	ChatType:    sdk.ChatTypeGroup,
	ChatID:      "oc_xxx",
	Text:        "reply text",
	MessageUUID: messageUUID,
})
```

SDK 会先获取 tenant access token：

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

再发送文本：

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

群聊使用 `receive_id_type=chat_id`。单聊会根据 `ChatID` 推断 receive id type，通常 `oc_...` 使用 `chat_id`，`ou_...` 使用 `open_id`。

## Session 规则

Gateway session identity 必须包含已连接 bot account 和 IM 平台 chat identity。

标准 session key：

```text
workspace_uuid + platform + account_uuid + chat_type + chat_id
```

推荐 Beak session 字段：

```text
platform=lark
session_type=manual
source_type=im_chat
source_id=lark:<account_uuid>:<chat_type>:<chat_id>
```

单聊：

```text
chat_type=direct
chat_id=<lark_chat_id_or_sender_open_id>
source_id=lark:<account_uuid>:direct:<chat_id>
```

群聊：

```text
chat_type=group
chat_id=<lark_chat_id>
source_id=lark:<account_uuid>:group:<chat_id>
```

同一个群里有多个 bot account 时，必须是多个 session：

```text
source_id=lark:account_a:group:oc_group
source_id=lark:account_b:group:oc_group
```

## 验证

```sh
go test ./...
go build ./...
```
