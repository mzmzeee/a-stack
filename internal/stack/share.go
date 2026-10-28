package stack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const SharedListen = "127.0.0.1:41677"
const shareSettingsFile = "share.json"
const shareOwnerFile = "tailscale-serve.json"

type shareSettings struct {
	Enabled bool `json:"enabled"`
}
type serveOwner struct {
	Host string `json:"host"`
}

// SaveShareSetting is persistent so all three service managers use the same
// startup path. A running service needs a restart to apply an enabled setting.
func SaveShareSetting(dir string, enabled bool) error {
	if _, err := statePath(dir); err != nil {
		return err
	}
	if err := MkdirPrivate(dir); err != nil {
		return err
	}
	if err := checkPrivateDirectory(dir); err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, shareSettingsFile), shareSettings{enabled}, true)
}

// DisableShare removes only an unchanged rule recorded by this state directory.
func DisableShare(ctx context.Context, dir string) error {
	s := &Service{lock: &stateLock{dir: dir}}
	_, err := s.ConfigureShare(ctx, false)
	return err
}

func ShareEnabled(dir string) (bool, error) {
	path := filepath.Join(dir, shareSettingsFile)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	}
	b, err := privateRead(path)
	if err != nil {
		return false, err
	}
	var settings shareSettings
	if decodeJSON(b, &settings) != nil {
		return false, failure("invalid_input", "invalid share settings")
	}
	return settings.Enabled, nil
}

func tailscale(ctx context.Context, args ...string) ([]byte, error) {
	exe, err := exec.LookPath("tailscale")
	if err != nil {
		return nil, failure("integration_unavailable", "tailscale is missing; install Tailscale and sign in before using --share")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Never echo subprocess output: it may contain login URLs or private names.
	out, err := exec.CommandContext(ctx, exe, args...).Output()
	if err != nil {
		return nil, failure("integration_unavailable", "tailscale command failed; check that tailscaled is running, you are signed in, and HTTPS/Serve is enabled for the tailnet")
	}
	if len(out) > 1<<20 {
		return nil, failure("integration_unavailable", "tailscale response is too large")
	}
	return out, nil
}

type serveConfig struct {
	TCP         map[string]map[string]any
	Web         map[string]struct{ Handlers map[string]map[string]any }
	AllowFunnel map[string]bool
	Foreground  map[string]json.RawMessage
}

func readServeConfig(ctx context.Context) (serveConfig, error) {
	var c serveConfig
	b, err := tailscale(ctx, "serve", "status", "--json")
	if err != nil {
		return c, err
	}
	if json.Unmarshal(b, &c) != nil {
		return c, failure("integration_unavailable", "invalid Tailscale Serve status")
	}
	return c, nil
}
func (c serveConfig) empty443() bool {
	if len(c.Foreground) > 0 || c.TCP["443"] != nil {
		return false
	}
	for host := range c.Web {
		if strings.HasSuffix(host, ":443") {
			return false
		}
	}
	for host, on := range c.AllowFunnel {
		if on && strings.HasSuffix(host, ":443") {
			return false
		}
	}
	return true
}
func (c serveConfig) owns443(host string) bool {
	if len(c.Foreground) > 0 || !reflect.DeepEqual(c.TCP["443"], map[string]any{"HTTPS": true}) || c.AllowFunnel[host+":443"] {
		return false
	}
	for h := range c.Web {
		if strings.HasSuffix(h, ":443") && h != host+":443" {
			return false
		}
	}
	handlers := c.Web[host+":443"].Handlers
	return len(handlers) == 1 && reflect.DeepEqual(handlers["/"], map[string]any{"Proxy": "http://" + SharedListen})
}

// ConfigureShare runs only after the loopback listener is live. The ownership
// record and exact status check prevent deletion of somebody else's Serve rule.
// Other ports are left alone; no reset or Funnel command is ever used.
func (s *Service) ConfigureShare(ctx context.Context, enabled bool) (string, error) {
	dir := s.lock.dir
	path := filepath.Join(dir, shareOwnerFile)
	var owner serveOwner
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		b, err := privateRead(path)
		if err != nil {
			return "", err
		}
		if decodeJSON(b, &owner) != nil || !serveName.MatchString(owner.Host) {
			return "", failure("invalid_input", "invalid Tailscale Serve ownership record")
		}
	}
	if !enabled && owner.Host == "" {
		return "", nil
	}
	c, err := readServeConfig(ctx)
	if err != nil {
		return "", err
	}
	if !enabled {
		if !c.empty443() {
			if !c.owns443(owner.Host) {
				return "", failure("conflict", "Tailscale Serve changed outside a-stack; refusing to remove its HTTPS configuration")
			}
			if _, err = tailscale(ctx, "serve", "--https=443", "off"); err != nil {
				return "", err
			}
		}
		if err = os.Remove(path); err != nil {
			return "", storageError(err)
		}
		_ = s.SetServeHost("")
		return "", syncDir(dir)
	}
	if s.Discovery.URL != "http://"+SharedListen {
		return "", failure("invalid_input", "--share requires --listen "+SharedListen)
	}
	b, err := tailscale(ctx, "status", "--json")
	if err != nil {
		return "", err
	}
	var status struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if json.Unmarshal(b, &status) != nil || status.BackendState != "Running" {
		return "", failure("integration_unavailable", "Tailscale is logged out or not running; sign in before using --share")
	}
	host := strings.TrimSuffix(status.Self.DNSName, ".")
	if !serveName.MatchString(host) {
		return "", failure("integration_unavailable", "Tailscale has no MagicDNS ts.net name; enable MagicDNS and HTTPS for the tailnet")
	}
	if owner.Host != "" && owner.Host != host {
		return "", failure("conflict", "Tailscale node name changed; disable the old a-stack share before enabling a new one")
	}
	if !c.empty443() && (owner.Host == "" || !c.owns443(host)) {
		return "", failure("conflict", "Tailscale HTTPS port 443 is already configured; a-stack will not replace foreign Serve or Funnel rules")
	}
	if c.empty443() {
		// Record intent before the external side effect, so an interrupted setup can
		// be retried or removed without adopting a pre-existing rule.
		if err = writePrivate(path, serveOwner{host}, true); err != nil {
			return "", err
		}
		if _, err = tailscale(ctx, "serve", "--bg", "--https=443", "http://"+SharedListen); err != nil {
			return "", err
		}
		c, err = readServeConfig(ctx)
		if err != nil {
			return "", err
		}
		if !c.owns443(host) {
			return "", failure("conflict", "Tailscale Serve configuration did not match a-stack; HTTPS access remains disabled")
		}
	}
	if err = s.SetServeHost(host); err != nil {
		return "", err
	}
	return "https://" + host, nil
}
