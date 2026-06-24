package beaklark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const larkDefaultPingInterval = 2 * time.Minute
const larkDefaultPongTimeout = 5 * time.Second

type larkHostStreamState struct {
	Fragments    map[string][][]byte
	PingInterval time.Duration
}

func (c Connector) ConnectStream(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount) (*sdk.StreamConnectResult, error) {
	appID := strings.TrimSpace(stringValue(account.Credential["app_id"]))
	appSecret := strings.TrimSpace(stringValue(account.Credential["app_secret"]))
	if appID == "" {
		return nil, fmt.Errorf("lark stream app_id is required")
	}
	if appSecret == "" {
		return nil, fmt.Errorf("lark stream app_secret is required")
	}
	endpoint, err := larkConnectionEndpoint(ctx, runtime, account)
	if err != nil {
		return nil, err
	}
	if endpoint == nil || strings.TrimSpace(endpoint.Url) == "" {
		return nil, fmt.Errorf("lark stream endpoint response is empty")
	}
	pingInterval := larkDefaultPingInterval
	if endpoint.ClientConfig != nil && endpoint.ClientConfig.PingInterval > 0 {
		pingInterval = time.Duration(endpoint.ClientConfig.PingInterval) * time.Second
	}
	parsed, err := url.Parse(endpoint.Url)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return &sdk.StreamConnectResult{
		URL:             endpoint.Url,
		ServiceID:       parsed.Query().Get(larkws.ServiceID),
		ReadMessageType: sdk.StreamMessageTypeBinary,
		PingInterval:    pingInterval,
		PongTimeout:     larkDefaultPongTimeout,
		State:           &larkHostStreamState{Fragments: map[string][][]byte{}, PingInterval: pingInterval},
		HealthUpdates: map[string]any{
			sdk.RuntimeHealthKeyStreamConnectionState:      sdk.RuntimeHealthStateConnected,
			sdk.RuntimeHealthKeyStreamConnectedAt:          now,
			sdk.RuntimeHealthKeyStreamLastActivityAt:       now,
			sdk.RuntimeHealthKeyStreamReconnectError:       "",
			sdk.RuntimeHealthKeyStreamReconnectErrorAt:     "",
			sdk.RuntimeHealthKeyStreamLastError:            "",
			sdk.RuntimeHealthKeyStreamLastErrorAt:          "",
			sdk.RuntimeHealthKeyStreamReconnectRequestedAt: "",
		},
	}, nil
}

func (c Connector) BuildStreamPing(ctx context.Context, req sdk.StreamPingRequest) (*sdk.StreamFrame, error) {
	serviceID, _ := strconv.ParseInt(strings.TrimSpace(req.ServiceID), 10, 32)
	frame := larkws.NewPingFrame(int32(serviceID))
	payload, err := frame.Marshal()
	if err != nil {
		return nil, err
	}
	return &sdk.StreamFrame{MessageType: sdk.StreamMessageTypeBinary, Data: payload}, nil
}

func (c Connector) HandleStreamFrame(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, req sdk.StreamFrameRequest) (*sdk.StreamFrameResult, error) {
	state := larkStreamState(req.State)
	out := &sdk.StreamFrameResult{
		State: state,
		HealthUpdates: map[string]any{
			sdk.RuntimeHealthKeyStreamLastActivityAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	if req.MessageType != 0 && req.MessageType != sdk.StreamMessageTypeBinary {
		return out, nil
	}
	var frame larkws.Frame
	if err := frame.Unmarshal(req.Data); err != nil {
		return out, err
	}
	switch larkws.FrameType(frame.Method) {
	case larkws.FrameTypeControl:
		return c.handleLarkControlFrame(frame, state, out), nil
	case larkws.FrameTypeData:
		return c.handleLarkDataFrame(ctx, runtime, account, &frame, state, out)
	default:
		return out, nil
	}
}

func (c Connector) handleLarkControlFrame(frame larkws.Frame, state *larkHostStreamState, out *sdk.StreamFrameResult) *sdk.StreamFrameResult {
	headers := larkws.Headers(frame.Headers)
	if larkws.MessageType(headers.GetString(larkws.HeaderType)) != larkws.MessageTypePong || len(frame.Payload) == 0 {
		return out
	}
	var config larkws.ClientConfig
	if err := json.Unmarshal(frame.Payload, &config); err == nil && config.PingInterval > 0 {
		state.PingInterval = time.Duration(config.PingInterval) * time.Second
		out.PingInterval = state.PingInterval
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	out.HealthUpdates[sdk.RuntimeHealthKeyStreamLastPongAt] = now
	out.HealthUpdates[sdk.RuntimeHealthKeyStreamLastActivityAt] = now
	return out
}

func (c Connector) handleLarkDataFrame(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount, frame *larkws.Frame, state *larkHostStreamState, out *sdk.StreamFrameResult) (*sdk.StreamFrameResult, error) {
	headers := larkws.Headers(frame.Headers)
	payload := frame.Payload
	if sum := headers.GetInt(larkws.HeaderSum); sum > 1 {
		var ok bool
		payload, ok = state.combine(headers.GetString(larkws.HeaderMessageID), sum, headers.GetInt(larkws.HeaderSeq), payload)
		if !ok {
			return out, nil
		}
	}
	if larkws.MessageType(headers.GetString(larkws.HeaderType)) != larkws.MessageTypeEvent {
		return out, nil
	}

	startedAt := time.Now()
	var handled *EventResult
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(frameCtx context.Context, event *larkim.P2MessageReceiveV1) error {
			body, err := larkStreamEventBody(event)
			if err != nil {
				return err
			}
			result, err := c.HandleEvent(frameCtx, runtime, account, body)
			if err != nil {
				return err
			}
			handled = result
			return nil
		})

	result, err := eventHandler.Do(ctx, payload)
	resp := larkws.NewResponseByCode(http.StatusOK)
	if err != nil {
		resp = larkws.NewResponseByCode(http.StatusInternalServerError)
	} else if result != nil {
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			resp = larkws.NewResponseByCode(http.StatusInternalServerError)
		} else {
			resp.Data = data
		}
	}
	headers.Add(larkws.HeaderBizRt, strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10))
	responseBody, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		return out, marshalErr
	}
	frame.Payload = responseBody
	frame.Headers = headers
	outbound, marshalErr := frame.Marshal()
	if marshalErr != nil {
		return out, marshalErr
	}
	out.ResponseFrames = append(out.ResponseFrames, sdk.StreamFrame{MessageType: sdk.StreamMessageTypeBinary, Data: outbound})
	out.EventResult = larkStreamEventResult(handled)
	if handled != nil && !handled.Ignored && strings.TrimSpace(handled.MessageUUID) != "" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		out.HealthUpdates[sdk.RuntimeHealthKeyStreamLastEventAt] = now
		out.HealthUpdates[sdk.RuntimeHealthKeyStreamLastActivityAt] = now
	}
	return out, err
}

func larkConnectionEndpoint(ctx context.Context, runtime sdk.Runtime, account sdk.ChannelAccount) (*larkws.Endpoint, error) {
	body, err := json.Marshal(map[string]string{
		"AppID":     stringValue(account.Credential["app_id"]),
		"AppSecret": stringValue(account.Credential["app_secret"]),
	})
	if err != nil {
		return nil, err
	}
	client := runtime.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURLFromCredential(account.Credential)+larkws.GenEndpointUri, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("locale", "zh")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lark stream endpoint failed: status=%s body=%s", resp.Status, string(respBody))
	}
	var endpointResp larkws.EndpointResp
	if err := json.Unmarshal(respBody, &endpointResp); err != nil {
		return nil, err
	}
	if endpointResp.Code != larkws.OK {
		return nil, fmt.Errorf("lark stream endpoint failed: code=%d msg=%s", endpointResp.Code, endpointResp.Msg)
	}
	return endpointResp.Data, nil
}

func larkStreamState(value any) *larkHostStreamState {
	if state, ok := value.(*larkHostStreamState); ok && state != nil {
		if state.Fragments == nil {
			state.Fragments = map[string][][]byte{}
		}
		if state.PingInterval <= 0 {
			state.PingInterval = larkDefaultPingInterval
		}
		return state
	}
	return &larkHostStreamState{Fragments: map[string][][]byte{}, PingInterval: larkDefaultPingInterval}
}

func (s *larkHostStreamState) combine(messageID string, sum, seq int, payload []byte) ([]byte, bool) {
	if s.Fragments == nil {
		s.Fragments = map[string][][]byte{}
	}
	fragments, ok := s.Fragments[messageID]
	if !ok {
		fragments = make([][]byte, sum)
		s.Fragments[messageID] = fragments
	}
	if seq >= 0 && seq < len(fragments) {
		fragments[seq] = payload
	}
	size := 0
	for _, item := range fragments {
		if len(item) == 0 {
			return nil, false
		}
		size += len(item)
	}
	combined := make([]byte, 0, size)
	for _, item := range fragments {
		combined = append(combined, item...)
	}
	delete(s.Fragments, messageID)
	return combined, true
}

func larkStreamEventBody(event *larkim.P2MessageReceiveV1) ([]byte, error) {
	if event == nil {
		return nil, fmt.Errorf("lark stream message event is required")
	}
	if event.EventReq != nil && len(event.Body) > 0 {
		return append([]byte(nil), event.Body...), nil
	}
	return json.Marshal(event)
}

func larkStreamEventResult(result *EventResult) *sdk.StreamEventResult {
	if result == nil {
		return nil
	}
	return &sdk.StreamEventResult{
		Type:        result.Type,
		Ignored:     result.Ignored,
		Reason:      result.Reason,
		SessionUUID: result.SessionUUID,
		MessageUUID: result.MessageUUID,
		Inbound:     result.Inbound,
	}
}
