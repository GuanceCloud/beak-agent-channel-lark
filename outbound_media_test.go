package beaklark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func TestGeneratedMediaDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, text, reply                           string
		failUpload, failSend, failNotice, allFailed bool
	}{
		{name: "mixed", text: "报告已生成"},
		{name: "attachment only"},
		{name: "thread upload denied", text: "报告已生成", reply: "om_parent", failUpload: true},
		{name: "send failure", failSend: true},
		{name: "all failed", failUpload: true, allFailed: true},
		{name: "notice failure", text: "报告已生成", failUpload: true, failNotice: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/file.bin"
			if err := os.WriteFile(path, []byte("fixture-content"), 0600); err != nil {
				t.Fatal(err)
			}
			attachments := []sdk.MediaAttachment{
				{ID: "image", Kind: "image", Source: "path", Path: path, FileName: "图.png"},
				{ID: "file", Kind: "file", Source: "path", Path: path, FileName: "报告.pdf"},
			}
			if tc.allFailed {
				attachments = attachments[1:]
			}
			type sentMessage struct {
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
				UUID      string `json:"uuid"`
				ReceiveID string `json:"receive_id"`
			}
			var sent []sentMessage
			client := &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					return testJSONResponse(map[string]any{"code": 0, "tenant_access_token": "fixture-token", "expire": 7200})
				case "/open-apis/im/v1/images", "/open-apis/im/v1/files":
					if err := r.ParseMultipartForm(1024); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					field, wantName := "file", "报告.pdf"
					if strings.HasSuffix(r.URL.Path, "images") {
						field, wantName = "image", "图.png"
					}
					f, h, err := r.FormFile(field)
					if err != nil {
						t.Fatal(err)
					}
					b, err := io.ReadAll(f)
					f.Close()
					if err != nil {
						t.Fatal(err)
					}
					if h.Filename != wantName || string(b) != "fixture-content" {
						t.Errorf("upload name=%q body=%q", h.Filename, b)
					}
					if field == "image" {
						if r.FormValue("image_type") != "message" {
							t.Errorf("image_type=%q", r.FormValue("image_type"))
						}
						return testJSONResponse(map[string]any{"code": 0, "data": map[string]any{"image_key": "img_fixture"}})
					}
					if r.FormValue("file_name") != wantName || r.FormValue("file_type") != "stream" {
						t.Errorf("file fields=%v", r.MultipartForm.Value)
					}
					if tc.failUpload {
						return testJSONResponse(map[string]any{"code": 99991672, "msg": "sensitive-server-response"})
					}
					return testJSONResponse(map[string]any{"code": 0, "data": map[string]any{"file_key": "file_fixture"}})
				case "/open-apis/im/v1/messages", "/open-apis/im/v1/messages/om_parent/reply":
					var m sentMessage
					if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
						t.Fatal(err)
					}
					if tc.reply != "" {
						if !strings.HasSuffix(r.URL.Path, "/om_parent/reply") {
							t.Errorf("wrong thread: %s", r.URL.Path)
						}
					} else if m.ReceiveID != "oc_target" {
						t.Errorf("target=%q", m.ReceiveID)
					}
					sent = append(sent, m)
					if m.MsgType == "file" && tc.failSend {
						return testJSONResponse(map[string]any{"code": 230001, "msg": "sensitive-server-response"})
					}
					if m.MsgType == "text" && strings.Contains(m.Content, "报告.pdf") && tc.failNotice {
						return nil, fmt.Errorf("notice unavailable")
					}
					return testJSONResponse(map[string]any{"code": 0, "data": map[string]any{"message_id": fmt.Sprintf("om_%d", len(sent))}})
				default:
					return nil, fmt.Errorf("unexpected request %s", r.URL.Path)
				}
			})}
			req := sdk.OutboundMessage{AccountUUID: "account", MessageUUID: "reply-id", ChatID: " oc_target ", Text: tc.text, Attachments: attachments}
			if tc.reply != "" {
				req.Raw = map[string]any{"reply_to_message_id": tc.reply}
			}
			rt := sdk.Runtime{HTTPClient: client, Account: sdkAccount("account", "app", "secret", "")}
			connector := NewConnector()
			result, err := connector.Send(context.Background(), rt, req)
			if tc.allFailed {
				if err == nil {
					t.Fatal("all failed must return error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if result == nil || len(result.AttachmentResults) != len(attachments) {
				t.Fatalf("result=%+v", result)
			}
			wantStatus := sdk.AttachmentStatusSent
			if tc.failUpload || tc.failSend {
				wantStatus = sdk.AttachmentStatusFailed
			}
			if result.AttachmentResults[len(attachments)-1].Status != wantStatus {
				t.Fatalf("result=%+v", result)
			}
			if tc.failUpload || tc.failSend {
				if len(sent) == 0 {
					t.Fatal("missing failure notice")
				}
				notice := sent[len(sent)-1]
				if notice.MsgType != "text" || !strings.Contains(notice.Content, "报告.pdf") {
					t.Fatalf("missing notice: %+v", sent)
				}
				if strings.Contains(notice.Content, "sensitive-server-response") || strings.Contains(notice.Content, path) {
					t.Fatalf("notice exposed internal details: %s", notice.Content)
				}
			}
			first := append([]sentMessage(nil), sent...)
			sent = nil
			_, _ = connector.Send(context.Background(), rt, req)
			if len(first) != len(sent) {
				t.Fatalf("retry messages changed: %d / %d", len(first), len(sent))
			}
			ids := map[string]bool{}
			for i, m := range first {
				if m.UUID == "" || len(m.UUID) > 50 || m.UUID != sent[i].UUID {
					t.Errorf("unstable uuid: %+v / %+v", m, sent[i])
				}
				if ids[m.UUID] {
					t.Errorf("duplicate uuid within reply: %q", m.UUID)
				}
				ids[m.UUID] = true
			}
		})
	}
}
