# Beak Agent Lark Connector SDK

[English](README.md)

这是一个 Go SDK 包，用于把 Beak Channel Gateway 接入飞书/Lark bot account。

本仓库提供的是可被 Beak host `import` 的库代码，不是命令行工具。SDK 不读取用户编写的运行时配置文件，不维护本地状态目录，不拥有数据库持久化，也不要求用户登录服务器修改文件。Beak host 负责客户端 UI、credential 持久化、account state 持久化、Lark/飞书 WebSocket event client、事件路由、session 创建、message 写入、agent stream 订阅和 connector runtime 打包。SDK 只负责飞书/Lark connector 逻辑：credential schema、WebSocket event payload 标准化、可选 HTTP callback challenge/签名/解密辅助、消息去重、token 处理，以及通过飞书/Lark Open API 发送消息。

## 范围

v1 支持：

- 通过 `beaklark.NewConnector()` 暴露通用 `sdk.Connector` 实现。
- 基于 credential 的飞书/Lark bot account 接入。
- 由 Beak host 保存 credential 和 connector state。
- 文本和 `post` 富文本 `im.message.receive_v1` WebSocket 事件入站到 Beak session。
- Beak agent 文本或 markdown 渲染输出通过 `im/v1/messages` 回发到飞书/Lark。
- 支持由 Beak host 映射的 raw outbound `msg_type` / `content`。
- 支持通过 `im/v1/messages/{message_id}/reply` 回复原消息。
- 配置 `encrypt_key` 后，兼容 HTTP callback 的加密 body 解密和请求签名校验。
- 单聊和群聊标准化。
- 透传飞书 thread/topic 上下文，但 SDK 的 `peer_sessions` 仍保持 chat 维度。
- mention 标准化中 `@all` 只设置 `mention_all`，不会自动设置 `mentioned_me`。
- 只有明确 @bot 但 mention 过滤后正文为空的消息，仍会进入 Beak，用于 follow-up。
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
- `internal/lark`：飞书/Lark Open API HTTP client 和事件模型。
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

该 connector 同时实现 `beaklark.EventConnector`，用于 host-owned WebSocket event runtime：

```go
type EventConnector interface {
	sdk.Connector
	HandleEvent(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.EventResult, error)
}
```

`HandleEvent` 接收 Lark WebSocket `EventDispatcher` 解码后的 SDK-flattened event payload。

为了兼容 HTTP event callback，connector 也实现 `beaklark.WebhookConnector`：

```go
type WebhookConnector interface {
	sdk.Connector
	HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.WebhookResult, error)
}
```

如果 Beak host 希望 SDK 直接处理 `*http.Request` 并校验签名，也可以 type assert：

```go
type WebhookRequestConnector interface {
	sdk.Connector
	HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*sdk.WebhookResponse, error)
}
```

OpenClaw 对齐的 WebSocket 主链路应 type assert `EventConnector`。只有 host 暴露 HTTP callback endpoint 时，才使用 `WebhookRequestConnector`；该路径返回平台 HTTP response，不返回 Beak 内部消息元数据。

## Credential Schema

`connector.CredentialSchema(ctx)` 要求 Beak UI 采集：

- `app_id`：必填，飞书/Lark 自建应用 app id。
- `app_secret`：必填，敏感字段。
- `verification_token`：可选，事件订阅 verification token。
- `encrypt_key`：可选事件订阅 encrypt key。SDK 用它解密加密 webhook payload，并校验请求签名。
- `brand`：可选，`feishu` 或 `lark`，默认 `feishu`。
- `bot_open_id`：可选，用于过滤 bot 自己发出的 self echo 消息。

Beak host 必须在入库前加密 credential JSON。SDK 不把 credential 或 state 写入本地文件。

`ValidateCredential(ctx, req)` 会用 app credential 换取 tenant token，并尽量拉取 bot 信息。返回结果会回填归一化 credential/state 字段和标准 `bot_identity` / `bot_identities` state，后续事件处理可以用它做 self-echo 和 mention 判断，不需要 Beak host 解析飞书 payload。

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

`Start(ctx, runtime)` 负责校验 account wiring，为每个 account 创建或复用 channel-link session，保存 account state，然后返回。飞书/Lark 入站事件由 Beak host 的 WebSocket event runtime 收到后调用 `HandleEvent`。`Start` 不启动 CLI，不读取配置文件，不订阅 Beak agent stream，不拥有 WebSocket client，也不拥有本地事件服务器。

## 事件处理

OpenClaw 的 Lark 实现是 per-account WebSocket client，并在 Lark `EventDispatcher` 上注册 `im.message.receive_v1`。Beak host 应复用这个边界：host 持有 WebSocket 连接，加载对应 `channel_account` 后，把解码后的 event body 传给 SDK：

```go
connector := beaklark.NewConnector()

eventConnector, ok := connector.(beaklark.EventConnector)
if !ok {
	return errors.New("lark connector does not handle events")
}

result, err := eventConnector.HandleEvent(ctx, runtime, account, eventBody)
if err != nil {
	return err
}
```

`HandleEvent` 支持：

- SDK-flattened WebSocket `im.message.receive_v1` 文本和 `post` 富文本事件。
- host WebSocket runtime 已解码的事件；该路径的传输校验由 host 的 Lark `EventDispatcher` 负责。
- 配置了 `bot_open_id` 时过滤 self echo。
- 标准化提取 `mentions`，并用 `bot_open_id` 判断 `mentioned_me`。
- `mention_all` 单独上报，不等价于 `mentioned_me`。
- 只有事件没有明确提及当前 bot 时，空正文才会被忽略。
- `thread_id` / `parent_id` / `root_id` 会作为 thread 上下文透传给 Beak，但 SDK 的 peer-session 缓存 key 仍是 `chat_type:chat_id`。
- 标准 `chat_identity.id/type` 来自飞书 `chat_id/chat_type`。当 host 提供 `Runtime.HTTPClient` 时，SDK 会尽力通过飞书用户信息接口补全 `sender_display_name`，并通过飞书 chat 信息接口补全群聊 `chat_display_name`；查询失败时名称保持为空，不影响入站消息投递。
- 按 message id 或 event id 去重。
- 通过 `sdk.Gateway.EnsureChatSession` 创建或复用 session。
- 通过 `sdk.Gateway.CreateMessage` 写入 Beak message。

如果 host 使用 HTTP callback endpoint，必须调用 `HandleWebhookRequest`，由 SDK 校验签名 header、timestamp 新鲜度，必要时解密 body，并在配置 `verification_token` 时校验 token。`HandleWebhook` 只用于 host 已经自行验签和解密的内部路径。这只是兼容入口，OpenClaw 参考实现的主链路是 WebSocket-first。

## 发送文本和 Markdown

Gateway 可以通过 `connector.Send` 把 agent 输出发回飞书/Lark：

```go
_, err := connector.Send(ctx, runtime, sdk.OutboundMessage{
	AccountUUID: accountUUID,
	ChatType:    sdk.ChatTypeGroup,
	ChatID:      "oc_xxx",
	Text:        "reply text",
	Format:      "markdown", // 可选通用字段
	Title:       "Reply",    // 可选通用 markdown 标题
	MessageUUID: messageUUID,
})
```

SDK 会先获取并把 tenant access token 缓存在 host-owned account state 中：

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

再发送文本或 markdown 渲染的 `post` 消息：

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

群聊使用 `receive_id_type=chat_id`。单聊会根据 `ChatID` 推断 receive id type，通常 `oc_...` 使用 `chat_id`，`ou_...` 使用 `open_id`。

设置 `OutboundMessage.Format="markdown"` 时，SDK 会把 agent 输出渲染成飞书/Lark `post` 消息，并使用 `md` element。`OutboundMessage.Title` 会作为 post title；未设置时 SDK 会从第一行非空 markdown 文本推导短标题。

普通 agent 文本或 markdown 出站时，Beak host 应该和其他 SDK 一样只传 `Text` / `Format` / `Title`，由飞书 SDK 把 markdown 映射成 Lark `post`。`Raw["msg_type"]` 和 `Raw["content"]` 只用于 host 已经构造好的平台原生 Lark payload。如果需要回复原消息，设置 `Raw["reply_to_message_id"]`，必要时设置 `Raw["reply_in_thread"]`。

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

Beak host 保存 account state，SDK 通过 `sdk.AccountStore` 读取并回写：

- `channel_link_session`：该 bot account 对应的连接 session。
- `peer_sessions`：chat identity 到 Beak session uuid 的缓存。
- `inbound_seen`：入站消息 dedupe key。
- `sent_beak_messages`：预留给出站 message dedupe。
- `stream_cursors`：预留给 Beak stream cursor。
- `tenant_access_token` / `tenant_access_token_expires_at`：发送 API 使用的 tenant token 缓存。
- `bot_open_id`：用于过滤 self echo 的 bot identity。
- `bot_identity` / `bot_identities`：统一 SDK 契约里的标准 bot 身份缓存。

`peer_sessions` 必须保持 chat 维度。thread 上下文可以通过 inbound metadata 和 `EnsureChatSessionRequest.ThreadID` 获取；除非产品明确要求 thread 级 session，否则不要把飞书 thread 当成不同 SDK peer-session key。

## 验证

```sh
go test ./...
go build ./...
```
