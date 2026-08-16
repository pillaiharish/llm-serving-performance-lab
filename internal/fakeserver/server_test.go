package fakeserver

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestServeShutsDownWhenContextIsCanceled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, zeroDelayConfig()) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not shut down promptly")
	}
}

func TestServeValidatesInputs(t *testing.T) {
	if err := Serve(nil, nil, DefaultConfig()); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	if err := Serve(context.Background(), nil, DefaultConfig()); err == nil {
		t.Fatal("nil listener unexpectedly accepted")
	}
}
