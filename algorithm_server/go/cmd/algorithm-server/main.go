// Command algorithm-server is the production Go process entrypoint.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	algorithm "demo.ngg/algorithm-server/algorithm"
)

func main() {
	logger, err := algorithm.NewLoggerFromEnv()
	if err != nil {
		_, _ = os.Stderr.WriteString("level=error component=algorithm event=logger_initialization_failed error=" + err.Error() + "\n")
		os.Exit(2)
	}
	defer func() { _ = logger.Sync() }()
	log := logger.Sugar().With("component", "algorithm")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := algorithm.ConfigFromEnv()
	if err != nil {
		log.Errorw("configure Algorithm application", "event", "configuration_failed", "error", err)
		os.Exit(1)
	}
	config.Logger = logger
	address := os.Getenv("ALGORITHM_LISTEN_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	server, err := algorithm.NewServer(config, algorithm.ServerOptions{ListenAddress: address})
	if err != nil {
		log.Errorw("create Algorithm server", "event", "server_creation_failed", "error", err)
		os.Exit(1)
	}
	log.Infow("Algorithm API Server listening", "event", "server_listening", "address", address, "pythonModule", config.PythonModule)
	if err := server.Run(ctx); err != nil {
		log.Errorw("Algorithm server stopped with an error", "event", "server_failed", "error", err)
		os.Exit(1)
	}
}
