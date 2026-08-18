package main

import (
	"net/http"
	"testing"
)

func TestNewSharedHTTPClientConfiguresWorkerBoundedPool(t *testing.T) {
	client, transport, err := newSharedHTTPClient(8)
	if err != nil {
		t.Fatalf("newSharedHTTPClient: %v", err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	if client.Transport != transport {
		t.Fatal("client does not use returned shared transport")
	}
	if transport == http.DefaultTransport {
		t.Fatal("default transport was not cloned")
	}
	if transport.MaxIdleConns < 8 {
		t.Fatalf("MaxIdleConns = %d, want at least 8", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 8 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 8", transport.MaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != 8 {
		t.Fatalf("MaxConnsPerHost = %d, want 8", transport.MaxConnsPerHost)
	}
}

func TestNewSharedHTTPClientRejectsInvalidWorkerCount(t *testing.T) {
	if _, _, err := newSharedHTTPClient(0); err == nil {
		t.Fatal("newSharedHTTPClient unexpectedly accepted zero workers")
	}
}
