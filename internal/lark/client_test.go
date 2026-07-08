package lark

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientTenantTokenAndSendText(t *testing.T) {
	var sawToken bool
	var sawSend bool
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			sawToken = true
			if r.Method != http.MethodPost {
				t.Fatalf("token method=%s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_1" || body["app_secret"] != "secret_1" {
				t.Fatalf("token body=%+v", body)
			}
			return jsonResponse(map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/im/v1/messages":
			sawSend = true
			if r.Method != http.MethodPost {
				t.Fatalf("send method=%s", r.Method)
			}
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Fatalf("receive_id_type=%q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("auth=%q", got)
			}
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
				UUID      string `json:"uuid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ReceiveID != "oc_chat" || body.MsgType != "text" || body.UUID != "msg-uuid" {
				t.Fatalf("send body=%+v", body)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatal(err)
			}
			if content["text"] != "hello" {
				t.Fatalf("content=%+v", content)
			}
			return jsonResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"message_id": "om_1",
					"chat_id":    "oc_chat",
				},
			})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}

	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	resp, err := client.SendText(context.Background(), SendTextRequest{
		ReceiveID:     "oc_chat",
		ReceiveIDType: "chat_id",
		Text:          "hello",
		UUID:          "msg-uuid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken || !sawSend {
		t.Fatalf("sawToken=%v sawSend=%v", sawToken, sawSend)
	}
	if resp.Data.MessageID != "om_1" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestClientTenantTokenWithExpiryCachesToken(t *testing.T) {
	var tokenCalls int
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		tokenCalls++
		return jsonResponse(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "tenant-token", "expire": 7200})
	})}
	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	now := time.Now().UTC()
	token, expiresAt, err := client.TenantAccessTokenWithExpiry(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if token != "tenant-token" || !expiresAt.After(now) {
		t.Fatalf("token=%q expiresAt=%s", token, expiresAt)
	}
	token, _, err = client.TenantAccessTokenWithExpiry(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if token != "tenant-token" || tokenCalls != 1 {
		t.Fatalf("token=%q tokenCalls=%d", token, tokenCalls)
	}
}

func TestClientBotInfoUsesTenantToken(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/open-apis/bot/v3/info" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("bot info method=%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
			t.Fatalf("auth=%q", got)
		}
		return jsonResponse(map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"open_id": "ou_bot_live", "app_name": "Beak Bot"}})
	})}
	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	client.TenantToken = "cached-token"
	info, err := client.BotInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Bot.OpenID != "ou_bot_live" {
		t.Fatalf("info=%+v", info)
	}
}

func TestClientUserInfoAndChatInfo(t *testing.T) {
	var sawUserInfo bool
	var sawChatInfo bool
	var sawChatMembers bool
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/open-apis/contact/v3/users/ou_user":
			sawUserInfo = true
			if r.Method != http.MethodGet {
				t.Fatalf("user method=%s", r.Method)
			}
			if got := r.URL.Query().Get("user_id_type"); got != "open_id" {
				t.Fatalf("user_id_type=%q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
				t.Fatalf("auth=%q", got)
			}
			return jsonResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"user": map[string]any{
						"open_id": "ou_user",
						"name":    "Alice",
					},
				},
			})
		case "/open-apis/im/v1/chats/oc_group":
			sawChatInfo = true
			if r.Method != http.MethodGet {
				t.Fatalf("chat method=%s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
				t.Fatalf("auth=%q", got)
			}
			return jsonResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"chat_id":    "oc_group",
					"name":       "Team",
					"avatar_url": "https://example.test/team.png",
				},
			})
		case "/open-apis/im/v1/chats/oc_group/members":
			sawChatMembers = true
			if r.Method != http.MethodGet {
				t.Fatalf("members method=%s", r.Method)
			}
			if got := r.URL.Query().Get("member_id_type"); got != "open_id" {
				t.Fatalf("member_id_type=%q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "50" {
				t.Fatalf("page_size=%q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
				t.Fatalf("auth=%q", got)
			}
			return jsonResponse(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"items": []map[string]any{
						{"member_id": "ou_user", "member_id_type": "open_id", "name": "Alice Member"},
					},
				},
			})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}
	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	client.TenantToken = "cached-token"
	userInfo, err := client.UserInfo(context.Background(), "ou_user")
	if err != nil {
		t.Fatal(err)
	}
	chatInfo, err := client.ChatInfo(context.Background(), "oc_group")
	if err != nil {
		t.Fatal(err)
	}
	members, err := client.ChatMembers(context.Background(), "oc_group")
	if err != nil {
		t.Fatal(err)
	}
	if !sawUserInfo || !sawChatInfo || !sawChatMembers {
		t.Fatalf("sawUserInfo=%v sawChatInfo=%v sawChatMembers=%v", sawUserInfo, sawChatInfo, sawChatMembers)
	}
	if userInfo.DisplayName() != "Alice" {
		t.Fatalf("userInfo=%+v", userInfo)
	}
	if chatInfo.DisplayName() != "Team" || chatInfo.AvatarURL() != "https://example.test/team.png" {
		t.Fatalf("chatInfo=%+v", chatInfo)
	}
	if members.DisplayNameForOpenID("ou_user") != "Alice Member" {
		t.Fatalf("members=%+v", members)
	}
}

func TestClientSendGenericAndReplyMessage(t *testing.T) {
	var sawSend bool
	var sawReply bool
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
			t.Fatalf("auth=%q", got)
		}
		switch r.URL.Path {
		case "/open-apis/im/v1/messages":
			sawSend = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["msg_type"] != "post" || body["content"] != `{"zh_cn":{"title":"hi"}}` {
				t.Fatalf("send body=%+v", body)
			}
			return jsonResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_post", "chat_id": "oc_chat"}})
		case "/open-apis/im/v1/messages/om_parent/reply":
			sawReply = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["msg_type"] != "text" || body["uuid"] != "uuid-1" {
				t.Fatalf("reply body=%+v", body)
			}
			return jsonResponse(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_reply", "chat_id": "oc_chat"}})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		return nil, nil
	})}
	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	client.TenantToken = "cached-token"
	_, err := client.SendMessage(context.Background(), SendMessageRequest{
		ReceiveID:     "oc_chat",
		ReceiveIDType: "chat_id",
		MsgType:       "post",
		Content:       `{"zh_cn":{"title":"hi"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ReplyMessage(context.Background(), ReplyMessageRequest{
		MessageID: "om_parent",
		MsgType:   "text",
		Content:   `{"text":"reply"}`,
		UUID:      "uuid-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSend || !sawReply {
		t.Fatalf("sawSend=%v sawReply=%v", sawSend, sawReply)
	}
}

func TestClientAddMessageReaction(t *testing.T) {
	var sawReaction bool
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/open-apis/im/v1/messages/om_parent/reactions" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		sawReaction = true
		if r.Method != http.MethodPost {
			t.Fatalf("reaction method=%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
			t.Fatalf("auth=%q", got)
		}
		var body struct {
			ReactionType struct {
				EmojiType string `json:"emoji_type"`
			} `json:"reaction_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ReactionType.EmojiType != "THINKING" {
			t.Fatalf("reaction body=%+v", body)
		}
		return jsonResponse(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"reaction_id": "reaction-1",
				"reaction_type": map[string]any{
					"emoji_type": "THINKING",
				},
			},
		})
	})}
	client := NewClient("https://open.feishu.test", "cli_1", "secret_1")
	client.HTTPClient = httpClient
	client.TenantToken = "cached-token"
	resp, err := client.AddMessageReaction(context.Background(), AddMessageReactionRequest{
		MessageID: "om_parent",
		EmojiType: "THINKING",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawReaction || resp.Data.ReactionID != "reaction-1" || resp.Data.ReactionType.EmojiType != "THINKING" {
		t.Fatalf("sawReaction=%v resp=%+v", sawReaction, resp)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body any) (*http.Response, error) {
	var builder strings.Builder
	if err := json.NewEncoder(&builder).Encode(body); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(builder.String())),
	}, nil
}

func TestParseWebhookText(t *testing.T) {
	body := []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt_1","event_type":"im.message.receive_v1","app_id":"cli_1","token":"verify-token"},
		"event":{
			"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user"},
			"message":{"message_id":"om_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"<at user_id=\\\"ou_bot\\\">Bot</at> hello\"}","create_time":"1770000000000"}
		}
	}`)
	hook, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if hook.EventType() != EventTypeMessageReceive || hook.EventID() != "evt_1" {
		t.Fatalf("hook=%+v", hook)
	}
	if !hook.VerifyToken("verify-token") {
		t.Fatal("token should verify")
	}
	if got := hook.Event.Message.Text(); got != "Bot hello" {
		t.Fatalf("text=%q", got)
	}
	chat := hook.Event.Message.ChatIdentity(hook.Event.Sender.SenderID.OpenID)
	if chat.ChatType != ChatTypeGroup || chat.ChatID != "oc_group" || chat.SenderID != "ou_user" || chat.StateKey() != "group:oc_group" {
		t.Fatalf("chat=%+v", chat)
	}
}

func TestEventMessageTextResolvesMentionPlaceholders(t *testing.T) {
	msg := EventMessage{
		MessageType: "text",
		Content:     `{"text":"@_user_1 ping @_bot @_all"}`,
		Mentions: []Mention{
			{Key: "@_user_1", ID: SenderID{OpenID: "ou_user"}, Name: "Alice"},
			{Key: "@_bot", ID: SenderID{OpenID: "ou_bot"}, Name: "Bot"},
			{Key: "@_all", Name: "所有人"},
		},
	}
	got := msg.TextWithMentionFilter(func(mention Mention) bool {
		return mention.ID.OpenID == "ou_bot"
	})
	if got != "@Alice ping @all" {
		t.Fatalf("text=%q", got)
	}
}

func TestEventMessagePostText(t *testing.T) {
	msg := EventMessage{
		MessageType: "post",
		Content: `{
			"zh_cn":{
				"title":"日报",
				"content":[
					[
						{"tag":"text","text":"处理","style":["bold"]},
						{"tag":"a","text":"链接","href":"https://example.test"}
					],
					[
						{"tag":"at","user_id":"ou_bot","user_name":"Bot"},
						{"tag":"text","text":" 和 "},
						{"tag":"at","user_id":"ou_user","user_name":"Alice"}
					],
					[
						{"tag":"code_block","language":"go","text":"fmt.Println(1)"}
					]
				]
			}
		}`,
		Mentions: []Mention{
			{Key: "@_bot", ID: SenderID{OpenID: "ou_bot"}, Name: "Bot"},
			{Key: "@_user", ID: SenderID{OpenID: "ou_user"}, Name: "Alice"},
		},
	}
	got := msg.TextWithMentionFilter(func(mention Mention) bool {
		return mention.ID.OpenID == "ou_bot"
	})
	want := "**日报**\n\n**处理**[链接](https://example.test)\n和 @Alice\n```go\nfmt.Println(1)\n```"
	if got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestEventMessagePostTextUnwrapsPostLocaleEnvelope(t *testing.T) {
	msg := EventMessage{
		MessageType: "post",
		Content: `{
			"post":{
				"zh_cn":{
					"title":"公告",
					"content":[
						[{"tag":"text","text":"第一行"}],
						[{"tag":"text","text":"第二行"}]
					]
				}
			}
		}`,
	}
	got := msg.Text()
	want := "**公告**\n\n第一行\n第二行"
	if got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestEventMessagePostTextUnwrapsContentLocaleEnvelope(t *testing.T) {
	msg := EventMessage{
		MessageType: "post",
		Content: `{
			"content":{
				"zh_cn":{
					"content":[
						[{"tag":"text","text":"包装正文"}]
					]
				}
			}
		}`,
	}
	got := msg.Text()
	want := "包装正文"
	if got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestMessageResponseAcceptsStringMentionIDAndPostText(t *testing.T) {
	content := `{"zh_cn":{"content":[[{"tag":"text","text":"hello "},{"tag":"at","user_id":"ou_user","user_name":"Alice"}]]}}`
	payload, err := json.Marshal(map[string]any{
		"code": 0,
		"msg":  "ok",
		"data": map[string]any{
			"items": []map[string]any{{
				"message_id": "om_parent",
				"chat_id":    "oc_group",
				"chat_type":  "group",
				"msg_type":   "post",
				"content":    content,
				"mentions": []map[string]any{{
					"key":  "@_user",
					"id":   "ou_user",
					"name": "Alice",
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp MessageResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatal(err)
	}
	item := resp.FirstMessage()
	if item == nil {
		t.Fatal("missing first message")
	}
	if got := item.EventMessage().Text(); got != "hello @Alice" {
		t.Fatalf("text=%q", got)
	}
}

func TestMessageResponsePostTextUnwrapsNestedContentString(t *testing.T) {
	nestedContent := `{"zh_cn":{"content":[[{"tag":"md","text":"**分析结果**\n\n1. 服务异常\n2. Trace ID 缺失"}]]}}`
	payload, err := json.Marshal(map[string]any{
		"code": 0,
		"msg":  "ok",
		"data": map[string]any{
			"items": []map[string]any{{
				"message_id": "om_parent",
				"chat_id":    "oc_group",
				"chat_type":  "group",
				"msg_type":   "post",
				"content":    map[string]any{"content": nestedContent},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp MessageResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatal(err)
	}
	item := resp.FirstMessage()
	if item == nil {
		t.Fatal("missing first message")
	}
	want := "**分析结果**\n\n1. 服务异常\n2. Trace ID 缺失"
	if got := item.EventMessage().Text(); got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestEventMessagePostTextRendersContentArrayEnvelope(t *testing.T) {
	msg := EventMessage{
		MessageType: "post",
		Content: `{
			"content":[
				[
					{"tag":"text","text":"引用正文 "},
					{"tag":"a","text":"详情","href":"https://example.test/detail"}
				]
			]
		}`,
	}
	want := "引用正文 [详情](https://example.test/detail)"
	if got := msg.Text(); got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestEventMessagePostTextMatchesMentionUserID(t *testing.T) {
	msg := EventMessage{
		MessageType: "post",
		Content: `{
			"zh_cn":{
				"content":[[
					{"tag":"at","user_id":"user_bot","user_name":"Bot"},
					{"tag":"text","text":" 和 "},
					{"tag":"at","user_id":"user_alice","user_name":"Alice"}
				]]
			}
		}`,
		Mentions: []Mention{
			{ID: SenderID{UserID: "user_bot"}, Name: "Bot"},
			{Key: "@_alice", ID: SenderID{UserID: "user_alice"}, Name: "Alice"},
		},
	}
	got := msg.TextWithMentionFilter(func(mention Mention) bool {
		return mention.ID.UserID == "user_bot"
	})
	want := "和 @Alice"
	if got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestParseURLVerification(t *testing.T) {
	hook, err := ParseWebhook([]byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hook.IsURLVerification() || hook.Challenge != "challenge-1" || !hook.VerifyToken("verify-token") {
		t.Fatalf("hook=%+v", hook)
	}
}

func TestDecryptAndDecodeWebhookBody(t *testing.T) {
	encrypted := encryptForTest(t, `{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`, "test key")
	decoded, err := DecodeWebhookBody([]byte(`{"encrypt":"`+encrypted+`"}`), "test key")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := ParseWebhook(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if hook.Challenge != "challenge-1" {
		t.Fatalf("hook=%+v", hook)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"encrypt":"abc"}`)
	sum := sha256.Sum256([]byte("timestamp" + "nonce" + "key" + string(body)))
	if VerifyWebhookSignature("timestamp", "nonce", "key", body, base64.StdEncoding.EncodeToString(sum[:])) {
		t.Fatal("base64 signature should not verify")
	}
}

func TestVerifyWebhookSignatureHex(t *testing.T) {
	body := []byte(`{"encrypt":"abc"}`)
	sum := sha256.Sum256([]byte("timestamp" + "nonce" + "key" + string(body)))
	if !VerifyWebhookSignature("timestamp", "nonce", "key", body, fmt.Sprintf("%x", sum[:])) {
		t.Fatal("hex signature should verify")
	}
}

func encryptForTest(t *testing.T, plain string, key string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append([]byte(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
	iv := []byte("1234567890abcdef")
	out := make([]byte, len(iv)+len(padded))
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[len(iv):], padded)
	return base64.StdEncoding.EncodeToString(out)
}
