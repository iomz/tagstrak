// Command tagstrak-rig is the test rig that comes with tagstrak: it emulates an
// LLRP reader that serves deterministic inventory scenarios, and generates tag
// data.
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

	app "github.com/iomz/tagstrak/v2/internal/app/rig"
	"github.com/iomz/tagstrak/v2/internal/emulator"
	"github.com/iomz/tagstrak/v2/internal/inventory"
)

const usage = `Usage: tagstrak-rig reader --scenario FILE [flags]
       tagstrak-rig reader --inventory FILE [--shuffle] [--seed N] [flags]
       tagstrak-rig uii epc|iso17360|iso17363 [flags]`

// parse converts reader arguments into a validated application configuration
// and immutable scenario. It writes help and flag diagnostics to output and
// returns flag.ErrHelp when no subcommand or an explicit help flag is given.
func parse(args []string, output io.Writer) (app.Config, *emulator.Scenario, error) {
	config := app.DefaultConfig()
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(output, usage)
		return config, nil, flag.ErrHelp
	}
	if args[0] != "reader" {
		return config, nil, fmt.Errorf("unknown subcommand %q", args[0])
	}
	flags := flag.NewFlagSet("tagstrak-rig reader", flag.ContinueOnError)
	flags.SetOutput(output)
	var scenarioPath, inventoryPath string
	var seed int64
	var shuffle bool
	flags.StringVar(&scenarioPath, "scenario", "", "Version-1 scenario JSON file to play")
	flags.StringVar(&inventoryPath, "inventory", "", "Version-1 inventory JSON file to repeat as one cycle")
	flags.Int64Var(&seed, "seed", 0, "Shuffle seed for --inventory")
	flags.BoolVar(&shuffle, "shuffle", false, "Shuffle each --inventory cycle using the seed")
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
	if flags.NArg() != 0 {
		return config, nil, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if (scenarioPath == "") == (inventoryPath == "") {
		return config, nil, errors.New("exactly one of --scenario or --inventory is required")
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if scenarioPath != "" && (set["seed"] || set["shuffle"]) {
		return config, nil, errors.New("--seed and --shuffle apply only to --inventory; a scenario sets its own")
	}
	input := scenarioPath + inventoryPath
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
	if inventoryPath != "" {
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

// run parses the command line, initializes the emulator, and runs it with ctx,
// or runs the uii subcommand. Generated tags are written to stdout; flag output
// and structured application logs to stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "uii" {
		return uii(args[1:], stdout, stderr)
	}
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
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		if ctx.Err() != nil && err == ctx.Err() {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
