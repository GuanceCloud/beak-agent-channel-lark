# Beak Agent Lark Connector SDK

[English](README.md)

这是一个 Go SDK 包，用于把 Beak Channel Gateway 接入飞书/Lark bot account。

本仓库提供的是可被 Beak host `import` 的库代码，不是命令行工具。SDK 不读取用户编写的运行时配置文件，不维护本地状态目录，不拥有数据库持久化，也不要求用户登录服务器修改文件。Beak host 负责客户端 UI、credential 持久化、account state 持久化、webhook 路由、session 创建、message 写入、agent stream 订阅和 connector runtime 打包。SDK 只负责飞书/Lark connector 逻辑：credential schema、事件挑战处理、webhook 签名/解密辅助、文本 webhook 标准化、消息去重、token 处理，以及通过飞书/Lark Open API 发送消息。

## 范围

v1 支持：

- 通过 `beaklark.NewConnector()` 暴露通用 `sdk.Connector` 实现。
- 基于 credential 的飞书/Lark bot account 接入。
- 由 Beak host 保存 credential 和 connector state。
- 文本 `im.message.receive_v1` webhook 事件入站到 Beak session。
- Beak agent 文本输出通过 `im/v1/messages` 回发到飞书/Lark。
- 支持由 Beak host 映射的 raw outbound `msg_type` / `content`。
- 支持通过 `im/v1/messages/{message_id}/reply` 回复原消息。
- 配置 `encrypt_key` 后，支持加密 webhook body 解密和请求签名校验。
- 单聊和群聊标准化。
- 一个已连接 bot account 中的一个群聊对应一个 Beak session。
- 一个已连接 bot account 中的一个单聊对应一个 Beak session。
- 如果同一个群里接入多个 bot account，每个 bot account 都创建或复用自己的 Beak session。
- 每个 bot account 连接创建一个 channel-link session，但不创建 task。

v1 不支持：

- media、voice、typing status。
- 一等 interactive card builder。
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
	beaklark "github.com/GuanceCloud/beak-agent-channel-lark"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
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

如果 Beak host 希望 SDK 直接处理 `*http.Request` 并校验签名，也可以 type assert：

```go
type WebhookRequestConnector interface {
	WebhookConnector
	HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*beaklark.WebhookResult, error)
}
```

Beak host 在路由飞书/Lark 事件回调 endpoint 时，可以按接入方式 type assert 对应接口。

## Credential Schema

`connector.CredentialSchema(ctx)` 要求 Beak UI 采集：

- `app_id`：必填，飞书/Lark 自建应用 app id。
- `app_secret`：必填，敏感字段。
- `verification_token`：可选，事件订阅 verification token。
- `encrypt_key`：可选事件订阅 encrypt key。SDK 用它解密加密 webhook payload，并校验请求签名。
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
- 明文和加密 `im.message.receive_v1` 文本事件。
- 配置了 `verification_token` 时进行 token 校验。
- 通过 `HandleWebhookRequest` 校验飞书/Lark 请求签名。
- 配置了 `bot_open_id` 时过滤 self echo。
- 按 message id 或 event id 去重。
- 通过 `sdk.Gateway.EnsureChatSession` 创建或复用 session。
- 通过 `sdk.Gateway.CreateMessage` 写入 Beak message。

如果 Beak host 已经自行完成请求验签和解密，可以继续调用 `HandleWebhook`。如果希望由 SDK 校验请求 header，则调用 `HandleWebhookRequest`。

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

SDK 会先获取并把 tenant access token 缓存在 host-owned account state 中：

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

再发送文本：

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

群聊使用 `receive_id_type=chat_id`。单聊会根据 `ChatID` 推断 receive id type，通常 `oc_...` 使用 `chat_id`，`ou_...` 使用 `open_id`。

如果 Beak host 已经把 agent 输出映射成飞书消息格式，可以设置 `Raw["msg_type"]` 和 `Raw["content"]`。如果需要回复原消息，设置 `Raw["reply_to_message_id"]`，必要时设置 `Raw["reply_in_thread"]`。

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

## State 规则

Beak host 保存 account state，SDK 只通过 `sdk.AccountStore.SaveChannelAccountState` 回写：

- `channel_link_session`：该 bot account 对应的连接 session。
- `peer_sessions`：chat identity 到 Beak session uuid 的缓存。
- `inbound_seen`：入站消息 dedupe key。
- `sent_beak_messages`：预留给出站 message dedupe。
- `stream_cursors`：预留给 Beak stream cursor。
- `tenant_access_token` / `tenant_access_token_expires_at`：发送 API 使用的 tenant token 缓存。
- `bot_open_id`：用于过滤 self echo 的 bot identity。

## 验证

```sh
go test ./...
go build ./...
```
