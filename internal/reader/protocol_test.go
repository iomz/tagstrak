package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func TestStateTransitions(t *testing.T) {
	s, peer := pipeSession(t, testConfig())
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan inventory.Observation, 1)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	if err := handshake(peer); err != nil {
		t.Fatal(err)
	}
	if err := llrp.WriteMessage(peer, llrp.KeepaliveMessage(7), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := readPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := llrp.WriteMessage(peer, report(t), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("no observation")
	}
	cancel()
	_ = waitResult(t, done)
	decoder := json.NewDecoder(&logs)
	var got []State
	for {
		var entry struct {
			State State `json:"state"`
		}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if entry.State != "" {
			got = append(got, entry.State)
		}
	}
	want := []State{Connecting, Handshaking, Serving, Keepalive, Serving, Reporting, Serving, Stopping, Stopped}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states: %v, want %v", got, want)
	}
}

func TestConfigResponseValidation(t *testing.T) {
	good := llrp.SetReaderConfigResponseMessage(1)
	for _, body := range [][]byte{nil, {1}, append(append([]byte{}, good.Payload...), good.Payload...), {1, 31, 0, 8, 0, 0, 0, 1}} {
		message := good
		message.Payload = body
		if err := validateConfigResponse(message, 1, llrp.DefaultLimits()); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %x: %v", body, err)
		}
	}
	if err := validateConfigResponse(good, 1, llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidObservationBatch(t *testing.T) {
	for _, epc := range [][]byte{
		{0, 241, 0, 8, 0, 15, 0, 1},    // Non-word bit count.
		{0, 241, 0, 9, 0, 16, 0, 1, 0}, // Trailing bytes.
		{},                             // No EPC.
		append(llrp.EPCData(0, 96, make([]byte, 12)), llrp.EPCData(0, 96, make([]byte, 12))...),
	} {
		parameter, err := llrp.EncodeParameter(llrp.Parameter{Type: 240, Data: epc})
		if err != nil {
			t.Fatal(err)
		}
		body := append(append([]byte{}, report(t).Payload...), parameter...)
		if observations, err := decodeObservations(body, llrp.DefaultLimits(), "test", time.Now()); err == nil || observations != nil {
			t.Fatalf("accepted batch: %x", body)
		}
	}
}

func TestHandshakeDeadlineIsNotExtendedByKeepalives(t *testing.T) {
	s, peer := pipeSession(t, testConfig())
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background(), make(chan inventory.Observation, 1)) }()
	if err := llrp.WriteMessage(peer, llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := readPeer(peer); err != nil {
		t.Fatal(err)
	}
	for {
		if err := llrp.WriteMessage(peer, llrp.KeepaliveMessage(7), llrp.DefaultLimits()); err != nil {
			break
		}
		if _, err := readPeer(peer); err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := waitResult(t, done); !errors.Is(err, ErrRetryExhausted) {
		t.Fatal(err)
	}
}

func TestPartialFrameTimesOut(t *testing.T) {
	s, peer := pipeSession(t, testConfig())
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background(), make(chan inventory.Observation, 1)) }()
	frame, err := llrp.EncodeMessage(llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = peer.Write(frame[:11]); err != nil {
		t.Fatal(err)
	}
	if err = waitResult(t, done); !errors.Is(err, ErrRetryExhausted) {
		t.Fatal(err)
	}
}
