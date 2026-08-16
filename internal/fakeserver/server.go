package fakeserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 5 * time.Second

func Serve(ctx context.Context, listener net.Listener, config Config) error {
	if ctx == nil {
		return fmt.Errorf("server context is required")
	}
	if listener == nil {
		return fmt.Errorf("server listener is required")
	}
	handler, err := NewHandler(config)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serveError := make(chan error, 1)
	go func() {
		serveError <- server.Serve(listener)
	}()

	select {
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownError := server.Shutdown(shutdownContext)
		if shutdownError != nil {
			_ = server.Close()
		}
		serveResult := <-serveError
		if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
			return serveResult
		}
		if shutdownError != nil {
			return fmt.Errorf("gracefully shut down fake server: %w", shutdownError)
		}
		return nil
	}
}
