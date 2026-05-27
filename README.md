# Beak Agent Lark Connector SDK

[中文文档](README.zh-CN.md)

Go SDK package for connecting Beak Channel Gateway to Lark/Feishu bot accounts.

This repository is importable library code. It is not a CLI, does not read user-authored runtime files, does not own persistence, and does not require users to edit server files. The Beak host owns UI, credential persistence, account state persistence, webhook routing, session creation, message writes, agent stream subscription, and runtime packaging. This SDK owns the Lark/Feishu connector logic: credential schema, event challenge handling, webhook signature/decryption helpers, text webhook normalization, message dedupe, token handling, and outbound delivery through Lark/Feishu Open APIs.

## Scope

- Generic `sdk.Connector` implementation exposed by `beaklark.NewConnector()`.
- Credential-based Lark/Feishu bot account setup.
- Host-backed credential and state persistence.
- Text-only inbound `im.message.receive_v1` webhook events to Beak sessions.
- Text Beak agent output back to Lark/Feishu through `im/v1/messages`.
- Raw outbound `msg_type`/`content` delivery for host-mapped Lark message types.
- Optional reply delivery through `im/v1/messages/{message_id}/reply`.
- Encrypted webhook body decryption and request signature verification when `encrypt_key` is configured.
- Direct chat and group chat normalization.
- One connected bot account plus one group chat maps to one Beak session; one connected bot account plus one direct chat maps to one Beak session.
- If multiple bot accounts are in the same group, each account creates or reuses its own Beak session for that group.
- One channel-link session is created per bot account connection, without creating a task.

Out of v1 scope: first-class media upload/download helpers, voice, typing status, first-class interactive card builders, WebSocket event client ownership, and Beak host code changes.

## Package Layout

- `sdk`: generic Beak Connector Plugin SDK interfaces and message types.
- package root: Lark/Feishu connector implementation.
- `internal/lark`: Lark/Feishu Open API HTTP client and webhook event models.
- `state`: account-scoped connector state helpers.
- `examples/basic`: minimal host-side import skeleton.

## Public Entrypoints

```go
import (
	beaklark "github.com/GuanceCloud/beak-agent-channel-lark"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
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

For raw `*http.Request` handling with signature verification, the connector also implements:

```go
type WebhookRequestConnector interface {
	WebhookConnector
	HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*beaklark.WebhookResult, error)
}
```

Beak host should type assert one of these interfaces when routing the Lark/Feishu event callback endpoint.

## Credential Schema

`connector.CredentialSchema(ctx)` asks Beak UI to collect:

- `app_id`: required, Lark/Feishu self-built application app id.
- `app_secret`: required, secret.
- `verification_token`: optional event subscription token.
- `encrypt_key`: optional event subscription encrypt key. The SDK uses it for encrypted webhook payload decryption and request signature verification.
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
- Plaintext and encrypted `im.message.receive_v1` text events.
- Verification token checks when `verification_token` exists.
- Signature verification through `HandleWebhookRequest` when Lark/Feishu signature headers are present.
- Self-echo filtering when `bot_open_id` exists.
- Dedupe by message id or event id.
- Session creation/reuse through `sdk.Gateway.EnsureChatSession`.
- Beak message creation through `sdk.Gateway.CreateMessage`.

If Beak host already terminates and verifies webhook requests itself, it can still call `HandleWebhook` with the raw body. If it wants the SDK to verify request headers, call `HandleWebhookRequest`.

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

The SDK obtains and caches a tenant access token in host-owned account state with:

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

Then sends text with:

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

For group chats, `receive_id_type=chat_id`. For direct chats, the SDK infers the receive id type from `ChatID`, usually `chat_id` for `oc_...` or `open_id` for `ou_...`.

For non-text payloads mapped by Beak host, set `Raw["msg_type"]` and `Raw["content"]`. For replies, set `Raw["reply_to_message_id"]`; optionally set `Raw["reply_in_thread"]`.

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

## State Rules

Beak host stores account state. The SDK only writes through `sdk.AccountStore.SaveChannelAccountState`:

- `channel_link_session`: connection session for this bot account.
- `peer_sessions`: chat identity to Beak session uuid cache.
- `inbound_seen`: inbound dedupe keys.
- `sent_beak_messages`: reserved outbound message dedupe state.
- `stream_cursors`: reserved Beak stream cursors.
- `tenant_access_token` / `tenant_access_token_expires_at`: tenant token cache for send APIs.
- `bot_open_id`: bot identity used for self-echo filtering.

## Verification

```sh
go test ./...
go build ./...
```
