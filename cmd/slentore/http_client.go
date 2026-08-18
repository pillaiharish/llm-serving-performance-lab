package main

import (
	"fmt"
	"net/http"
)

func newSharedHTTPClient(connectionLimit int) (*http.Client, *http.Transport, error) {
	if connectionLimit <= 0 {
		return nil, nil, fmt.Errorf("shared HTTP connection limit must be greater than zero")
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, nil, fmt.Errorf("default HTTP transport has unexpected type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	if transport.MaxIdleConns < connectionLimit {
		transport.MaxIdleConns = connectionLimit
	}
	transport.MaxIdleConnsPerHost = connectionLimit
	transport.MaxConnsPerHost = connectionLimit
	return &http.Client{Transport: transport}, transport, nil
}
