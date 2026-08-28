// Command algorithm-server 是正式Go主进程入口，业务实现位于algorithm包。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	algorithm "demo.ngg/algorithm-server/algorithm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := algorithm.ConfigFromEnv()
	if err != nil {
		log.Fatalf("configure Algorithm application: %v", err)
	}
	address := os.Getenv("ALGORITHM_LISTEN_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	server, err := algorithm.NewServer(config, algorithm.ServerOptions{ListenAddress: address})
	if err != nil {
		log.Fatalf("create Algorithm server: %v", err)
	}
	log.Printf("Go Algorithm API Server listening on %s; Python module=%s", address, config.PythonModule)
	if err := server.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
