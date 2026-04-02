package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"
	"log"
	"boot.dev/linko/internal/store"
)


var Logger = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)


func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()

	Logger.Println("Linko is shutting down")
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	st, err := store.New(dataDir)
	if err != nil {
		Logger.Printf("failed to create store: %v\n", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		Logger.Printf("failed to shutdown server: %v\n", err)
		return 1
	}
	if serverErr != nil {
		Logger.Printf("server error: %v\n", serverErr)
		return 1
	}

	return 0
}
