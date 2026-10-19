package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultRequestTimeout = 15 * time.Second

const maxMediaBytes int64 = 32 << 20

const (
	maxImageUploadBytes int64 = 10 << 20
	maxFileUploadBytes  int64 = 30 << 20
)

func blockedMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

type Client struct {
	BaseURL              string
	AppID                string
	AppSecret            string
	TenantToken          string
	TenantTokenExpiresAt time.Time
	RequestTimeout       time.Duration
	HTTPClient           *http.Client
}

func NewClient(baseURL, appID, appSecret string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:        baseURL,
		AppID:          appID,
		AppSecret:      appSecret,
		RequestTimeout: defaultRequestTimeout,
		HTTPClient:     http.DefaultClient,
	}
}

func (c *Client) TenantAccessToken(ctx context.Context) (string, error) {
	token, _, err := c.TenantAccessTokenWithExpiry(ctx, time.Now().UTC())
	return token, err
}

func (c *Client) TenantAccessTokenWithExpiry(ctx context.Context, now time.Time) (string, time.Time, error) {
	if strings.TrimSpace(c.TenantToken) != "" && c.TenantTokenExpiresAt.After(now.Add(5*time.Minute)) {
		return c.TenantToken, c.TenantTokenExpiresAt, nil
	}
	if strings.TrimSpace(c.AppID) == "" {
		return "", time.Time{}, credentialRejected("app_id is required")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		return "", time.Time{}, credentialRejected("app_secret is required")
	}
	var resp TokenResponse
	if err := c.doJSON(ctx, http.MethodPost, "/auth/v3/tenant_access_token/internal", nil, map[string]string{
		"app_id":     c.AppID,
		"app_secret": c.AppSecret,
	}, &resp); err != nil {
		return "", time.Time{}, err
	}
	if resp.Code != 0 {
		return "", time.Time{}, responseError("tenant access token", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.TenantAccessToken) == "" {
		return "", time.Time{}, transientFailure("tenant access token failed: missing token")
	}
	expiresIn := resp.Expire
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	expiresAt := now.Add(time.Duration(expiresIn) * time.Second)
	c.TenantToken = resp.TenantAccessToken
	c.TenantTokenExpiresAt = expiresAt
	return resp.TenantAccessToken, expiresAt, nil
}

func (c *Client) BotInfo(ctx context.Context) (*BotInfoResponse, error) {
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp BotInfoResponse
	if err := c.doJSON(ctx, http.MethodGet, "/bot/v3/info", nil, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, responseError("bot info", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.Bot.OpenID) == "" {
		return nil, transientFailure("bot info failed: missing open_id")
	}
	return &resp, nil
}

func (c *Client) UserInfo(ctx context.Context, openID string) (*UserInfoResponse, error) {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return nil, fmt.Errorf("open_id is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp UserInfoResponse
	if err := c.doJSON(ctx, http.MethodGet, "/contact/v3/users/"+url.PathEscape(openID), map[string]string{"user_id_type": "open_id"}, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("user info failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

func (c *Client) ChatInfo(ctx context.Context, chatID string) (*ChatInfoResponse, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp ChatInfoResponse
	if err := c.doJSON(ctx, http.MethodGet, "/im/v1/chats/"+url.PathEscape(chatID), nil, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("chat info failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

func (c *Client) ChatMembers(ctx context.Context, chatID string) (*ChatMembersResponse, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp ChatMembersResponse
	if err := c.doJSON(ctx, http.MethodGet, "/im/v1/chats/"+url.PathEscape(chatID)+"/members", map[string]string{
		"member_id_type": "open_id",
		"page_size":      "50",
	}, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("chat members failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

func (c *Client) Message(ctx context.Context, messageID string) (*MessageItem, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp MessageResponse
	if err := c.doJSON(ctx, http.MethodGet, "/im/v1/messages/"+url.PathEscape(messageID), map[string]string{
		"user_id_type":          "open_id",
		"card_msg_content_type": "raw_card_content",
	}, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("message get failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	item := resp.FirstMessage()
	if item == nil {
		return nil, fmt.Errorf("message get failed: missing message")
	}
	return item, nil
}

func (c *Client) LatestThreadMessage(ctx context.Context, threadID string) (*MessageItem, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("thread_id is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	var resp MessageResponse
	if err := c.doJSON(ctx, http.MethodGet, "/im/v1/messages", map[string]string{
		"container_id_type":     "thread",
		"container_id":          threadID,
		"sort_type":             "ByCreateTimeDesc",
		"page_size":             "1",
		"user_id_type":          "open_id",
		"card_msg_content_type": "raw_card_content",
	}, nil, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("thread messages get failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	item := resp.FirstMessage()
	if item == nil || strings.TrimSpace(item.MessageID) == "" {
		return nil, fmt.Errorf("thread messages get failed: thread %s has no reply target", threadID)
	}
	return item, nil
}

func (c *Client) SendText(ctx context.Context, req SendTextRequest) (*SendTextResponse, error) {
	if strings.TrimSpace(req.ReceiveID) == "" {
		return nil, fmt.Errorf("receive_id is required")
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	content, err := json.Marshal(map[string]string{"text": req.Text})
	if err != nil {
		return nil, err
	}
	return c.SendMessage(ctx, SendMessageRequest{
		ReceiveID:     req.ReceiveID,
		ReceiveIDType: req.ReceiveIDType,
		MsgType:       "text",
		Content:       string(content),
		UUID:          req.UUID,
	})
}

func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*SendMessageResponse, error) {
	if strings.TrimSpace(req.ReceiveID) == "" {
		return nil, fmt.Errorf("receive_id is required")
	}
	msgType := strings.TrimSpace(req.MsgType)
	if msgType == "" {
		msgType = "text"
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	receiveIDType := strings.TrimSpace(req.ReceiveIDType)
	if receiveIDType == "" {
		receiveIDType = ReceiveIDTypeForTarget(req.ReceiveID)
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"receive_id": req.ReceiveID,
		"msg_type":   msgType,
		"content":    req.Content,
	}
	if req.UUID != "" {
		body["uuid"] = req.UUID
	}
	var resp SendMessageResponse
	if err := c.doJSON(ctx, http.MethodPost, "/im/v1/messages", map[string]string{"receive_id_type": receiveIDType}, body, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("send text failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

func (c *Client) ReplyMessage(ctx context.Context, req ReplyMessageRequest) (*SendMessageResponse, error) {
	if strings.TrimSpace(req.MessageID) == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	msgType := strings.TrimSpace(req.MsgType)
	if msgType == "" {
		msgType = "text"
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"msg_type": msgType,
		"content":  req.Content,
	}
	if req.UUID != "" {
		body["uuid"] = req.UUID
	}
	if req.ReplyInThread != nil {
		body["reply_in_thread"] = *req.ReplyInThread
	}
	var resp SendMessageResponse
	if err := c.doJSON(ctx, http.MethodPost, "/im/v1/messages/"+url.PathEscape(req.MessageID)+"/reply", nil, body, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("reply message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

// DownloadMessageResource downloads a message resource to a private temporary
// file. Lark only exposes message resources through the message id and file
// key, so the caller must preserve the message id from the event envelope.
func (c *Client) DownloadMessageResource(ctx context.Context, messageID, resourceID, resourceType string) (string, func(), error) {
	messageID = strings.TrimSpace(messageID)
	resourceID = strings.TrimSpace(resourceID)
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	if messageID == "" {
		return "", nil, fmt.Errorf("message_id is required")
	}
	if resourceID == "" {
		return "", nil, fmt.Errorf("resource_id is required")
	}
	if resourceType != "image" && resourceType != "file" {
		resourceType = "file"
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return "", nil, err
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.url("/im/v1/messages/"+url.PathEscape(messageID)+"/resources/"+url.PathEscape(resourceID), map[string]string{"type": resourceType}), nil)
	if err != nil {
		return "", nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "BeakAgentLark/0.1.0")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(request)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes))
		if readErr != nil {
			return "", nil, readErr
		}
		return "", nil, &HTTPError{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: request.URL.Path, Body: string(data)}
	}
	tmp, err := os.CreateTemp("", "beak-lark-resource-*")
	if err != nil {
		return "", nil, err
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, maxMediaBytes+1)); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if info, statErr := tmp.Stat(); statErr != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, statErr
	} else if info.Size() > maxMediaBytes {
		_ = tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("lark media resource exceeds %d bytes", maxMediaBytes)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// DownloadURL materializes an HTTP(S) media URL to a private temporary file.
// It is intentionally conservative because outbound attachments may originate
// from an untrusted channel payload.
func (c *Client) DownloadURL(ctx context.Context, rawURL string) (string, func(), error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", nil, fmt.Errorf("media URL must be absolute HTTP or HTTPS")
	}
	if blockedMediaHost(parsed.Hostname()) {
		return "", nil, fmt.Errorf("media URL host is not allowed")
	}
	tokenURL := parsed.String()
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "BeakAgentLark/0.1.0")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(request)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, &HTTPError{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: parsed.Path, Body: resp.Status}
	}
	tmp, err := os.CreateTemp("", "beak-lark-outbound-*")
	if err != nil {
		return "", nil, err
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, maxMediaBytes+1)); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if info, statErr := tmp.Stat(); statErr != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, statErr
	} else if info.Size() > maxMediaBytes {
		_ = tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("lark media resource exceeds %d bytes", maxMediaBytes)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// UploadMedia uploads a local file into the Lark media store and returns the
// platform resource key used by image/file message types.
func (c *Client) UploadMedia(ctx context.Context, path, kind, fileName string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("media path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("media path is not a regular file")
	}
	image := strings.EqualFold(strings.TrimSpace(kind), "image") || strings.EqualFold(strings.TrimSpace(kind), "sticker")
	limit := maxFileUploadBytes
	if image {
		limit = maxImageUploadBytes
	}
	if info.Size() == 0 {
		return "", ErrMediaEmpty
	}
	if info.Size() > limit {
		return "", fmt.Errorf("%w: limit=%d bytes", ErrMediaTooLarge, limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	field := "file"
	endpoint := "/im/v1/files"
	if image {
		field = "image"
		endpoint = "/im/v1/images"
	}
	uploadName := strings.TrimSpace(fileName)
	if uploadName == "" {
		uploadName = path
	}
	uploadName = filepath.Base(uploadName)
	part, err := writer.CreateFormFile(field, uploadName)
	if err != nil {
		return "", err
	}
	written, err := io.Copy(part, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written == 0 {
		return "", ErrMediaEmpty
	}
	if written > limit {
		return "", fmt.Errorf("%w: limit=%d bytes", ErrMediaTooLarge, limit)
	}
	if field == "image" {
		if err := writer.WriteField("image_type", "message"); err != nil {
			return "", err
		}
	} else {
		if err := writer.WriteField("file_type", "stream"); err != nil {
			return "", err
		}
		if err := writer.WriteField("file_name", uploadName); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url(endpoint, nil), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("User-Agent", "BeakAgentLark/0.1.0")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &HTTPError{StatusCode: resp.StatusCode, Method: http.MethodPost, Path: endpoint, Body: string(data)}
	}
	var result MediaUploadResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", transientFailure(fmt.Sprintf("decode media upload response: %v", err))
	}
	if result.Code != 0 {
		return "", responseError("upload lark media", result.Code, result.Msg)
	}
	key := strings.TrimSpace(result.Data.ImageKey)
	if key == "" {
		key = strings.TrimSpace(result.Data.FileKey)
	}
	if key == "" {
		return "", transientFailure("upload lark media failed: missing resource key")
	}
	return key, nil
}

func (c *Client) AddMessageReaction(ctx context.Context, req AddMessageReactionRequest) (*MessageReactionResponse, error) {
	if strings.TrimSpace(req.MessageID) == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	emojiType := strings.TrimSpace(req.EmojiType)
	if emojiType == "" {
		return nil, fmt.Errorf("emoji_type is required")
	}
	token, err := c.tokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"reaction_type": map[string]any{
			"emoji_type": emojiType,
		},
	}
	var resp MessageReactionResponse
	if err := c.doJSON(ctx, http.MethodPost, "/im/v1/messages/"+url.PathEscape(req.MessageID)+"/reactions", nil, body, &resp, withBearer(token)); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("add message reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

func (c *Client) tokenForRequest(ctx context.Context) (string, error) {
	token := strings.TrimSpace(c.TenantToken)
	if token != "" && (c.TenantTokenExpiresAt.IsZero() || c.TenantTokenExpiresAt.After(time.Now().UTC().Add(5*time.Minute))) {
		return token, nil
	}
	return c.TenantAccessToken(ctx)
}

type requestOption func(*http.Request)

func withBearer(token string) requestOption {
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, query map[string]string, body any, out any, opts ...requestOption) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, c.url(path, query), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BeakAgentLark/0.1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for _, opt := range opts {
		opt(req)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return transientFailure(fmt.Sprintf("decode response: %v", err))
	}
	return nil
}

func (c *Client) url(path string, query map[string]string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	base = strings.TrimSuffix(base, "/open-apis")
	values := url.Values{}
	for key, value := range query {
		if value != "" {
			values.Set(key, value)
		}
	}
	out := base + "/open-apis/" + strings.TrimLeft(path, "/")
	if encoded := values.Encode(); encoded != "" {
		out += "?" + encoded
	}
	return out
}
