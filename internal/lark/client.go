package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRequestTimeout = 15 * time.Second

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
		return "", time.Time{}, fmt.Errorf("app_id is required")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		return "", time.Time{}, fmt.Errorf("app_secret is required")
	}
	var resp TokenResponse
	if err := c.doJSON(ctx, http.MethodPost, "/auth/v3/tenant_access_token/internal", nil, map[string]string{
		"app_id":     c.AppID,
		"app_secret": c.AppSecret,
	}, &resp); err != nil {
		return "", time.Time{}, err
	}
	if resp.Code != 0 {
		return "", time.Time{}, fmt.Errorf("tenant access token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.TenantAccessToken) == "" {
		return "", time.Time{}, fmt.Errorf("tenant access token failed: missing token")
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
		return nil, fmt.Errorf("bot info failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.Bot.OpenID) == "" {
		return nil, fmt.Errorf("bot info failed: missing open_id")
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
		return fmt.Errorf("%s %s failed: status=%d body=%s", method, path, resp.StatusCode, string(data))
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
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
