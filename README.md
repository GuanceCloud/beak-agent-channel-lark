# Beak Agent Lark Connector SDK

[中文文档](README.zh-CN.md)

Go SDK package for connecting Beak Channel Gateway to Lark/Feishu bot accounts.

This repository is importable library code. It is not a CLI, does not read user-authored runtime files, does not own persistence, and does not require users to edit server files. The Beak host owns UI, credential persistence, account state persistence, webhook routing, session creation, message writes, agent stream subscription, and runtime packaging. This SDK owns the Lark/Feishu connector logic: credential schema, event challenge handling, text webhook normalization, message dedupe, and text sending through Lark/Feishu Open APIs.

## Scope

- Generic `sdk.Connector` implementation exposed by `beaklark.NewConnector()`.
- Credential-based Lark/Feishu bot account setup.
- Host-backed credential and state persistence.
- Text-only inbound `im.message.receive_v1` webhook events to Beak sessions.
- Text-only Beak agent output back to Lark/Feishu through `im/v1/messages`.
- Direct chat and group chat normalization.
- One connected bot account plus one group chat maps to one Beak session; one connected bot account plus one direct chat maps to one Beak session.
- If multiple bot accounts are in the same group, each account creates or reuses its own Beak session for that group.
- One channel-link session is created per bot account connection, without creating a task.

Out of v1 scope: media, voice, typing status, interactive cards, encrypted event payload decryption, WebSocket event client ownership, and Beak host code changes.

## Package Layout

- `sdk`: generic Beak Connector Plugin SDK interfaces and message types.
- package root: Lark/Feishu connector implementation.
- `internal/lark`: Lark/Feishu Open API HTTP client and webhook event models.
- `state`: account-scoped connector state helpers.
- `examples/basic`: minimal host-side import skeleton.

## Public Entrypoints

```go
import (
	beaklark "beak-agent-lark"
	"beak-agent-lark/sdk"
)

func LarkConnector() sdk.Connector {
	return beaklark.NewConnector()
}
```

The connector also implements `beaklark.WebhookConnector`:

```go
type WebhookConnector interface {
	sdk.Connector
	HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.WebhookResult, error)
}
```

Beak host should type assert this interface when routing the Lark/Feishu event callback endpoint.

## Credential Schema

`connector.CredentialSchema(ctx)` asks Beak UI to collect:

- `app_id`: required, Lark/Feishu self-built application app id.
- `app_secret`: required, secret.
- `verification_token`: optional event subscription token.
- `encrypt_key`: reserved; v1 expects plaintext events or Beak host-side decryption before calling SDK.
- `brand`: optional, `feishu` or `lark`; defaults to `feishu`.
- `base_url`: optional Open API base URL override.
- `bot_open_id`: optional bot open id used to drop self echo messages.

Beak host must encrypt credential JSON before persistence. The SDK does not write credential or state to local files.

## Runtime Boundary

Beak host injects a `sdk.Runtime`:

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

`Start(ctx, runtime)` validates account wiring and creates or reuses a channel-link session for each account. Lark/Feishu inbound events are delivered by Beak host's webhook endpoint through `HandleWebhook`; `Start` does not start a CLI, read config files, or own a local event server.

## Webhook Handling

Beak host should expose an HTTPS callback endpoint for Lark/Feishu event subscription, load the target `channel_account`, and pass the raw request body to the SDK:

```go
connector := beaklark.NewConnector()
result, err := connector.HandleWebhook(ctx, runtime, account, requestBody)
if err != nil {
	return err
}
if result.Challenge != "" {
	// Return {"challenge": result.Challenge} to Lark/Feishu.
}
```

`HandleWebhook` supports:

- URL verification challenge.
- Plaintext `im.message.receive_v1` text events.
- Verification token checks when `verification_token` exists.
- Self-echo filtering when `bot_open_id` exists.
- Dedupe by message id or event id.
- Session creation/reuse through `sdk.Gateway.EnsureChatSession`.
- Beak message creation through `sdk.Gateway.CreateMessage`.

Encrypted event payloads are intentionally not decrypted in v1. If the application enables encrypted events, Beak host must decrypt the event before calling `HandleWebhook`, or keep event encryption disabled for this connector account.

## Sending Text

Gateway can send agent output back through `connector.Send`:

```go
_, err := connector.Send(ctx, runtime, sdk.OutboundMessage{
	AccountUUID: accountUUID,
	ChatType:    sdk.ChatTypeGroup,
	ChatID:      "oc_xxx",
	Text:        "reply text",
	MessageUUID: messageUUID,
})
```

The SDK obtains a tenant access token with:

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

Then sends text with:

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

For group chats, `receive_id_type=chat_id`. For direct chats, the SDK infers the receive id type from `ChatID`, usually `chat_id` for `oc_...` or `open_id` for `ou_...`.

## Session Rules

Gateway session identity is the connected bot account plus platform chat identity.

Canonical session key:

```text
workspace_uuid + platform + account_uuid + chat_type + chat_id
```

Recommended Beak session fields:

```text
platform=lark
session_type=manual
source_type=im_chat
source_id=lark:<account_uuid>:<chat_type>:<chat_id>
```

Direct chat:

```text
chat_type=direct
chat_id=<lark_chat_id_or_sender_open_id>
source_id=lark:<account_uuid>:direct:<chat_id>
```

Group chat:

```text
chat_type=group
chat_id=<lark_chat_id>
source_id=lark:<account_uuid>:group:<chat_id>
```

Same group, different bot accounts:

```text
source_id=lark:account_a:group:oc_group
source_id=lark:account_b:group:oc_group
```

## Verification

```sh
go test ./...
go build ./...
```
