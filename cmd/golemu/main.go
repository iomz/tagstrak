// Command golemu serves deterministic v2 LLRP inventory scenarios.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	app "github.com/iomz/tagstrak/v2/internal/app/golemu"
	"github.com/iomz/tagstrak/v2/internal/emulator"
	"github.com/iomz/tagstrak/v2/internal/inventory"
)

// parse converts server or simulator arguments into a validated application
// configuration and immutable scenario. It writes help and flag diagnostics to
// output and returns flag.ErrHelp when no mode or an explicit help flag is given.
func parse(args []string, output io.Writer) (app.Config, *emulator.Scenario, error) {
	config := app.DefaultConfig()
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(output, "Usage: golemu server --inventory FILE [flags]\n       golemu simulator --scenario FILE [flags]")
		return config, nil, flag.ErrHelp
	}
	mode := args[0]
	if mode != "server" && mode != "simulator" {
		return config, nil, fmt.Errorf("unknown mode %q", mode)
	}
	flags := flag.NewFlagSet("golemu "+mode, flag.ContinueOnError)
	flags.SetOutput(output)
	var input string
	var seed int64
	var shuffle bool
	if mode == "server" {
		flags.StringVar(&input, "inventory", "", "Version-1 inventory JSON file (required)")
		flags.Int64Var(&seed, "seed", 0, "Deterministic shuffle seed")
		flags.BoolVar(&shuffle, "shuffle", false, "Shuffle each inventory cycle using the seed")
	} else {
		flags.StringVar(&input, "scenario", "", "Version-1 scenario JSON file (required)")
	}
	flags.StringVar(&config.Emulator.Address, "listen", config.Emulator.Address, "LLRP data-plane host:port")
	flags.StringVar(&config.ControlSocket, "control-socket", "", "Optional Unix control socket in a private directory")
	flags.IntVar(&config.Emulator.MaxClients, "max-clients", config.Emulator.MaxClients, "Maximum concurrent LLRP clients (1-128)")
	flags.DurationVar(&config.Emulator.ReportInterval, "report-interval", config.Emulator.ReportInterval, "Delay after each completed inventory cycle")
	flags.DurationVar(&config.Emulator.KeepaliveInterval, "keepalive-interval", config.Emulator.KeepaliveInterval, "Default interval when peer omits KeepaliveSpec")
	flags.DurationVar(&config.Emulator.AckTimeout, "ack-timeout", config.Emulator.AckTimeout, "Maximum wait for a keepalive ACK")
	flags.DurationVar(&config.Emulator.HandshakeTimeout, "handshake-timeout", config.Emulator.HandshakeTimeout, "Total handshake deadline")
	flags.DurationVar(&config.Emulator.ReadTimeout, "read-timeout", config.Emulator.ReadTimeout, "Deadline for each inbound frame")
	flags.DurationVar(&config.Emulator.WriteTimeout, "write-timeout", config.Emulator.WriteTimeout, "Deadline for each control frame or whole report cycle")
	flags.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", config.ShutdownTimeout, "Control HTTP shutdown deadline")
	frame := uint(config.Emulator.FrameBytes)
	flags.UintVar(&frame, "frame-bytes", frame, "Maximum LLRP frame size (128-1048576)")
	if err := flags.Parse(args[1:]); err != nil {
		return config, nil, err
	}
	if flags.NArg() != 0 || input == "" {
		return config, nil, errors.New("exactly one input file flag is required; positional arguments are not supported")
	}
	if frame > 1<<20 {
		return config, nil, errors.New("frame budget exceeds maximum")
	}
	config.Emulator.FrameBytes = uint32(frame)
	if err := config.Validate(); err != nil {
		return config, nil, err
	}
	info, err := os.Stat(input)
	if err != nil {
		return config, nil, err
	}
	if !info.Mode().IsRegular() {
		return config, nil, errors.New("input must be a regular file")
	}
	if mode == "server" {
		tags, err := inventory.LoadFile(input, inventory.DefaultLimits())
		if err != nil {
			return config, nil, err
		}
		scenario, err := emulator.NewScenario([][]inventory.Tag{tags}, seed, true, shuffle)
		return config, scenario, err
	}
	file, err := os.Open(input)
	if err != nil {
		return config, nil, err
	}
	defer file.Close()
	scenario, err := emulator.DecodeScenario(file)
	return config, scenario, err
}

// run parses the command line, initializes the emulator, and runs it with ctx.
// Flag output and structured application logs are written to stderr.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	config, scenario, err := parse(args, stderr)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	runtime, err := app.New(config, scenario, emulator.SystemClock{}, emulator.SeededRandom, logger)
	if err != nil {
		return err
	}
	return runtime.Run(ctx)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		if ctx.Err() != nil && err == ctx.Err() {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
