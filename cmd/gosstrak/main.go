// Command gosstrak runs the v2 reader ingestion runtime.
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
	"syscall"

	app "github.com/iomz/tagstrak/v2/internal/app/gosstrak"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// parseConfig parses command-line arguments into an application configuration,
// applying defaults and validating the resulting limits and settings. An optional
// "start" subcommand is accepted and ignored. It returns a parsing or validation
// error when the arguments or configuration are invalid.
func parseConfig(args []string, output io.Writer) (app.Config, error) {
	config := app.DefaultConfig()
	flags := flag.NewFlagSet("gosstrak", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&config.Reader.Address, "reader", config.Reader.Address, "LLRP reader host:port")
	flags.StringVar(&config.Reader.Source, "source", config.Reader.Source, "Observation source identifier")
	flags.StringVar(&config.TelemetryAddress, "telemetry", config.TelemetryAddress, "Read-only health and metrics host:port")
	flags.IntVar(&config.QueueCapacity, "queue-capacity", config.QueueCapacity, "Maximum queued observations; overflow fails the run")
	flags.DurationVar(&config.Reader.ConnectTimeout, "connect-timeout", config.Reader.ConnectTimeout, "Per-attempt dial timeout")
	flags.DurationVar(&config.Reader.HandshakeTimeout, "handshake-timeout", config.Reader.HandshakeTimeout, "Total connection handshake timeout")
	flags.DurationVar(&config.Reader.ReadTimeout, "read-timeout", config.Reader.ReadTimeout, "Maximum time to read one complete frame")
	flags.DurationVar(&config.Reader.WriteTimeout, "write-timeout", config.Reader.WriteTimeout, "Maximum time to write one complete frame")
	flags.DurationVar(&config.Reader.RetryMin, "retry-min", config.Reader.RetryMin, "Initial reconnect delay")
	flags.DurationVar(&config.Reader.RetryMax, "retry-max", config.Reader.RetryMax, "Maximum reconnect delay")
	flags.IntVar(&config.Reader.MaxRetries, "max-retries", config.Reader.MaxRetries, "Total reconnect budget for this process run")
	flags.DurationVar(&config.ConsumeTimeout, "consume-timeout", config.ConsumeTimeout, "Per-observation processing timeout")
	flags.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", config.ShutdownTimeout, "Queue drain and HTTP shutdown timeout")
	maxFrame := uint(config.Reader.Limits.MaxFrameSize)
	maxParameter := uint(config.Reader.Limits.MaxParameterSize)
	flags.UintVar(&maxFrame, "max-frame-bytes", maxFrame, "Maximum LLRP frame size")
	flags.UintVar(&maxParameter, "max-parameter-bytes", maxParameter, "Maximum LLRP parameter size")
	flags.IntVar(&config.Reader.Limits.MaxParameters, "max-parameters", config.Reader.Limits.MaxParameters, "Maximum parameters per codec container")
	if len(args) > 0 && args[0] == "start" {
		args = args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return config, err
	}
	if flags.NArg() != 0 {
		return config, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if maxFrame > uint(^uint32(0)) || maxParameter > uint(^uint16(0)) {
		return config, fmt.Errorf("codec limit exceeds wire size")
	}
	config.Reader.Limits.MaxFrameSize = uint32(maxFrame)
	config.Reader.Limits.MaxParameterSize = uint16(maxParameter)
	return config, config.Validate()
}

// run parses the command-line arguments, initializes the ingestion runtime, and runs it with the supplied context.
// It returns any configuration, initialization, or runtime error encountered.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	config, err := parseConfig(args, stderr)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	// Tags without a protocol-control word are assumed to be GS1 EPC data.
	// That assumption is explicit rather than inferred from the tag itself,
	// and readers that report the PC word are believed instead.
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	// Subscriptions start empty: their configuration belongs to issue #10.
	// Matching runs regardless, so translated tags and their matches are
	// logged at debug level through the same path delivery will use.
	processor, err := app.NewMatchingProcessor(&filter.Set{}, logger)
	if err != nil {
		return err
	}
	consumer := app.NewTranslatingConsumer(translator, processor)
	runtime, err := app.New(config, &net.Dialer{}, consumer, logger)
	if err != nil {
		return err
	}
	source := tdt.SourceInfo()
	logger.Info("starting gosstrak", "mode", "ingestion", "reader", config.Reader.Address, "telemetry", config.TelemetryAddress,
		"tds_version", source.TDSVersion, "tdt_artefacts", source.Commit, "schemes", tdt.SupportedSchemes())
	return runtime.Run(ctx)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		// A normal signal is successful only when shutdown did not also fail.
		if ctx.Err() != nil && err == ctx.Err() {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
