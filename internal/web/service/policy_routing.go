package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/policyrouting"
)

const policyRoutingSetting = "nativePolicyRouting"

var (
	nativePolicyManager = policyrouting.NewManager()
	nativePolicyMu      sync.Mutex
)

type PolicyRoutingService struct{}

type PolicyRoutingView struct {
	EnabledByEnv bool                 `json:"enabledByEnv"`
	Config       policyrouting.Config `json:"config"`
	State        policyrouting.State  `json:"state"`
}

func PolicyRoutingEnabled() bool { return os.Getenv("XUI_ENABLE_POLICY_ROUTING") == "true" }

func (s *PolicyRoutingService) config() (policyrouting.Config, error) {
	setting, err := (&SettingService{}).getSetting(policyRoutingSetting)
	if database.IsNotFound(err) {
		return policyrouting.DefaultConfig(), nil
	}
	if err != nil {
		return policyrouting.Config{}, err
	}
	cfg := policyrouting.DefaultConfig()
	if err := json.Unmarshal([]byte(setting.Value), &cfg); err != nil {
		return cfg, err
	}
	if cfg.Rules == nil {
		cfg.Rules = []policyrouting.Rule{}
	}
	return cfg, cfg.Validate()
}

func (s *PolicyRoutingService) View(ctx context.Context) (PolicyRoutingView, error) {
	nativePolicyMu.Lock()
	defer nativePolicyMu.Unlock()
	cfg, err := s.config()
	if err != nil {
		return PolicyRoutingView{}, err
	}
	v := PolicyRoutingView{EnabledByEnv: PolicyRoutingEnabled(), Config: cfg}
	if v.EnabledByEnv {
		v.State = nativePolicyManager.Status(ctx)
	} else {
		v.State.Interfaces = []string{}
		v.State.Error = "Set XUI_ENABLE_POLICY_ROUTING=true to manage the container's Linux routing"
	}
	return v, nil
}

func (s *PolicyRoutingService) Apply(ctx context.Context, cfg policyrouting.Config) error {
	if !PolicyRoutingEnabled() {
		return fmt.Errorf("native policy routing is disabled; set XUI_ENABLE_POLICY_ROUTING=true")
	}
	if cfg.Rules == nil {
		cfg.Rules = []policyrouting.Rule{}
	}
	nativePolicyMu.Lock()
	defer nativePolicyMu.Unlock()
	return nativePolicyManager.Update(ctx, cfg, s.config, func(c policyrouting.Config) error {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return (&SettingService{}).saveSetting(policyRoutingSetting, string(data))
	})
}

// Reconcile never writes settings; only an explicit Apply saves configuration.
func (s *PolicyRoutingService) Reconcile(ctx context.Context) error {
	if !PolicyRoutingEnabled() {
		return nil
	}
	nativePolicyMu.Lock()
	defer nativePolicyMu.Unlock()
	cfg, err := s.config()
	if err != nil {
		return err
	}
	return nativePolicyManager.Reconcile(ctx, cfg)
}
