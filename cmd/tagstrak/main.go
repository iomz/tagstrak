// Command tagstrak runs the v2 reader ingestion runtime.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	app "github.com/iomz/tagstrak/v2/internal/app/tagstrak"
	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// envPrefix names the environment layer: flag --x-y is TAGSTRAK_X_Y.
const envPrefix = "TAGSTRAK_"

// envName derives a flag's environment variable, so the flag set is the one
// list of scalar settings and the two layers cannot drift.
func envName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// settings holds what the flag set binds to. Codec limits are bound as uint
// and checked against their wire sizes after parsing.
type settings struct {
	config       app.Config
	configPath   string
	maxFrame     uint
	maxParameter uint
}

// bind defines every flag on fs with s's current values as defaults.
func bind(fs *flag.FlagSet, s *settings) {
	c := &s.config
	fs.StringVar(&s.configPath, "config", s.configPath, "Configuration file (.toml or .json); subscriptions and destinations come only from here")
	fs.StringVar(&c.Reader.Address, "reader", c.Reader.Address, "LLRP reader host:port")
	fs.StringVar(&c.Reader.Source, "source", c.Reader.Source, "Observation source identifier")
	fs.StringVar(&c.TelemetryAddress, "telemetry", c.TelemetryAddress, "Read-only health and metrics host:port")
	fs.StringVar(&c.Control.Socket, "control-socket", c.Control.Socket, "Management API Unix socket in a private directory (same-user trust)")
	fs.StringVar(&c.Control.Address, "control-address", c.Control.Address, "Management API loopback host:port; requires control.token in the file")
	fs.IntVar(&c.QueueCapacity, "queue-capacity", c.QueueCapacity, "Maximum queued observations; overflow fails the run")
	fs.DurationVar(&c.Reader.ConnectTimeout, "connect-timeout", c.Reader.ConnectTimeout, "Per-attempt dial timeout")
	fs.DurationVar(&c.Reader.HandshakeTimeout, "handshake-timeout", c.Reader.HandshakeTimeout, "Total connection handshake timeout")
	fs.DurationVar(&c.Reader.ReadTimeout, "read-timeout", c.Reader.ReadTimeout, "Maximum time to read one complete frame")
	fs.DurationVar(&c.Reader.WriteTimeout, "write-timeout", c.Reader.WriteTimeout, "Maximum time to write one complete frame")
	fs.DurationVar(&c.Reader.RetryMin, "retry-min", c.Reader.RetryMin, "Initial reconnect delay")
	fs.DurationVar(&c.Reader.RetryMax, "retry-max", c.Reader.RetryMax, "Maximum reconnect delay")
	fs.IntVar(&c.Reader.MaxRetries, "max-retries", c.Reader.MaxRetries, "Total reconnect budget for this process run")
	fs.DurationVar(&c.ConsumeTimeout, "consume-timeout", c.ConsumeTimeout, "Per-observation processing timeout")
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", c.ShutdownTimeout, "Queue drain, report drain and HTTP shutdown timeout")
	s.maxFrame = uint(c.Reader.Limits.MaxFrameSize)
	s.maxParameter = uint(c.Reader.Limits.MaxParameterSize)
	fs.UintVar(&s.maxFrame, "max-frame-bytes", s.maxFrame, "Maximum LLRP frame size")
	fs.UintVar(&s.maxParameter, "max-parameter-bytes", s.maxParameter, "Maximum LLRP parameter size")
	fs.IntVar(&c.Reader.Limits.MaxParameters, "max-parameters", c.Reader.Limits.MaxParameters, "Maximum parameters per codec container")
}

func newFlagSet(output io.Writer, s *settings) *flag.FlagSet {
	fs := flag.NewFlagSet("tagstrak", flag.ContinueOnError)
	fs.SetOutput(output)
	bind(fs, s)
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: tagstrak [start] [flags]\n\nSettings apply in order: defaults, --config file, %s<FLAG> environment variables, flags.\n\n", envPrefix)
		fs.PrintDefaults()
	}
	return fs
}

// loadedFile is a decoded configuration file and the path it came from, which
// SIGHUP reloads.
type loadedFile struct {
	configfile.File
	Path string
}

// parseConfig builds one immutable configuration from defaults, the optional
// configuration file, the environment, and flags, in that order (#2). An
// optional "start" subcommand is accepted and ignored. It returns the decoded
// file, or nil without --config, for the pipeline's structured settings.
func parseConfig(args []string, output io.Writer, lookup func(string) (string, bool)) (app.Config, *loadedFile, error) {
	if len(args) > 0 && args[0] == "start" {
		args = args[1:]
	}
	// First pass: learn which flags were given explicitly, and the file.
	first := settings{config: app.DefaultConfig()}
	fs := newFlagSet(output, &first)
	if err := fs.Parse(args); err != nil {
		return app.Config{}, nil, err
	}
	if fs.NArg() != 0 {
		return app.Config{}, nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	explicit := map[string]string{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = f.Value.String() })
	path := first.configPath
	if _, ok := explicit["config"]; !ok {
		if v, ok := lookup(envName("config")); ok {
			path = v
		}
	}

	// Second pass: rebuild on top of the file, then the environment, then
	// the explicit flags.
	layered := settings{config: app.DefaultConfig(), configPath: path}
	var file *loadedFile
	if path != "" {
		f, err := configfile.Load(path)
		if err != nil {
			return app.Config{}, nil, err
		}
		file = &loadedFile{File: f, Path: path}
		layered.config = app.ApplyFile(layered.config, f)
	}
	fs = newFlagSet(io.Discard, &layered)
	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		if _, ok := explicit[f.Name]; ok || f.Name == "config" || envErr != nil {
			return
		}
		if v, ok := lookup(envName(f.Name)); ok {
			if err := fs.Set(f.Name, v); err != nil {
				envErr = fmt.Errorf("environment %s: %w", envName(f.Name), err)
			}
		}
	})
	if envErr != nil {
		return app.Config{}, nil, envErr
	}
	for name, value := range explicit {
		if err := fs.Set(name, value); err != nil {
			return app.Config{}, nil, fmt.Errorf("flag --%s: %w", name, err)
		}
	}

	if layered.maxFrame > uint(^uint32(0)) || layered.maxParameter > uint(^uint16(0)) {
		return app.Config{}, nil, fmt.Errorf("codec limit exceeds wire size")
	}
	c := layered.config
	c.Reader.Limits.MaxFrameSize = uint32(layered.maxFrame)
	c.Reader.Limits.MaxParameterSize = uint16(layered.maxParameter)
	return c, file, c.Validate()
}

// Flag output and structured application logs are written to stderr.
func run(ctx context.Context, args []string, stderr io.Writer, lookup func(string) (string, bool)) error {
	config, file, err := parseConfig(args, stderr, lookup)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if file == nil {
		// Without a file there are no subscriptions or destinations: tags are
		// translated, matched against nothing, and logged at debug level.
		file = &loadedFile{File: configfile.File{Version: configfile.Version}}
	}
	assembly, err := app.Pipeline(file.File, config, lookup, logger)
	if err != nil {
		return err
	}
	runtime, err := app.New(config, &net.Dialer{}, assembly.Consumer, logger, assembly.Options...)
	if err != nil {
		return err
	}
	if file.Path != "" && assembly.Source == configfile.SourceFile {
		stop := reloadOnHangup(ctx, file.Path, assembly.Subscriptions, logger)
		defer stop()
	}
	source := tdt.SourceInfo()
	logger.Info("starting tagstrak", "mode", "ingestion", "reader", config.Reader.Address, "telemetry", config.TelemetryAddress,
		"subscriptions", len(file.Subscriptions), "subscription_source", assembly.Source, "destinations", len(file.Destinations),
		"control", config.Control.Enabled(),
		"tds_version", source.TDSVersion, "tdt_artefacts", source.Commit, "schemes", tdt.SupportedSchemes())
	return runtime.Run(ctx)
}

// reloadOnHangup reloads subscriptions from path on every SIGHUP until the
// returned stop function runs, which also joins the reload goroutine.
func reloadOnHangup(ctx context.Context, path string, table *app.SubscriptionTable, logger *slog.Logger) (stop func()) {
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hangup:
				if err := app.ReloadFile(ctx, path, table, logger); err != nil {
					logger.Warn("subscription reload rejected; keeping current subscriptions", "error", err)
				}
			}
		}
	}()
	return func() {
		signal.Stop(hangup)
		cancel()
		<-done
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr, os.LookupEnv); err != nil && !errors.Is(err, flag.ErrHelp) {
		// A normal signal is successful only when shutdown did not also fail.
		if ctx.Err() != nil && err == ctx.Err() {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
