package server

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/iomz/tagstrak/v2/internal/golemu/api"
	"github.com/iomz/tagstrak/v2/internal/golemu/connection"
	"github.com/iomz/tagstrak/v2/internal/inventory"
	log "github.com/sirupsen/logrus"
)

// Server exposes one shared inventory through HTTP management and LLRP reports.
type Server struct {
	ip                string
	port              int
	apiPort           int
	file              string
	pdu               int
	reportInterval    int
	keepaliveInterval int
	initialMessageID  int
	inventory         *inventory.Service
	isConnAlive       *atomic.Bool
	llrpHandler       *connection.Handler
}

func NewServer(ip string, port, apiPort, pdu, reportInterval, keepaliveInterval, initialMessageID int, file string) *Server {
	isConnAlive := &atomic.Bool{}
	inventoryService := inventory.NewService(inventory.NewStore(), file, inventory.DefaultLimits())
	return &Server{
		ip: ip, port: port, apiPort: apiPort, file: file, pdu: pdu, reportInterval: reportInterval,
		keepaliveInterval: keepaliveInterval, initialMessageID: initialMessageID, inventory: inventoryService,
		isConnAlive: isConnAlive,
		llrpHandler: connection.NewHandler(initialMessageID, pdu, reportInterval, keepaliveInterval, inventoryService, isConnAlive),
	}
}

func (s *Server) Run() int {
	if err := s.loadInventory(); err != nil {
		log.Errorf("loading inventory: %v", err)
		return 1
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.ip+":"+strconv.Itoa(s.port))
	if err != nil {
		log.Error(err)
		return 1
	}
	defer listener.Close()
	log.Infof("listening on %v:%v", s.ip, s.port)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() { <-signals; listener.Close() }()
	apiServer := api.NewServer(s.apiPort, s.inventory)
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Errorf("API server error: %v", err)
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0
			}
			log.Error(err)
			continue
		}
		if err := s.llrpHandler.SendReaderEventNotification(conn); err != nil {
			log.Errorf("error sending READER_EVENT_NOTIFICATION: %v", err)
			conn.Close()
			continue
		}
		go s.llrpHandler.HandleRequest(conn)
	}
}

func (s *Server) loadInventory() error {
	if s.file == "" {
		return nil
	}
	if _, err := os.Stat(s.file); errors.Is(err, os.ErrNotExist) {
		log.Warnf("%v does not exist; starting with empty inventory", s.file)
		return nil
	}
	if err := s.inventory.Load(); err != nil {
		return err
	}
	log.Infof("%v inventory tags loaded", len(s.inventory.Snapshot()))
	return nil
}
