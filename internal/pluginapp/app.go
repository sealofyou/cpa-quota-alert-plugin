package pluginapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/management"
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"
	MethodPluginShutdown    = "plugin.shutdown"

	SchemaVersion = 1
	PluginVersion = "0.1.2"

	// HostSchemaVersionCPA72157 is the RPC schema that official CPA v7.2.157
	// sends on plugin.register. The register payload shape is still
	// config_yaml + schema_version; versions 3-6 only change later stream
	// and management encoding, which this plugin does not consume at register.
	HostSchemaVersionCPA72157 = 6

	maxLifecycleRequestBytes = config.MaxYAMLBytes*2 + 4096
)

type ConfigParser func([]byte, config.Getenv) (config.Config, error)

type App struct {
	mu         sync.RWMutex
	getenv     config.Getenv
	parse      ConfigParser
	handler    *management.Handler
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	current    config.Config
	configured bool
	closed     bool
}

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	SchemaVersion uint32 `json:"schema_version,omitempty"`
	ConfigYAML    []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      metadata                 `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

func New(getenv config.Getenv) *App {
	return NewWithHost(getenv, nil)
}

func NewWithHost(getenv config.Getenv, hostFactory HostClientFactory) *App {
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{getenv: getenv, parse: config.ParseYAML, ctx: ctx, cancel: cancel}
	handler, err := newRuntimeHandler(app, getenv, hostFactory)
	if err == nil {
		app.handler = handler
	}
	return app
}

// Call is the pure-Go side of the native ABI dispatcher. It always returns a
// JSON envelope. A non-zero return code tells the CPA host that registration,
// reconfiguration, or the plugin call itself failed.
func (a *App) Call(method string, request []byte) (response []byte, returnCode int) {
	defer func() {
		if recover() != nil {
			response = errorEnvelope("plugin_panic", "plugin call failed")
			returnCode = 1
		}
	}()

	if a == nil {
		return errorEnvelope("plugin_unavailable", "plugin is unavailable"), 1
	}
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			var shutdownErr *shutdownError
			if errors.As(err, &shutdownErr) {
				return errorEnvelope("plugin_shutdown", "plugin is shut down"), 1
			}
			return errorEnvelope("invalid_config", "configuration is invalid"), 1
		}
		return okEnvelope(pluginRegistration()), 0
	case management.MethodManagementRegister:
		if err := validateManagementRegisterRequest(request); err != nil {
			return errorEnvelope("invalid_request", "management request is invalid"), 1
		}
		return a.callManagement(method, request)
	case management.MethodManagementHandle:
		return a.callManagement(method, request)
	case MethodPluginShutdown:
		a.Shutdown()
		return okEnvelope(map[string]any{}), 0
	case "":
		return errorEnvelope("invalid_method", "method is required"), 1
	default:
		return errorEnvelope("unknown_method", "unknown plugin method"), 0
	}
}

func (a *App) Current() (config.Config, bool) {
	if a == nil {
		return config.Config{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.configured || a.closed {
		return config.Config{}, false
	}
	return cloneConfig(a.current), true
}

func (a *App) Shutdown() {
	a.Close()
}

// Close is intentionally non-blocking because App.Call(plugin.shutdown) may be
// used by pure-Go or non-official hosts without CPA's guarded native shutdown
// ordering. Official native shutdown calls ShutdownAndWait after detaching the
// App from the global dispatcher.
func (a *App) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	a.configured = false
	a.current = config.Config{}
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
}

func (a *App) Wait() {
	if a == nil {
		return
	}
	a.wg.Wait()
}

func (a *App) ShutdownAndWait() {
	a.Close()
	a.Wait()
}

func (a *App) configure(request []byte) error {
	if len(request) > maxLifecycleRequestBytes {
		return errors.New("lifecycle request is too large")
	}
	req, err := decodeLifecycleRequest(request)
	if err != nil {
		return err
	}
	parser := a.parse
	if parser == nil {
		return errors.New("configuration parser is unavailable")
	}
	parsed, err := parser(req.ConfigYAML, a.getenv)
	if err != nil {
		return err
	}
	parsed = cloneConfig(parsed)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return &shutdownError{}
	}
	a.current = parsed
	a.configured = true
	return nil
}

func (a *App) callManagement(method string, request []byte) ([]byte, int) {
	ctx, handler, done, err := a.beginManagementCall()
	if err != nil {
		if errors.Is(err, errManagementShutdown) {
			return errorEnvelope("plugin_shutdown", "plugin is shut down"), 1
		}
		if errors.Is(err, errManagementNotConfigured) {
			return errorEnvelope("not_configured", "plugin is not configured"), 1
		}
		return errorEnvelope("management_unavailable", "management handler is unavailable"), 1
	}
	defer done()
	raw, code := handler.Call(ctx, method, request)
	if code != 0 {
		return errorEnvelope("management_error", "management call failed"), 1
	}
	return okEnvelopeRaw(raw), 0
}

var (
	errManagementShutdown      = errors.New("plugin is shut down")
	errManagementNotConfigured = errors.New("plugin is not configured")
	errManagementUnavailable   = errors.New("management handler is unavailable")
)

func (a *App) beginManagementCall() (context.Context, *management.Handler, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, nil, nil, errManagementShutdown
	}
	if !a.configured {
		return nil, nil, nil, errManagementNotConfigured
	}
	if a.handler == nil || a.ctx == nil {
		return nil, nil, nil, errManagementUnavailable
	}
	a.wg.Add(1)
	return a.ctx, a.handler, a.wg.Done, nil
}

func decodeLifecycleRequest(raw []byte) (lifecycleRequest, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return lifecycleRequest{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var req lifecycleRequest
	if err := decoder.Decode(&req); err != nil {
		return lifecycleRequest{}, errors.New("invalid lifecycle request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return lifecycleRequest{}, errors.New("invalid lifecycle request")
	}
	if req.SchemaVersion > HostSchemaVersionCPA72157 {
		return lifecycleRequest{}, errors.New("unsupported lifecycle request schema")
	}
	return req, nil
}

func validateManagementRegisterRequest(raw []byte) error {
	if len(raw) > management.MaxManagementRequestBytes {
		return errors.New("management register request is too large")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("invalid management register request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid management register request")
	}
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: SchemaVersion,
		Metadata: metadata{
			Name:             "cpa-quota-alert-plugin",
			Version:          PluginVersion,
			Author:           "sealofyou",
			GitHubRepository: "https://github.com/sealofyou/cpa-quota-alert-plugin",
			Logo:             "",
			ConfigFields: []configField{
				{Name: "dry_run", Type: "boolean", Description: "Evaluates alerts without delivering notifications."},
				{Name: "low_threshold", Type: "number", Description: "Creates a low-quota event below this Plus-week equivalent."},
				{Name: "recovery_threshold", Type: "number", Description: "Creates a recovery event at or above this Plus-week equivalent."},
				{Name: "mail.templates", Type: "object", Description: "Optional public-safe subject/body overrides. Recipients stay in environment variables."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func okEnvelope(value any) []byte {
	result, err := json.Marshal(value)
	if err != nil {
		return errorEnvelope("plugin_error", "plugin response failed")
	}
	raw, err := json.Marshal(Envelope{OK: true, Result: result})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin response failed"}}`)
	}
	return raw
}

func okEnvelopeRaw(result json.RawMessage) []byte {
	if len(result) == 0 {
		return errorEnvelope("plugin_error", "plugin response failed")
	}
	raw, err := json.Marshal(Envelope{OK: true, Result: result})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin response failed"}}`)
	}
	return raw
}

func errorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin response failed"}}`)
	}
	return raw
}

type shutdownError struct{}

func (*shutdownError) Error() string { return "plugin is shut down" }

func cloneConfig(in config.Config) config.Config {
	out := in
	out.PlanRules = make(map[string]config.PlanRule, len(in.PlanRules))
	for name, rule := range in.PlanRules {
		rule.Aliases = append([]string(nil), rule.Aliases...)
		out.PlanRules[name] = rule
	}
	out.Aliases = cloneStringMap(in.Aliases)
	out.IgnoredPlans = make(config.PlanSet, len(in.IgnoredPlans))
	for value := range in.IgnoredPlans {
		out.IgnoredPlans[value] = struct{}{}
	}
	out.TerminalErrorCodes = make(config.StringSet, len(in.TerminalErrorCodes))
	for value := range in.TerminalErrorCodes {
		out.TerminalErrorCodes[value] = struct{}{}
	}
	out.AllowedQuotaHosts = make(config.HostSet, len(in.AllowedQuotaHosts))
	for value := range in.AllowedQuotaHosts {
		out.AllowedQuotaHosts[value] = struct{}{}
	}
	out.SMTP.NonSecretLabels = cloneStringMap(in.SMTP.NonSecretLabels)
	out.Webhook.NonSecretMetadata = cloneStringMap(in.Webhook.NonSecretMetadata)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
