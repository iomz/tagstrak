package golemu

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/emulator"
)

const validScenario = `{"version":1,"seed":1,"repeat":true,"cycles":[["0001"]]}`

func tmp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "emu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func newApp(t *testing.T, c Config) *App {
	t.Helper()
	scenario, err := emulator.DecodeScenario(strings.NewReader(validScenario))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(c, scenario, emulator.SystemClock{}, emulator.SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func wait(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func joined(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
		return nil
	}
}
func TestLocalControlAndShutdown(t *testing.T) {
	c := DefaultConfig()
	c.Emulator.Address = "127.0.0.1:0"
	c.ControlSocket = filepath.Join(tmp(t), "control.sock")
	a := newApp(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	wait(t, func() bool { return a.Status().Running })
	info, err := os.Stat(c.ControlSocket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.ControlSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for _, tc := range []struct {
		body string
		code int
	}{{`{"invalid":true}`, 400}, {validScenario, 204}, {strings.Repeat("x", emulator.MaxScenarioBytes+1), 400}} {
		request, err := http.NewRequest(http.MethodPut, "http://local/scenario", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != tc.code {
			t.Fatal(response.StatusCode)
		}
	}
	if a.Status().Revision != 2 {
		t.Fatal(a.Status())
	}
	response, err := client.Get("http://local/status")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	cancel()
	if err = joined(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(c.ControlSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket survived: %v", err)
	}
	if a.Status().Running {
		t.Fatal(a.Status())
	}
}
func TestPrivateControlDirectoryRequired(t *testing.T) {
	dir := tmp(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Emulator.Address = "127.0.0.1:0"
	c.ControlSocket = filepath.Join(dir, "control.sock")
	a := newApp(t, c)
	if err := a.Run(context.Background()); !errors.Is(err, emulator.ErrConfig) {
		t.Fatal(err)
	}
	if a.Status().Running {
		t.Fatal("data listener started")
	}
}
func TestExistingControlPathPreserved(t *testing.T) {
	c := DefaultConfig()
	c.Emulator.Address = "127.0.0.1:0"
	c.ControlSocket = filepath.Join(tmp(t), "existing")
	if err := os.WriteFile(c.ControlSocket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newApp(t, c)
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("overwrote existing path")
	}
	data, err := os.ReadFile(c.ControlSocket)
	if err != nil || string(data) != "keep" {
		t.Fatal(string(data), err)
	}
}
func TestDataBindFailureClosesControl(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := DefaultConfig()
	c.Emulator.Address = listener.Addr().String()
	c.ControlSocket = filepath.Join(tmp(t), "control.sock")
	a := newApp(t, c)
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected bind failure")
	}
	if _, err = os.Stat(c.ControlSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket survived startup failure", err)
	}
}
