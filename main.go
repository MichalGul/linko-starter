package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
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

func initializeLogger(logFile string) (*log.Logger, closeFunc, error) {
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %w", err)
		}


		bufferedFile := bufio.NewWriterSize(file, 8192)
		multiWriter := io.MultiWriter(os.Stderr, bufferedFile)

		// Buffer writes to improve performance
		return log.New(multiWriter, "", log.LstdFlags), func() error {
			if err := bufferedFile.Flush(); err != nil {
				return fmt.Errorf("failed to flush log buffer: %w", err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("failed to close log file: %w", err)
			}
			return nil
		}, nil
	}

	return log.New(os.Stderr, "", log.LstdFlags), func() error { return nil }, nil

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
	} ()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Printf("failed to create store: %v\n", err)
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

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Printf("failed to shutdown server: %v\n", err)
		return 1
	}
	if serverErr != nil {
		logger.Printf("server error: %v\n", serverErr)
		return 1
	}

	return 0
}
