# Beak Agent Lark Connector SDK

[中文文档](README.zh-CN.md)

Go SDK package for connecting Beak Channel Gateway to Lark/Feishu bot accounts.

This repository is importable library code. It is not a CLI, does not read user-authored runtime files, does not own persistence, and does not require users to edit server files. The Beak host owns UI, credential persistence, account state persistence, the Lark/Feishu WebSocket event client, event routing, session creation, message writes, agent stream subscription, and runtime packaging. This SDK owns the Lark/Feishu connector logic: credential schema, WebSocket event payload normalization, optional HTTP callback challenge/signature/decryption helpers, message dedupe, token handling, and outbound delivery through Lark/Feishu Open APIs.

## Scope

- Generic `sdk.Connector` implementation exposed by `beaklark.NewConnector()`.
- Credential-based Lark/Feishu bot account setup.
- Host-backed credential and state persistence.
- Text and `post` rich-text inbound `im.message.receive_v1` WebSocket events to Beak sessions.
- Text or markdown-rendered Beak agent output back to Lark/Feishu through `im/v1/messages`.
- Raw outbound `msg_type`/`content` delivery for host-mapped Lark message types.
- Optional reply delivery through `im/v1/messages/{message_id}/reply`.
- Optional encrypted HTTP callback body decryption and request signature verification when `encrypt_key` is configured.
- Direct chat and group chat normalization.
- Thread id propagation for Lark replies and topics; the SDK still keeps `peer_sessions` chat-scoped.
- Bot mention normalization where `@all` sets `mention_all` but not `mentioned_me`.
- Explicit `@bot` messages with empty text after mention filtering are still delivered to Beak for follow-up handling.
- One connected bot account plus one group chat maps to one Beak session; one connected bot account plus one direct chat maps to one Beak session.
- If multiple bot accounts are in the same group, each account creates or reuses its own Beak session for that group.
- One channel-link session is created per bot account connection, without creating a task.

Out of v1 scope: first-class media upload/download helpers, voice, typing status, first-class interactive card builders, WebSocket event client ownership, and Beak host code changes.

## Package Layout

- `sdk`: generic Beak Connector Plugin SDK interfaces and message types.
- package root: Lark/Feishu connector implementation.
- `internal/lark`: Lark/Feishu Open API HTTP client and event models.
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

The connector also implements `beaklark.EventConnector` for host-owned WebSocket event runtimes:

```go
type EventConnector interface {
	sdk.Connector
	HandleEvent(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.EventResult, error)
}
```

`HandleEvent` accepts the SDK-flattened event payload produced by the Lark WebSocket `EventDispatcher`.

For HTTP event callback compatibility, the connector also implements `beaklark.WebhookConnector`:

```go
type WebhookConnector interface {
	sdk.Connector
	HandleWebhook(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, body []byte) (*beaklark.WebhookResult, error)
}
```

For raw `*http.Request` handling with signature verification, the connector also implements:

```go
type WebhookRequestConnector interface {
	sdk.Connector
	HandleWebhookRequest(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req *http.Request) (*sdk.WebhookResponse, error)
}
```

Beak host should use `EventConnector` for the OpenClaw-aligned WebSocket path. Use `WebhookRequestConnector` only when the host exposes an HTTP callback endpoint; that path returns the platform HTTP response, not Beak internal message metadata.

## Credential Schema

`connector.CredentialSchema(ctx)` asks Beak UI to collect:

- `app_id`: required, Lark/Feishu self-built application app id.
- `app_secret`: required, secret.
- `verification_token`: optional event subscription token.
- `encrypt_key`: optional event subscription encrypt key. The SDK uses it for encrypted webhook payload decryption and request signature verification.
- `brand`: optional, `feishu` or `lark`; defaults to `feishu`.
- `bot_open_id`: optional bot open id used to drop self echo messages.

Beak host must encrypt credential JSON before persistence. The SDK does not write credential or state to local files.

`ValidateCredential(ctx, req)` exchanges the app credential for a tenant token and fetches bot information when possible. The result backfills normalized credential/state fields and standard `bot_identity` / `bot_identities` state entries so later event handling can perform self-echo and mention detection without Beak host parsing Lark payloads.

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

`Start(ctx, runtime)` validates account wiring, creates or reuses a channel-link session for each account, persists the account state, and then returns. Lark/Feishu inbound events are received by the Beak host's WebSocket event runtime and passed to `HandleEvent`; `Start` does not start a CLI, read config files, subscribe to Beak agent streams, own the WebSocket client, or own a local event server.

## Event Handling

OpenClaw's Lark implementation uses a per-account WebSocket client and registers `im.message.receive_v1` with the Lark `EventDispatcher`. The Beak host should mirror that ownership: keep the WebSocket connection in the host runtime, load the target `channel_account`, and pass the decoded event body to the SDK:

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

`HandleEvent` supports:

- SDK-flattened WebSocket `im.message.receive_v1` text and `post` events.
- Already-decoded events from the host WebSocket runtime. The host's Lark `EventDispatcher` owns transport verification for this path.
- Self-echo filtering when `bot_open_id` exists.
- Standard `mentions` extraction and `mentioned_me` detection with `bot_open_id`.
- `mention_all` is reported separately and does not imply `mentioned_me`.
- Empty text is ignored only when the event did not explicitly mention the current bot.
- `thread_id` / `parent_id` / `root_id` are propagated to Beak as thread context, while SDK peer-session cache keys remain `chat_type:chat_id`.
- Standard `chat_identity.id/type` is propagated from Lark `chat_id/chat_type`. When the host provides `Runtime.HTTPClient`, the SDK best-effort resolves `sender_display_name` through Lark contact user info and group `chat_display_name` through Lark chat info; lookup failures leave names empty and do not block inbound delivery.
- Dedupe by message id or event id.
- Session creation/reuse through `sdk.Gateway.EnsureChatSession`.
- Beak message creation through `sdk.Gateway.CreateMessage`.

For an HTTP callback endpoint, call `HandleWebhookRequest` so the SDK verifies signature headers, timestamp freshness, decrypts the body when needed, and checks `verification_token` when configured. `HandleWebhook` is only for host-owned paths that have already verified and decrypted the request. That path is compatibility support; the OpenClaw reference runtime is WebSocket-first.

## Sending Text and Markdown

Gateway can send agent output back through `connector.Send`:

```go
_, err := connector.Send(ctx, runtime, sdk.OutboundMessage{
	AccountUUID: accountUUID,
	ChatType:    sdk.ChatTypeGroup,
	ChatID:      "oc_xxx",
	Text:        "reply text",
	Format:      "markdown", // optional common field
	Title:       "Reply",    // optional common markdown title
	MessageUUID: messageUUID,
})
```

The SDK obtains and caches a tenant access token in host-owned account state with:

```text
POST /open-apis/auth/v3/tenant_access_token/internal
```

Then sends text or markdown-formatted `post` messages with:

```text
POST /open-apis/im/v1/messages?receive_id_type=<chat_id|open_id|union_id>
```

For group chats, `receive_id_type=chat_id`. For direct chats, the SDK infers the receive id type from `ChatID`, usually `chat_id` for `oc_...` or `open_id` for `ou_...`.

Set `OutboundMessage.Format="markdown"` to render agent output as a Feishu/Lark `post` message with an `md` element. `OutboundMessage.Title` is used as the post title; when omitted, the SDK does not derive a visible title from the message body.

For normal agent text or markdown, Beak host should pass the same `Text` / `Format` / `Title` fields it passes to the other SDKs and let this SDK map markdown to Lark `post`. `Raw["msg_type"]` and `Raw["content"]` are only for already platform-native Lark payloads. For replies, set `Raw["reply_to_message_id"]`; optionally set `Raw["reply_in_thread"]`.

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

Beak host stores account state. The SDK reads and writes through `sdk.AccountStore`:

- `channel_link_session`: connection session for this bot account.
- `peer_sessions`: chat identity to Beak session uuid cache.
- `inbound_seen`: inbound dedupe keys.
- `sent_beak_messages`: reserved outbound message dedupe state.
- `stream_cursors`: reserved Beak stream cursors.
- `tenant_access_token` / `tenant_access_token_expires_at`: tenant token cache for send APIs.
- `bot_open_id`: bot identity used for self-echo filtering.
- `bot_identity` / `bot_identities`: standard bot identity cache used by the unified SDK contract.

`peer_sessions` must stay chat-scoped. Thread context is available through inbound metadata and `EnsureChatSessionRequest.ThreadID`; do not treat a Lark thread as a different SDK peer-session key unless Beak product requirements explicitly move to thread-level sessions.

## Verification

```sh
go test ./...
go build ./...
```
