//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package connection

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
	log "github.com/sirupsen/logrus"
)

// Simulator implements an LLRP simulator mode that loads tag data from files
// organized by event cycle and sends RO_ACCESS_REPORT messages simulating
// RFID reader behavior over time.
type Simulator struct {
	ip               string
	port             int
	pdu              int
	reportInterval   int
	simulationDir    string
	currentMessageID *uint32
	loopStarted      *atomic.Bool
}

func (s *Simulator) nextMessageID() uint32 {
	return atomic.AddUint32(s.currentMessageID, 1) - 1
}

// NewSimulator creates a new simulator instance with the specified configuration.
//
// Parameters:
//   - ip: IP address to listen on
//   - port: Port number to listen on
//   - pdu: Maximum Protocol Data Unit size in bytes
//   - reportInterval: Interval in milliseconds between RO_ACCESS_REPORT messages
//   - simulationDir: Directory containing JSON inventory files for each event cycle
//   - initialMessageID: Starting message ID for LLRP messages
func NewSimulator(ip string, port, pdu, reportInterval int, simulationDir string, initialMessageID int) *Simulator {
	msgID := uint32(initialMessageID)
	return &Simulator{
		ip:               ip,
		port:             port,
		pdu:              pdu,
		reportInterval:   reportInterval,
		simulationDir:    simulationDir,
		currentMessageID: &msgID,
		loopStarted:      &atomic.Bool{},
	}
}

// Run starts the simulator and begins listening for LLRP connections.
// It loads simulation files from the configured directory, waits for a client connection,
// sends a READER_EVENT_NOTIFICATION, and then processes incoming LLRP messages.
// The simulator cycles through event files, sending tag reports at the configured interval.
//
// Returns 0 on normal shutdown, non-zero on error.
func (s *Simulator) Run() int {
	simulationFiles, err := s.loadSimulationFiles()
	if err != nil {
		log.Fatal(err)
	}

	l, err := net.Listen("tcp", s.ip+":"+strconv.Itoa(s.port))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	log.Infof("listening on %v:%v", s.ip, s.port)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-signals
		log.Infof("received signal %v, shutting down...", sig)
		signal.Stop(signals)
		l.Close()
	}()

	log.Info("waiting for LLRP connection...")
	conn, err := l.Accept()
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return 0
		}
		log.Fatal(err)
	}
	defer conn.Close()
	log.Infof("initiated LLRP connection with %v", conn.RemoteAddr())

	// Send READER_EVENT_NOTIFICATION
	currentTime := uint64(time.Now().UTC().UnixNano() / 1000)
	if err := llrp.WriteMessage(conn, llrp.ReaderEventNotificationMessage(s.nextMessageID(), currentTime), llrp.DefaultLimits()); err != nil {
		log.Fatalf("error sending READER_EVENT_NOTIFICATION: %v", err)
	}
	log.Info("<<< READER_EVENT_NOTIFICATION")

	eventCycle := 0
	roarTicker := time.NewTicker(time.Duration(s.reportInterval) * time.Millisecond)
	defer roarTicker.Stop() // Safety net in case simulation never starts
	var simulationDone chan struct{}

	for {
		// Use a channel to read messages asynchronously so we can select on done signal
		type readResult struct {
			message llrp.Message
			err     error
		}
		readCh := make(chan readResult, 1)
		go func() {
			message, err := llrp.ReadMessage(conn, llrp.DefaultLimits())
			readCh <- readResult{message: message, err: err}
		}()

		select {
		case result := <-readCh:
			if result.err != nil {
				if result.err == io.EOF || errors.Is(result.err, net.ErrClosed) {
					log.Info("connection closed, exiting")
					return 0
				}
				log.Fatalf("error reading LLRP message: %v", result.err)
			}

			if result.message.Header.Type == llrp.SetReaderConfigHeader {
				if err := llrp.WriteMessage(conn, llrp.SetReaderConfigResponseMessage(s.nextMessageID()), llrp.DefaultLimits()); err != nil {
					log.Fatalf("error writing SET_READER_CONFIG_RESPONSE: %v", err)
				}

				if s.loopStarted.CompareAndSwap(false, true) {
					simulationDone = s.startSimulationLoop(conn, simulationFiles, &eventCycle, roarTicker)
				} else {
					log.Warn("simulation loop already running; ignoring duplicate SET_READER_CONFIG")
				}
			} else {
				log.Warnf(">>> header: %v", result.message.Header.Type)
			}
		case <-simulationDone:
			log.Info("simulation loop terminated, exiting read loop")
			return 0
		}
	}
}

func (s *Simulator) loadSimulationFiles() ([]string, error) {
	dir, err := filepath.Abs(s.simulationDir)
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	simulationFiles := []string{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			simulationFiles = append(simulationFiles, path.Join(dir, f.Name()))
		}
	}
	if len(simulationFiles) == 0 {
		return nil, fmt.Errorf("no event cycle file found in %s", s.simulationDir)
	}
	return simulationFiles, nil
}

func (s *Simulator) loadTagsForNextEventCycle(simulationFiles []string, eventCycle *int) ([]inventory.Tag, error) {
	tags := []inventory.Tag{}
	if len(simulationFiles) <= *eventCycle {
		log.Debugf("Total iteration: %v, current event cycle: %v", len(simulationFiles), *eventCycle)
		log.Infof("Resetting event cycle from %v to 0", *eventCycle)
		*eventCycle = 0
	}
	loaded, err := inventory.LoadFile(simulationFiles[*eventCycle], inventory.DefaultLimits())
	if err != nil {
		return tags, err
	}
	return loaded, nil
}

func (s *Simulator) startSimulationLoop(conn net.Conn, simulationFiles []string, eventCycle *int, roarTicker *time.Ticker) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer s.loopStarted.Store(false)
		defer close(done)
		defer roarTicker.Stop()
		for {
			<-roarTicker.C
			tags, err := s.loadTagsForNextEventCycle(simulationFiles, eventCycle)
			if err != nil {
				log.Warn(err)
				continue
			}
			*eventCycle++
			trds, err := buildTagReportDataStack(tags, s.pdu)
			if err != nil {
				log.Warn(err)
				continue
			}

			log.Infof("<<< Simulated Event Cycle %v, %v tags, %v roars", *eventCycle-1, len(tags), len(trds))
			for _, trd := range trds {
				if err := llrp.WriteMessage(conn, llrp.ROAccessReportMessage(trd.Data, s.nextMessageID()), llrp.DefaultLimits()); err != nil {
					log.Errorf("error sending RO_ACCESS_REPORT: %v", err)
					return
				}
			}
		}
	}()
	return done
}
