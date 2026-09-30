package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aivara-se/dispatcher/internal/config"
)

// template is the smallest routes file this service reads, with the two log
// paths and nothing else left to fill in. Its shape is
// config/routes.example.yaml's.
const template = `listen:
  address: 127.0.0.1
  port: 8645
endpoint_path: /github
repositories:
  - aivara-se/dispatcher
events:
  - issues
log_path: %LOG%
dead_letter_path: %DEAD%
request_timeout: 10s
retry_bound: 3
dedup_ttl: 1h
dedup_bound: 16
routes:
  - name: mimi
    bot: mimi
    profile: mimi
    gateway_route: mimi-queue
    secret: TEST_GITHUB_SECRET
    gateway_secret: TEST_GATEWAY_SECRET
`

// valid is the template with this test's own log paths.
func valid(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := strings.Replace(template, "%LOG%", filepath.Join(dir, "audit.jsonl"), 1)
	return strings.Replace(body, "%DEAD%", filepath.Join(dir, "dead-letter.jsonl"), 1)
}

// write puts a routes file in a fresh directory and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the routes file: %v", err)
	}
	return path
}

// setSecrets sets the two references the template names. Every load of a valid
// body needs them, because a reference that resolves to nothing is a boot
// failure and not a warning.
func setSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_GITHUB_SECRET", "github-value")
	t.Setenv("TEST_GATEWAY_SECRET", "gateway-value")
}

func TestLoad(t *testing.T) {
	setSecrets(t)
	cfg, err := config.Load(write(t, valid(t)))
	if err != nil {
		t.Fatalf("a valid routes file must load: %v", err)
	}
	if got, want := cfg.Listen.Addr(), "127.0.0.1:8645"; got != want {
		t.Errorf("listen address = %q, want %q", got, want)
	}
	if cfg.EndpointPath != "/github" {
		t.Errorf("endpoint path = %q, want /github", cfg.EndpointPath)
	}
	if cfg.RequestTimeout != 10*time.Second {
		t.Errorf("request_timeout = %s, want 10s", cfg.RequestTimeout)
	}
	if cfg.DedupTTL != time.Hour {
		t.Errorf("dedup_ttl = %s, want 1h", cfg.DedupTTL)
	}
	if cfg.DedupBound != 16 || cfg.RetryBound != 3 {
		t.Errorf("bounds = dedup %d, retry %d", cfg.DedupBound, cfg.RetryBound)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Bot != "mimi" || cfg.Routes[0].GatewayRoute != "mimi-queue" {
		t.Fatalf("routes = %+v", cfg.Routes)
	}

	// The reference resolves to the value, and the two hops are two values.
	inbound, err := cfg.Routes[0].Secret()
	if err != nil {
		t.Fatalf("the inbound secret must resolve: %v", err)
	}
	outbound, err := cfg.Routes[0].GatewaySecret()
	if err != nil {
		t.Fatalf("the gateway secret must resolve: %v", err)
	}
	if inbound != "github-value" || outbound != "gateway-value" {
		t.Errorf("the references resolved to the wrong hops: %q and %q", inbound, outbound)
	}
}

// The committed example is the file an operator copies, so it has to be a file
// this service loads: every field named, both references resolvable, one route
// per bot and nothing invented.
func TestExampleRoutesFileLoads(t *testing.T) {
	for _, bot := range config.Bots {
		upper := strings.ToUpper(bot)
		t.Setenv("DISPATCHER_GITHUB_SECRET_"+upper, "example-value")
		t.Setenv("DISPATCHER_GATEWAY_SECRET_"+upper, "example-value")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "config", "routes.example.yaml"))
	if err != nil {
		t.Fatalf("config/routes.example.yaml must load: %v", err)
	}
	if len(cfg.Routes) != len(config.Bots) {
		t.Fatalf("the example has %d route(s); one per bot is %d", len(cfg.Routes), len(config.Bots))
	}
	for i, bot := range config.Bots {
		if cfg.Routes[i].Bot != bot {
			t.Errorf("route %d wakes %q, want %q", i, cfg.Routes[i].Bot, bot)
		}
		if cfg.Routes[i].Profile != bot {
			t.Errorf("route %q posts to profile %q, want %q", cfg.Routes[i].Name, cfg.Routes[i].Profile, bot)
		}
	}
	for _, want := range []string{"issues", "pull_request_review", "check_run"} {
		if !contains(cfg.Events, want) {
			t.Errorf("the example's events do not include %q", want)
		}
	}
}

// A reference resolves to a mode-600 file as well as to an environment variable:
// both are the shapes ADR 006 allows, and the mode is part of allowing it.
func TestLoadResolvesASecretFile(t *testing.T) {
	setSecrets(t)
	secretPath := filepath.Join(t.TempDir(), "github.secret")
	if err := os.WriteFile(secretPath, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(valid(t), "secret: TEST_GITHUB_SECRET", "secret: "+secretPath, 1)
	cfg, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("a mode-600 secret file is a reference ADR 006 allows: %v", err)
	}
	value, err := cfg.Routes[0].Secret()
	if err != nil {
		t.Fatalf("resolving the file: %v", err)
	}
	if value != "file-value" {
		t.Errorf("the file is read and trimmed, got %q", value)
	}
}

// Every one of these is a configuration fault that must stop the process at
// boot rather than at the first event
// (docs/adrs/006-configuration-and-secrets.md, docs/SYSTEMS.md section 8).
func TestLoadRefuses(t *testing.T) {
	cases := []struct {
		name string
		body func(t *testing.T) string
		says string
	}{
		{
			name: "a bot outside the fleet",
			body: func(t *testing.T) string { return strings.Replace(valid(t), "bot: mimi", "bot: miriam", 1) },
			says: "not one of",
		},
		{
			name: "a duplicate route name",
			body: func(t *testing.T) string {
				second := "  - name: mimi\n    bot: mama\n    profile: mama\n    gateway_route: mama-queue\n    secret: TEST_GITHUB_SECRET\n    gateway_secret: TEST_GATEWAY_SECRET\n"
				return valid(t) + second
			},
			says: "unique",
		},
		{
			name: "a secret reference that resolves to nothing",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "secret: TEST_GITHUB_SECRET", "secret: TEST_SECRET_THAT_IS_NOT_SET", 1)
			},
			says: "neither set in the environment nor a path",
		},
		{
			name: "a world-readable secret file",
			body: func(t *testing.T) string {
				secretPath := filepath.Join(t.TempDir(), "github.secret")
				if err := os.WriteFile(secretPath, []byte("file-value\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return strings.Replace(valid(t), "secret: TEST_GITHUB_SECRET", "secret: "+secretPath, 1)
			},
			says: "mode 600",
		},
		{
			name: "no gateway secret",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "\n    gateway_secret: TEST_GATEWAY_SECRET", "", 1)
			},
			says: "gateway_secret",
		},
		{
			name: "no port",
			body: func(t *testing.T) string { return strings.Replace(valid(t), "port: 8645", "port: 0", 1) },
			says: "not a port",
		},
		{
			name: "no endpoint path",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "endpoint_path: /github", `endpoint_path: github`, 1)
			},
			says: "starts with /",
		},
		{
			name: "an empty allowlist",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "repositories:\n  - aivara-se/dispatcher", "repositories: []", 1)
			},
			says: "nothing would be accepted",
		},
		{
			name: "no log path",
			body: func(t *testing.T) string {
				body := valid(t)
				start := strings.Index(body, "log_path: ")
				end := strings.Index(body[start:], "\n") + start
				return body[:start] + `log_path: ""` + body[end:]
			},
			says: "log_path",
		},
		{
			name: "no routes",
			body: func(t *testing.T) string {
				body := valid(t)
				return body[:strings.Index(body, "routes:")] + "routes: []\n"
			},
			says: "no webhook would be answered",
		},
		{
			name: "a field that is not in the file's shape",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "endpoint_path: /github", "endpoint_path: /github\nlisten_port: 9999", 1)
			},
			says: "not the YAML this service reads",
		},
		{
			name: "a duration that is not one",
			body: func(t *testing.T) string {
				return strings.Replace(valid(t), "request_timeout: 10s", "request_timeout: soon", 1)
			},
			says: "not the YAML this service reads",
		},
		{
			name: "a malformed file",
			body: func(t *testing.T) string { return "listen: [unclosed\nendpoint_path: /github\n" },
			says: "not the YAML this service reads",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setSecrets(t)
			_, err := config.Load(write(t, tc.body(t)))
			if err == nil {
				t.Fatalf("%s must be refused at boot", tc.name)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error should say %q, got: %v", tc.says, err)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
