package benchmarkexec

import (
	"net/http"
	"testing"
)

func TestNewSharedHTTPClientConfiguresAdmissionBoundedPool(t *testing.T) {
	client, transport, err := newSharedHTTPClient(8)
	if err != nil {
		t.Fatalf("newSharedHTTPClient: %v", err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if client.Transport != transport || transport == http.DefaultTransport {
		t.Fatal("client must use a cloned transport")
	}
	if transport.MaxIdleConns < 8 || transport.MaxIdleConnsPerHost != 8 || transport.MaxConnsPerHost != 8 {
		t.Fatalf("transport limits = (%d, %d, %d)", transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	}
}

func TestNewSharedHTTPClientRejectsInvalidConnectionLimit(t *testing.T) {
	if _, _, err := newSharedHTTPClient(0); err == nil {
		t.Fatal("newSharedHTTPClient unexpectedly accepted zero workers")
	}
}
