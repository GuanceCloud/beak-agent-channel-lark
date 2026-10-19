package beaklark

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/GuanceCloud/beak-agent-channel-lark/internal/lark"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func sendLarkAttachment(ctx context.Context, client *lark.Client, req sdk.OutboundMessage,
	attachment sdk.MediaAttachment, index int, replyTo string, replyInThread *bool,
) (item sdk.AttachmentSendResult, err error) {
	item = sdk.AttachmentSendResult{AttachmentID: attachment.ID, Kind: attachment.Kind, Status: sdk.AttachmentStatusFailed}
	defer func() {
		if err != nil {
			item.Error = err.Error()
		}
	}()
	if err = sdk.ValidateMediaAttachment(attachment); err != nil {
		return item, err
	}
	path := strings.TrimSpace(attachment.Path)
	if path == "" && strings.TrimSpace(attachment.URL) != "" {
		var cleanup func()
		path, cleanup, err = client.DownloadURL(ctx, attachment.URL)
		if err != nil {
			return item, err
		}
		defer cleanup()
	}
	if path == "" {
		return item, fmt.Errorf("lark outbound attachment path or URL is required")
	}
	key, err := client.UploadMedia(ctx, path, attachment.Kind, attachment.FileName)
	if err != nil {
		return item, err
	}
	msgType, content, status, err := larkMediaMessage(attachment, key)
	if err != nil {
		return item, err
	}
	uuid := larkMediaMessageUUID(req, fmt.Sprintf("attachment:%d:%s", index, attachment.ID))
	resp, err := sendLarkMessage(ctx, client, strings.TrimSpace(req.ChatID), receiveIDType(req), replyTo, replyInThread, msgType, content, uuid)
	if err != nil {
		return item, err
	}
	item.MessageID = strings.TrimSpace(resp.Data.MessageID)
	if item.MessageID == "" {
		return item, fmt.Errorf("lark attachment send returned no message ID")
	}
	item.Status, item.PlatformResourceID = status, key
	return item, nil
}

// Feishu deduplicates a UUID within its one-hour window. An absent parent
// message ID must not turn unrelated sends into duplicates.
func larkMediaMessageUUID(req sdk.OutboundMessage, part string) string {
	if strings.TrimSpace(req.MessageUUID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		req.WorkspaceUUID, req.AccountUUID, req.ChatID, req.ThreadID, req.MessageUUID, part,
	}, "\x00")))
	return fmt.Sprintf("%x", sum[:16])
}

func larkAttachmentFailure(attachment sdk.MediaAttachment, index int, err error) string {
	name := strings.TrimSpace(attachment.FileName)
	if name == "" {
		name = fmt.Sprintf("附件 %d", index+1)
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		if r == '<' {
			return '＜'
		}
		if r == '>' {
			return '＞'
		}
		return r
	}, name)
	if runes := []rune(name); len(runes) > 120 {
		name = string(runes[:120]) + "…"
	}
	reason := "上传或发送失败，请稍后重试"
	switch {
	case errors.Is(err, lark.ErrMediaEmpty):
		reason = "文件为空，请重新生成"
	case errors.Is(err, lark.ErrMediaTooLarge):
		reason = "文件超过 30 MB，请缩小后重试"
		if attachment.Kind == sdk.MediaKindImage {
			reason = "图片超过 10 MB，请缩小后重试"
		}
	case lark.IsCredentialRejected(err):
		reason = "机器人资源权限或授权不足，请联系管理员检查"
	}
	return name + "：" + reason
}

func larkAttachmentFailureNotice(failures []string) string {
	shown := failures
	if len(shown) > 10 {
		shown = shown[:10]
	}
	text := "以下附件未能发送：\n" + strings.Join(shown, "\n")
	if len(failures) > len(shown) {
		text += fmt.Sprintf("\n另有 %d 个附件发送失败。", len(failures)-len(shown))
	}
	return text
}
