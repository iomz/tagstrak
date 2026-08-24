package connection

import (
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
	log "github.com/sirupsen/logrus"
)

// Handler manages one LLRP connection against a shared inventory service.
type Handler struct {
	currentMessageID  *uint32
	pdu               int
	reportInterval    int
	keepaliveInterval int
	inventory         *inventory.Service
	isConnAlive       *atomic.Bool
	reportLoopStarted *atomic.Bool
}

func NewHandler(initialMessageID, pdu, reportInterval, keepaliveInterval int, inventory *inventory.Service, isConnAlive *atomic.Bool) *Handler {
	messageID := uint32(initialMessageID)
	return &Handler{currentMessageID: &messageID, pdu: pdu, reportInterval: reportInterval, keepaliveInterval: keepaliveInterval, inventory: inventory, isConnAlive: isConnAlive, reportLoopStarted: &atomic.Bool{}}
}

func (h *Handler) nextMessageID() uint32 {
	return atomic.AddUint32(h.currentMessageID, 1) - 1
}

func (h *Handler) HandleRequest(conn net.Conn) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	for {
		message, err := llrp.ReadMessage(conn, llrp.DefaultLimits())
		if err == io.EOF {
			log.Info("the client is disconnected, closing LLRP connection")
			return
		}
		if err != nil {
			log.Infof("closing LLRP connection due to %s", err)
			return
		}
		switch message.Header.Type {
		case llrp.SetReaderConfigHeader:
			if err := llrp.WriteMessage(conn, llrp.SetReaderConfigResponseMessage(h.nextMessageID()), llrp.DefaultLimits()); err != nil {
				log.Warnf("error writing SET_READER_CONFIG_RESPONSE: %v", err)
				return
			}
			if h.reportLoopStarted.CompareAndSwap(false, true) {
				h.startReportLoop(conn, done)
			}
		case llrp.KeepaliveAckHeader:
			if h.reportLoopStarted.CompareAndSwap(false, true) {
				h.startReportLoop(conn, done)
			}
		default:
			log.Warnf("unknown header: %v", message.Header.Type)
			return
		}
	}
}

func (h *Handler) startReportLoop(conn net.Conn, done <-chan struct{}) {
	reportTicker := time.NewTicker(time.Duration(h.reportInterval) * time.Millisecond)
	go func() {
		defer reportTicker.Stop()
		defer h.reportLoopStarted.Store(false)
		keepaliveTicker := (<-chan time.Time)(nil)
		if h.keepaliveInterval > 0 {
			ticker := time.NewTicker(time.Duration(h.keepaliveInterval) * time.Second)
			defer ticker.Stop()
			keepaliveTicker = ticker.C
		}
		h.isConnAlive.Store(true)
		h.sendReports(conn)
		for h.isConnAlive.Load() {
			select {
			case <-done:
				return
			case <-reportTicker.C:
				h.sendReports(conn)
			case <-keepaliveTicker:
				if err := llrp.WriteMessage(conn, llrp.KeepaliveMessage(h.nextMessageID()), llrp.DefaultLimits()); err != nil {
					log.Warnf("error writing KEEP_ALIVE: %v", err)
					h.isConnAlive.Store(false)
				}
			}
		}
	}()
}

func (h *Handler) sendReports(conn net.Conn) {
	reports, err := buildTagReportDataStack(h.inventory.Snapshot(), h.pdu)
	if err != nil {
		log.Warn(err)
		h.isConnAlive.Store(false)
		return
	}
	for _, report := range reports {
		if err := llrp.WriteMessage(conn, llrp.ROAccessReportMessage(report.Data, h.nextMessageID()), llrp.DefaultLimits()); err != nil {
			log.Warn(err)
			h.isConnAlive.Store(false)
			return
		}
	}
}

func (h *Handler) SendReaderEventNotification(conn net.Conn) error {
	if err := llrp.WriteMessage(conn, llrp.ReaderEventNotificationMessage(h.nextMessageID(), uint64(time.Now().UTC().UnixMicro())), llrp.DefaultLimits()); err != nil {
		return err
	}
	return nil
}

func (h *Handler) IsConnAlive() bool { return h.isConnAlive.Load() }
