package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"

	// "os/user"
	"time"

	"boot.dev/linko/internal/store"
)

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error error
}

func HttpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	http.Error(w, err.Error(), status)
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func addRequestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = rand.Text()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// catches what was in request body and what was written to response body
			spyReadader := &spyReadCloser{ReadCloser: r.Body}
			spyResponseWriter := &spyResponseWriter{ResponseWriter: w}
			// replace the request body with the spyReadCloser
			r.Body = spyReadader

			logContext := &LogContext{}
			contextWithLogContext := context.WithValue(r.Context(), logContextKey, logContext)
			r = r.WithContext(contextWithLogContext)

			// call the next handler in the chain
			next.ServeHTTP(spyResponseWriter, r)

			requestID := spyResponseWriter.Header().Get("X-Request-ID")

			// attrs := []any{
			// 	"Served request",
			// 	slog.String("request_id", requestID),
			// 	slog.String("method", r.Method),
			// 	slog.String("path", r.URL.Path),
			// 	slog.String("client_ip", r.RemoteAddr),
			// 	slog.Duration("duration", time.Since(start)),
			// 	slog.Int("request_body_bytes", spyReadader.bytesRead),
			// 	slog.Int("response_status", spyResponseWriter.statusCode),
			// 	slog.Int("response_body_bytes", spyResponseWriter.bytesWritten),
			// }

			requestLogger := logger
			if logContext.Username != "" {
				requestLogger = logger.With("user", logContext.Username)
				// attrs = append(attrs, slog.String("user", logContext.Username))
			}

			if logContext.Error != nil {
				requestLogger = requestLogger.With("error", logContext.Error)
				// attrs = append(attrs, slog.Any("error", logContext.Error))
			}	

			requestLogger.Info(
				"Served request",
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", r.RemoteAddr),
				slog.Duration("duration", time.Since(start)),
				slog.Int("request_body_bytes", spyReadader.bytesRead),
				slog.Int("response_status", spyResponseWriter.statusCode),
				slog.Int("response_body_bytes", spyResponseWriter.bytesWritten),
			)
			// requestLogger.Info("Served request", attrs...)
		})
	}
}

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: addRequestIDHeader(requestLogger(logger)(mux)),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return errors.New("failed to get TCP address")
	}
	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d \n", tcpAddr.Port))

	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
