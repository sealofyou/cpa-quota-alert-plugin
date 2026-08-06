package pluginapp

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/abi"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/codexquota"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/management"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/monitor"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/notify"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/state"
)

type HostClientFactory func(hostCallbackID string) *abi.Client

func newRuntimeHandler(app *App, getenv config.Getenv, hostFactory HostClientFactory) (*management.Handler, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return management.NewHandler(management.Dependencies{
		ConfigProvider: runtimeConfigProvider{app: app},
		CheckerFactory: runtimeCheckerFactory{hostFactory: hostFactory},
		StoreFactory:   runtimeStoreFactory{},
		ChannelFactory: runtimeChannelFactory{getenv: getenv},
		Version:        PluginVersion,
	})
}

type runtimeConfigProvider struct {
	app *App
}

func (p runtimeConfigProvider) Current(ctx context.Context) (config.Config, bool) {
	if err := ctx.Err(); err != nil {
		return config.Config{}, false
	}
	if p.app == nil {
		return config.Config{}, false
	}
	return p.app.Current()
}

type runtimeCheckerFactory struct {
	hostFactory HostClientFactory
}

func (f runtimeCheckerFactory) NewChecker(hostCallbackID string, cfg config.Config) (management.Checker, error) {
	if f.hostFactory == nil {
		return nil, errors.New("host client factory unavailable")
	}
	client := f.hostFactory(hostCallbackID)
	if client == nil {
		return nil, errors.New("host client unavailable")
	}
	return codexquota.NewService(codexquota.ABIHost{Client: client}, cfg), nil
}

type runtimeStoreFactory struct{}

func (runtimeStoreFactory) NewStore(cfg config.Config) (management.Store, error) {
	if strings.TrimSpace(cfg.StatePath) == "" {
		return nil, errors.New("state path unavailable")
	}
	return runtimeStore{store: state.Store{Path: cfg.StatePath, FS: state.OSFS{}}}, nil
}

type runtimeStore struct {
	store state.Store
}

func (s runtimeStore) Load(ctx context.Context) (monitor.State, error) {
	if err := ctx.Err(); err != nil {
		return monitor.State{}, err
	}
	return s.store.Load()
}

func (s runtimeStore) Save(ctx context.Context, value monitor.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.store.Save(value)
}

type runtimeChannelFactory struct {
	getenv config.Getenv
}

func (f runtimeChannelFactory) Channels(cfg config.Config) ([]management.Channel, error) {
	var channels []management.Channel
	if cfg.SMTP.Enabled {
		count, err := recipientCount(f.env(cfg.SMTP.RecipientsEnv))
		if err != nil {
			return nil, err
		}
		channels = append(channels, management.Channel{
			Name:           "smtp",
			Sender:         notify.NewSMTPSender(cfg.SMTP, f.env),
			RecipientCount: count,
		})
	}
	if cfg.Webhook.Enabled {
		channels = append(channels, management.Channel{
			Name:   "webhook",
			Sender: notify.NewWebhookSender(cfg.Webhook, f.env),
		})
	}
	return channels, nil
}

func (f runtimeChannelFactory) env(name string) string {
	if f.getenv == nil {
		return ""
	}
	return f.getenv(name)
}

func recipientCount(raw string) (int, error) {
	if hasHeaderInjection(raw) {
		return 0, errors.New("invalid recipients")
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	count := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, err := mail.ParseAddress(part)
		if err != nil || addr.Address == "" || hasHeaderInjection(addr.Name) || hasHeaderInjection(addr.Address) {
			return 0, errors.New("invalid recipients")
		}
		count++
	}
	if count == 0 {
		return 0, errors.New("invalid recipients")
	}
	return count, nil
}

func hasHeaderInjection(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}
