package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	c, err := parseConfig([]string{"start", "--reader", "localhost:5085", "--max-retries", "2", "--read-timeout", "4s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Reader.Address != "localhost:5085" || c.Reader.MaxRetries != 2 || c.Reader.ReadTimeout != 4*time.Second {
		t.Fatalf("config: %+v", c)
	}
	for _, args := range [][]string{{"--queue-capacity", "0"}, {"--max-frame-bytes", "4294967296"}, {"--max-parameter-bytes", "65536"}, {"--max-parameters", "-1"}, {"--handshake-timeout", "0"}, {"--reader", "invalid"}, {"--max-retries", "-1"}, {"unexpected"}} {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, nil, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
