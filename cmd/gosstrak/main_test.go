package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	app "github.com/iomz/tagstrak/v2/internal/app/gosstrak"
	configfile "github.com/iomz/tagstrak/v2/internal/config"
)

func noEnv(string) (string, bool) { return "", false }

func env(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := vars[name]; return v, ok }
}

func TestParseConfig(t *testing.T) {
	c, file, err := parseConfig([]string{"start", "--reader", "localhost:5085", "--max-retries", "2", "--read-timeout", "4s"}, io.Discard, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if file != nil {
		t.Fatal("no --config, but a file was returned")
	}
	if c.Reader.Address != "localhost:5085" || c.Reader.MaxRetries != 2 || c.Reader.ReadTimeout != 4*time.Second {
		t.Fatalf("config: %+v", c)
	}
	for _, args := range [][]string{{"--queue-capacity", "0"}, {"--max-frame-bytes", "4294967296"}, {"--max-parameter-bytes", "65536"}, {"--max-parameters", "-1"}, {"--handshake-timeout", "0"}, {"--reader", "invalid"}, {"--max-retries", "-1"}, {"unexpected"}} {
		if _, _, err := parseConfig(args, io.Discard, noEnv); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gosstrak.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPrecedence checks #2's order: defaults, file, environment, flags.
func TestPrecedence(t *testing.T) {
	path := writeConfig(t, `version = 1
[reader]
address = "file:5084"
source = "from-file"
read_timeout = "7s"
max_retries = 3
`)
	c, file, err := parseConfig([]string{"--config", path, "--max-retries", "9"}, io.Discard, env(map[string]string{
		"GOSSTRAK_SOURCE":      "from-env",
		"GOSSTRAK_MAX_RETRIES": "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if file == nil || file.Version != configfile.Version {
		t.Fatalf("file %+v", file)
	}
	for name, got := range map[string]any{
		"file over default":   c.Reader.Address == "file:5084" && c.Reader.ReadTimeout == 7*time.Second,
		"env over file":       c.Reader.Source == "from-env",
		"flag over env":       c.Reader.MaxRetries == 9,
		"default where unset": c.ShutdownTimeout == 10*time.Second,
	} {
		if got != true {
			t.Errorf("%s: config %+v", name, c)
		}
	}
}

func TestConfigPathFromEnvironment(t *testing.T) {
	path := writeConfig(t, "version = 1\n[reader]\nsource = \"env-file\"\n")
	c, file, err := parseConfig(nil, io.Discard, env(map[string]string{"GOSSTRAK_CONFIG": path}))
	if err != nil || file == nil || c.Reader.Source != "env-file" {
		t.Fatalf("config %+v, file %v, err %v", c, file, err)
	}
	// An explicit --config wins over the environment.
	other := writeConfig(t, "version = 1\n[reader]\nsource = \"flag-file\"\n")
	c, _, err = parseConfig([]string{"--config", other}, io.Discard, env(map[string]string{"GOSSTRAK_CONFIG": path}))
	if err != nil || c.Reader.Source != "flag-file" {
		t.Fatalf("config %+v, err %v", c, err)
	}
}

func TestInvalidEnvironmentValueNamesTheVariable(t *testing.T) {
	_, _, err := parseConfig(nil, io.Discard, env(map[string]string{"GOSSTRAK_READ_TIMEOUT": "soon"}))
	if err == nil || !strings.Contains(err.Error(), "GOSSTRAK_READ_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidFileFailsStartup(t *testing.T) {
	path := writeConfig(t, "version = 1\n[reader]\nadress = \"typo\"\n")
	if _, _, err := parseConfig([]string{"--config", path}, io.Discard, noEnv); !errors.Is(err, configfile.ErrConfig) {
		t.Fatalf("err = %v", err)
	}
}

// TestMissingSecretFailsStartup checks fail-closed secrets end to end: a
// destination whose header secret is unset must stop the process before it
// connects to anything.
func TestMissingSecretFailsStartup(t *testing.T) {
	path := writeConfig(t, `version = 1
[[destinations]]
name = "erp"
url = "https://erp.example/hooks"
[destinations.headers.Authorization]
env = "ERP_TOKEN"
prefix = "Bearer "
[[subscriptions]]
id = "product"
patterns = ["urn:epc:idpat:sgtin:0614141.812345.*"]
destinations = ["erp"]
`)
	err := run(context.Background(), []string{"--config", path, "--telemetry", "127.0.0.1:0"}, io.Discard, noEnv)
	if !errors.Is(err, configfile.ErrSecret) || !strings.Contains(err.Error(), "destinations[0].headers.Authorization") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, nil, io.Discard, noEnv); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestRunWithConfiguredDelivery starts the full pipeline from a file and
// stops it again: delivery is wired, starts and drains with nothing queued.
func TestRunWithConfiguredDelivery(t *testing.T) {
	path := writeConfig(t, `version = 1
[telemetry]
address = "127.0.0.1:0"
[runtime]
shutdown_timeout = "1s"
[[destinations]]
name = "erp"
url = "https://erp.example/hooks"
[destinations.headers.Authorization]
env = "ERP_TOKEN"
prefix = "Bearer "
[[subscriptions]]
id = "product"
patterns = ["urn:epc:idpat:sgtin:0614141.812345.*"]
destinations = ["erp"]
`)
	// Cancel as a signal would; main treats exactly ctx.Err() as success.
	ctx, cancel := context.WithCancel(context.Background())
	stop := time.AfterFunc(100*time.Millisecond, cancel)
	defer stop.Stop()
	err := run(ctx, []string{"--config", path, "--reader", "127.0.0.1:1"}, io.Discard, env(map[string]string{"ERP_TOKEN": "t0ken"}))
	// Delivery started and drained cleanly, so nothing is reported as
	// undelivered and shutdown counts as successful.
	if err != ctx.Err() {
		t.Fatalf("err = %q, want exactly %v", err, ctx.Err())
	}
}

// TestExampleConfigurationIsValid keeps the shipped example loadable and
// buildable, so documentation cannot drift from the schema.
func TestExampleConfigurationIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "gosstrak", "gosstrak.toml")
	c, file, err := parseConfig([]string{"--config", path}, io.Discard, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Reader.Source != "dock-1" || len(file.Subscriptions) != 2 || len(file.Destinations) != 1 {
		t.Fatalf("config %+v, file %+v", c, file)
	}
	if _, _, err := app.Pipeline(*file, env(map[string]string{"ERP_TOKEN": "t0ken"}), nil); err != nil {
		t.Fatal(err)
	}
}
