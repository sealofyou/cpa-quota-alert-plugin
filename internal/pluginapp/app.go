package pluginapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"
	MethodPluginShutdown    = "plugin.shutdown"

	SchemaVersion = 1
	PluginVersion = "0.1.0"

	maxLifecycleRequestBytes = config.MaxYAMLBytes*2 + 4096
)

type ConfigParser func([]byte, config.Getenv) (config.Config, error)

type App struct {
	mu         sync.RWMutex
	getenv     config.Getenv
	parse      ConfigParser
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
	ConfigYAML []byte `json:"config_yaml"`
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
	return &App{getenv: getenv, parse: config.ParseYAML}
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
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.configured = false
	a.current = config.Config{}
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
	return req, nil
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
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: false},
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
