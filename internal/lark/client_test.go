package lark

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
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

func TestParseURLVerification(t *testing.T) {
	hook, err := ParseWebhook([]byte(`{"type":"url_verification","challenge":"challenge-1","token":"verify-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hook.IsURLVerification() || hook.Challenge != "challenge-1" || !hook.VerifyToken("verify-token") {
		t.Fatalf("hook=%+v", hook)
	}
}
