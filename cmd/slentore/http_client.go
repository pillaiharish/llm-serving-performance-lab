package main

import (
	"fmt"
	"net/http"
)

func newSharedHTTPClient(effectiveWorkers int) (*http.Client, *http.Transport, error) {
	if effectiveWorkers <= 0 {
		return nil, nil, fmt.Errorf("effective worker count must be greater than zero")
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, nil, fmt.Errorf("default HTTP transport has unexpected type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	if transport.MaxIdleConns < effectiveWorkers {
		transport.MaxIdleConns = effectiveWorkers
	}
	transport.MaxIdleConnsPerHost = effectiveWorkers
	transport.MaxConnsPerHost = effectiveWorkers
	return &http.Client{Transport: transport}, transport, nil
}
