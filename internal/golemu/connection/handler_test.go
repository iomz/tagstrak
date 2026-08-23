package connection

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func newInventory(t *testing.T, values ...string) *inventory.Service {
	t.Helper()
	service := inventory.NewService(inventory.NewStore(), "", inventory.DefaultLimits())
	for _, value := range values {
		tag, err := inventory.ParseTag(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Upsert(tag); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

func TestHandlerUsesInventorySnapshots(t *testing.T) {
	service := newInventory(t, "3000")
	alive := &atomic.Bool{}
	handler := NewHandler(1000, 1500, 1000, 0, service, alive)
	if handler.inventory != service {
		t.Fatal("inventory not retained")
	}
	reports := buildTagReportDataStack(service.Snapshot(), 1500)
	if len(reports) != 1 || reports[0].TagCount != 1 {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestHandlerSendsReaderEventNotification(t *testing.T) {
	handler := NewHandler(1000, 1500, 1000, 0, newInventory(t), &atomic.Bool{})
	var buffer bytes.Buffer
	if err := handler.SendReaderEventNotification(&mockConn{writer: &buffer}); err != nil {
		t.Fatal(err)
	}
	message, err := llrp.DecodeMessage(buffer.Bytes(), llrp.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Type != llrp.ReaderEventNotificationHeader || message.Header.ID != 1000 {
		t.Fatalf("header = %#v", message.Header)
	}
}

func TestBuildTagReportDataStackDerivesPCFromEPC(t *testing.T) {
	reports := buildTagReportDataStack(newInventory(t, "3000").Snapshot(), 1500)
	events, err := llrp.DecodeReadEvents(reports[0].Data, llrp.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !bytes.Equal(events[0].ID, []byte{0x30, 0x00}) {
		t.Fatalf("events = %#v", events)
	}
}
