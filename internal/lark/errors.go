package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
)

var (
	ErrCredentialRejected = errors.New("lark credential rejected")
	ErrTransientFailure   = errors.New("lark transient platform failure")
)

type HTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s failed: status=%d body=%s", e.Method, e.Path, e.StatusCode, e.Body)
}

func credentialRejected(message string) error {
	return fmt.Errorf("%w: %s", ErrCredentialRejected, message)
}

func transientFailure(message string) error {
	return fmt.Errorf("%w: %s", ErrTransientFailure, message)
}

func responseError(operation string, code int, message string) error {
	detail := fmt.Sprintf("%s failed: code=%d msg=%s", operation, code, message)
	if credentialResponseRejected(code) {
		return credentialRejected(detail)
	}
	if transientResponseCode(code) {
		return transientFailure(detail)
	}
	return fmt.Errorf("%s", detail)
}

func credentialResponseRejected(code int) bool {
	switch code {
	case 10005, 10012, 10013, 10014, 10015,
		99991663, 99991664, 99991665, 99991671, 99991672:
		return true
	default:
		return false
	}
}

func transientResponseCode(code int) bool {
	switch code {
	case 1500, 1503, 1551, 1557, 1642, 5000, 65001, 10101,
		96001, 96002, 96402, 1000003, 1000004, 1000005, 99991400:
		return true
	default:
		return false
	}
}

func IsCredentialRejected(err error) bool {
	if errors.Is(err, ErrCredentialRejected) {
		return true
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden {
		return true
	}
	code, ok := larkHTTPErrorCode(httpErr)
	return ok && credentialResponseRejected(code)
}

func IsRetryableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, ErrTransientFailure) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError {
		return true
	}
	code, ok := larkHTTPErrorCode(httpErr)
	return ok && transientResponseCode(code)
}

func larkHTTPErrorCode(httpErr *HTTPError) (int, bool) {
	if httpErr == nil {
		return 0, false
	}
	var payload struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &payload) != nil || payload.Code == 0 {
		return 0, false
	}
	return payload.Code, true
}
