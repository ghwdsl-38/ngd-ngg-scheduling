// server.go owns the complete Algorithm process lifecycle: Application,
// listener, HTTP server, Prometheus refresh loop and Python Worker shutdown.
package algorithm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// ServerOptions controls transport lifecycle without leaking Algorithm
// internals to production main or integration tests.
type ServerOptions struct {
	ListenAddress     string
	Listener          net.Listener
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

// Server is a complete runnable Algorithm API Server.
type Server struct {
	application       *Application
	httpServer        *http.Server
	listener          net.Listener
	baseURL           string
	client            *http.Client
	backgroundMetrics bool
	shutdownTimeout   time.Duration

	mu        sync.Mutex
	started   bool
	serveErr  error
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewServer constructs the complete Algorithm server and binds its listener.
// Prometheus refresh does not start until Start/Run has started HTTP serving.
func NewServer(config Config, options ServerOptions) (*Server, error) {
	backgroundMetrics := !config.DisableBackgroundMetrics
	applicationConfig := config
	applicationConfig.DisableBackgroundMetrics = true
	application, err := NewApplication(applicationConfig)
	if err != nil {
		return nil, err
	}

	listener := options.Listener
	if listener == nil {
		address := options.ListenAddress
		if address == "" {
			address = ":8080"
		}
		listener, err = net.Listen("tcp", address)
		if err != nil {
			_ = application.Close()
			return nil, fmt.Errorf("listen for Algorithm API Server: %w", err)
		}
	}
	if options.ReadHeaderTimeout <= 0 {
		options.ReadHeaderTimeout = 5 * time.Second
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = 5 * time.Second
	}

	host := listener.Addr().String()
	if tcp, ok := listener.Addr().(*net.TCPAddr); ok && tcp.IP.IsUnspecified() {
		host = net.JoinHostPort("127.0.0.1", fmt.Sprint(tcp.Port))
	}
	return &Server{
		application: application,
		httpServer:  &http.Server{Handler: application.Handler(), ReadHeaderTimeout: options.ReadHeaderTimeout},
		listener:    listener, baseURL: "http://" + host, client: &http.Client{},
		backgroundMetrics: backgroundMetrics, shutdownTimeout: options.ShutdownTimeout,
		done: make(chan struct{}),
	}, nil
}

// Start starts HTTP serving and then starts the Prometheus refresh loop. It is
// non-blocking; use Wait or Run to observe terminal server errors.
func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.application == nil || s.httpServer == nil || s.listener == nil {
		return fmt.Errorf("Algorithm server is not initialized")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("Algorithm server is already started")
	}
	s.started = true
	s.mu.Unlock()

	go func() {
		err := s.httpServer.Serve(s.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if closeErr := s.application.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		s.mu.Lock()
		s.serveErr = err
		s.mu.Unlock()
		close(s.done)
	}()
	if s.backgroundMetrics {
		s.application.StartBackgroundMetrics()
	}
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
			defer cancel()
			_ = s.Close(shutdown)
		case <-s.done:
		}
	}()
	return nil
}

// Run starts the server and blocks until it stops.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	return s.Wait()
}

// Wait blocks until HTTP serving and Application cleanup have finished.
func (s *Server) Wait() error {
	if s == nil || s.done == nil {
		return fmt.Errorf("Algorithm server is not initialized")
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

// WaitForReady checks the real HTTP health endpoint and, when background
// metrics are enabled, waits for the first Prometheus snapshot.
func (s *Server) WaitForReady(ctx context.Context) error {
	if s == nil || s.application == nil {
		return fmt.Errorf("Algorithm server is not initialized")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/healthz", nil)
		if err == nil {
			response, requestErr := s.client.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					metricsReady := true
					if s.backgroundMetrics && s.application.service.metrics.enabled() {
						status := s.application.service.metrics.status()
						metricsReady, _ = status["ready"].(bool)
					}
					if metricsReady {
						return nil
					}
					lastErr = fmt.Errorf("Prometheus metrics are not ready")
				} else {
					lastErr = fmt.Errorf("Algorithm health returned HTTP %d", response.StatusCode)
				}
			} else {
				lastErr = requestErr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Algorithm server readiness: %w; lastError=%v", ctx.Err(), lastErr)
		case <-s.done:
			return fmt.Errorf("Algorithm server stopped before readiness: %v", s.Wait())
		case <-ticker.C:
		}
	}
}

// URL returns the listener base URL for PRC and tests.
func (s *Server) URL() string { return s.baseURL }

// HTTPClient returns the client used for local server communication.
func (s *Server) HTTPClient() *http.Client { return s.client }

// CacheStatus returns the same observable cache state as Application.
func (s *Server) CacheStatus() map[string]any { return s.application.CacheStatus() }

// Close gracefully stops HTTP and all Application-owned background resources.
func (s *Server) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		shutdownErr := s.httpServer.Shutdown(ctx)
		applicationErr := s.application.Close()
		if shutdownErr != nil {
			s.closeErr = shutdownErr
		} else {
			s.closeErr = applicationErr
		}
	})
	return s.closeErr
}
