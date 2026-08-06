package abi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const Version = "v1"

const (
	MethodAuthList       = "host.auth.list"
	MethodAuthGet        = "host.auth.get"
	MethodAuthGetRuntime = "host.auth.get_runtime"
	MethodHTTPDo         = "host.http.do"
)

var (
	ErrInvalidEnvelope = errors.New("invalid host callback envelope")
	ErrInvalidResult   = errors.New("invalid host callback result")
)

type Caller interface {
	Call(ctx context.Context, method string, request []byte) ([]byte, error)
}

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Status    int    `json:"status,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type CallbackError struct {
	Code      string
	Status    int
	Retryable bool
}

func (e *CallbackError) Error() string {
	if e == nil {
		return "host callback error"
	}
	if e.Code != "" && e.Status != 0 {
		return fmt.Sprintf("host callback error: code=%s status=%d retryable=%t", e.Code, e.Status, e.Retryable)
	}
	if e.Code != "" {
		return fmt.Sprintf("host callback error: code=%s retryable=%t", e.Code, e.Retryable)
	}
	if e.Status != 0 {
		return fmt.Sprintf("host callback error: status=%d retryable=%t", e.Status, e.Retryable)
	}
	return fmt.Sprintf("host callback error: retryable=%t", e.Retryable)
}

type Client struct {
	caller         Caller
	hostCallbackID string
}

func NewClient(caller Caller, hostCallbackID string) *Client {
	return &Client{caller: caller, hostCallbackID: hostCallbackID}
}

type HostAuthFileEntry struct {
	AuthIndex   string `json:"auth_index"`
	Type        string `json:"type"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	RuntimeOnly bool   `json:"runtime_only"`
}

type AuthFile struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
}

type RuntimeAuth struct {
	Auth HostAuthFileEntry `json:"auth"`
}

type HTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers"`
	Body           []byte              `json:"body,omitempty"`
}

type HTTPResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

func (c *Client) ListAuth(ctx context.Context) ([]HostAuthFileEntry, error) {
	var result struct {
		Files []HostAuthFileEntry `json:"files"`
	}
	if err := c.call(ctx, MethodAuthList, map[string]string{"host_callback_id": c.hostCallbackID}, &result); err != nil {
		return nil, err
	}
	if result.Files == nil {
		return nil, ErrInvalidResult
	}
	return append([]HostAuthFileEntry(nil), result.Files...), nil
}

func (c *Client) GetAuth(ctx context.Context, authIndex string) (AuthFile, error) {
	var result AuthFile
	if err := c.call(ctx, MethodAuthGet, map[string]string{"host_callback_id": c.hostCallbackID, "auth_index": authIndex}, &result); err != nil {
		return AuthFile{}, err
	}
	if result.AuthIndex == "" || result.JSON == nil {
		return AuthFile{}, ErrInvalidResult
	}
	result.JSON = append(json.RawMessage(nil), result.JSON...)
	return result, nil
}

func (c *Client) GetRuntime(ctx context.Context) (RuntimeAuth, error) {
	var result RuntimeAuth
	if err := c.call(ctx, MethodAuthGetRuntime, map[string]string{"host_callback_id": c.hostCallbackID}, &result); err != nil {
		return RuntimeAuth{}, err
	}
	if result.Auth == (HostAuthFileEntry{}) {
		return RuntimeAuth{}, ErrInvalidResult
	}
	return result, nil
}

func (c *Client) HTTPDo(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	req.HostCallbackID = c.hostCallbackID
	req.Headers = cloneHeaders(req.Headers)
	req.Body = append([]byte(nil), req.Body...)
	var result HTTPResponse
	if err := c.call(ctx, MethodHTTPDo, req, &result); err != nil {
		return HTTPResponse{}, err
	}
	result.Headers = cloneHeaders(result.Headers)
	result.Body = append([]byte(nil), result.Body...)
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, req any, result any) error {
	if c == nil || c.caller == nil {
		return errors.New("host callback caller is nil")
	}
	request, err := json.Marshal(req)
	if err != nil {
		return err
	}
	response, err := c.caller.Call(ctx, method, request)
	if err != nil {
		return err
	}
	var env Envelope
	if err := json.Unmarshal(response, &env); err != nil {
		return fmt.Errorf("%w", ErrInvalidEnvelope)
	}
	if !env.OK {
		if env.Error == nil {
			return &CallbackError{}
		}
		return &CallbackError{Code: env.Error.Code, Status: env.Error.Status, Retryable: env.Error.Retryable}
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return ErrInvalidEnvelope
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("%w", ErrInvalidResult)
	}
	return nil
}

func cloneHeaders(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}
