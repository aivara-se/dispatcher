// Package config loads the routes file: what the service accepts, where it
// writes, and the names of the secrets it verifies with — never their values.
//
// The file, its fields, and the reasons a bad one must stop the process at boot
// rather than at the first event, are in docs/adrs/006-configuration-and-secrets.md
// and docs/SYSTEMS.md section 8.
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Bots is the fleet the service routes over — the four profiles and logins the
// queue configuration already names (docs/SYSTEMS.md section 4). A route naming
// anything else is a configuration fault rather than a fifth bot: a bot the
// router cannot place is a wake that would go nowhere.
//
// Card #8 moves this list to the fleet's own configuration, where a new bot is
// an entry and not a change here. Until then it is the list this file validates
// against.
var Bots = []string{"mama", "meme", "mimi", "momo"}

// Config is the routes file.
type Config struct {
	Listen         Listen        `yaml:"listen"`
	EndpointPath   string        `yaml:"endpoint_path"`
	Repositories   []string      `yaml:"repositories"`
	Events         []string      `yaml:"events"`
	LogPath        string        `yaml:"log_path"`
	DeadLetterPath string        `yaml:"dead_letter_path"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	RetryBound     int           `yaml:"retry_bound"`
	DedupTTL       time.Duration `yaml:"dedup_ttl"`
	DedupBound     int           `yaml:"dedup_bound"`
	Routes         []Route       `yaml:"routes"`
}

// Listen is the loopback address the receiver binds. TLS terminates in front of
// it, not in it (docs/SYSTEMS.md section 10).
type Listen struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

// Addr is what net.Listen takes.
func (l Listen) Addr() string {
	return net.JoinHostPort(l.Address, strconv.Itoa(l.Port))
}

// Route is one inbound webhook: the bot an event on it wakes, and the gateway
// route the wake is posted to.
//
// It names two secrets and holds neither. SecretRef is what GitHub signs this
// webhook with, which the receiver verifies with; GatewaySecretRef is what the
// bot's own route on the gateway holds, which the wake is signed with. They are
// different values for different hops, so one leak is not a credential on both
// sides (docs/SYSTEMS.md section 8).
type Route struct {
	Name             string `yaml:"name"`
	Bot              string `yaml:"bot"`
	Profile          string `yaml:"profile"`
	GatewayRoute     string `yaml:"gateway_route"`
	SecretRef        string `yaml:"secret"`
	GatewaySecretRef string `yaml:"gateway_secret"`
}

// Secret resolves the inbound reference to its value: what this route's webhook
// is signed with. The value is never logged, never named in an error, and never
// written anywhere — callers get it, verify with it and drop it
// (docs/adrs/006-configuration-and-secrets.md).
func (r Route) Secret() (string, error) {
	return ResolveSecret(r.SecretRef)
}

// GatewaySecret resolves the outbound reference: what the bot's gateway route
// holds, which the wake is signed with.
func (r Route) GatewaySecret() (string, error) {
	return ResolveSecret(r.GatewaySecretRef)
}

// ResolveSecret reads a reference: an environment variable name, or a path to a
// file whose mode grants nothing to group and other. Both shapes are the two
// the decision record allows; anything else is not a reference at all.
func ResolveSecret(ref string) (string, error) {
	if strings.ContainsRune(ref, os.PathSeparator) {
		info, err := os.Stat(ref)
		if err != nil {
			return "", fmt.Errorf("secret file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("secret %s is not a regular file", ref)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return "", fmt.Errorf("secret file %s is mode %04o: ADR 006 allows mode 600, owned by the service user", ref, perm)
		}
		raw, err := os.ReadFile(ref)
		if err != nil {
			return "", fmt.Errorf("secret file: %w", err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return "", fmt.Errorf("secret file %s is empty", ref)
		}
		return value, nil
	}
	value, ok := os.LookupEnv(ref)
	if !ok || value == "" {
		return "", fmt.Errorf("secret %s is neither set in the environment nor a path", ref)
	}
	return value, nil
}

// Load reads the routes file and refuses a bad one.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("routes file: %w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("routes file %s is not the YAML this service reads: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("routes file %s: %w", path, err)
	}
	return &cfg, nil
}

// validate is the boot-time gate of docs/adrs/006-configuration-and-secrets.md:
// an unknown bot, a duplicate route name, or a secret reference that resolves to
// nothing must fail here, loudly, rather than at the first event.
func (c *Config) validate() error {
	if c.Listen.Address == "" {
		return fmt.Errorf("listen.address is empty")
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen.port is %d, which is not a port", c.Listen.Port)
	}
	if !strings.HasPrefix(c.EndpointPath, "/") {
		return fmt.Errorf("endpoint_path is %q: it is a path, so it starts with /", c.EndpointPath)
	}
	if len(c.Repositories) == 0 {
		return fmt.Errorf("repositories is empty: nothing would be accepted")
	}
	if len(c.Events) == 0 {
		return fmt.Errorf("events is empty: nothing would be accepted")
	}
	if c.LogPath == "" {
		return fmt.Errorf("log_path is empty: every delivery is audited")
	}
	if c.DeadLetterPath == "" {
		return fmt.Errorf("dead_letter_path is empty: a failed wake is never dropped")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request_timeout is %s: it is a positive duration", c.RequestTimeout)
	}
	if c.RetryBound < 0 {
		return fmt.Errorf("retry_bound is %d: retries are bounded, not unbounded", c.RetryBound)
	}
	if c.DedupTTL <= 0 {
		return fmt.Errorf("dedup_ttl is %s: it is a positive duration", c.DedupTTL)
	}
	if c.DedupBound < 1 {
		return fmt.Errorf("dedup_bound is %d: the cache is bounded, so it is at least one", c.DedupBound)
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("routes is empty: no webhook would be answered")
	}
	seen := map[string]bool{}
	for i, route := range c.Routes {
		where := fmt.Sprintf("routes[%d] (%s)", i, route.Name)
		if route.Name == "" {
			return fmt.Errorf("routes[%d] has no name", i)
		}
		if seen[route.Name] {
			return fmt.Errorf("%s: a route name is unique", where)
		}
		seen[route.Name] = true
		if !known(route.Bot) {
			return fmt.Errorf("%s names bot %q, which is not one of %s", where, route.Bot, strings.Join(Bots, ", "))
		}
		if route.Profile == "" {
			return fmt.Errorf("%s has no profile: the wake goes to a profile, not to a bot in the abstract", where)
		}
		if route.GatewayRoute == "" {
			return fmt.Errorf("%s has no gateway_route: the wake is posted to that route", where)
		}
		if route.SecretRef == "" {
			return fmt.Errorf("%s names no secret: a webhook that is not signed is not accepted", where)
		}
		if _, err := route.Secret(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if route.GatewaySecretRef == "" {
			return fmt.Errorf("%s names no gateway_secret: the wake is signed with the bot's route secret, not this route's", where)
		}
		if _, err := route.GatewaySecret(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

func known(bot string) bool {
	for _, b := range Bots {
		if b == bot {
			return true
		}
	}
	return false
}
