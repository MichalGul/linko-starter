package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"

	// "io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"

	pkgerr "github.com/pkg/errors"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()

	os.Exit(status)
}

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

// replaceAttr is a slog.HandlerOptions.ReplaceAttr function that formats error attributes 
// with their full stack trace. Changes low with error key to a string with the formatted error. Leaves all other attributes unchanged.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		if stackErr, ok := errors.AsType[stackTracer](err); ok {

			return slog.GroupAttrs("error", 
				slog.Attr{
				Key: "message",
				Value: slog.StringValue(stackErr.Error()),
			},
				slog.Attr{
					Key: "stack_trace",
					Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
				},
			)
		}

	}
	return a
}

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %w", err)
		}

		bufferedFile := bufio.NewWriterSize(file, 8192)
		// multiWriter := io.MultiWriter(os.Stderr, bufferedFile)

		// Debug and above goes to stderr
		debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: replaceAttr,
		})
		// Level and above goes to file
		infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})

		logger := slog.New(slog.NewMultiHandler(
			debugHandler,
			infoHandler,
		))

		// Buffer writes to improve performance
		return logger, func() error {
			if err := bufferedFile.Flush(); err != nil {
				return fmt.Errorf("failed to flush log buffer: %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("failed to close log file: %w", err)
			}
			return nil
		}, nil
	}

	return slog.New(slog.NewTextHandler(os.Stderr, nil)),
		func() error { return nil },
		nil

}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {

	logger, close, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}

	defer func() {
		if err := close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close logger: %v\n", err)
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error(
			"failed to create store",
			slog.Any("error", err),
		)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger.Debug("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(
			"failed to shutdown server",
			slog.Any("error", err),
		)
		return 1
	}
	if serverErr != nil {
		logger.Error(
			"server error",
			slog.Any("error", serverErr),
		)
		return 1
	}

	return 0
}
