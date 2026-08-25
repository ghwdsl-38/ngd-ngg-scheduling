// Command algorithm-server 是正式Go主进程入口，业务实现位于algorithm包。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	algorithm "demo.ngg/algorithm-server/algorithm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := algorithm.ConfigFromEnv()
	if err != nil {
		log.Fatalf("configure Algorithm application: %v", err)
	}
	app, err := algorithm.NewApplication(config)
	if err != nil {
		log.Fatalf("start Algorithm application: %v", err)
	}
	defer app.Close()

	address := os.Getenv("ALGORITHM_LISTEN_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	server := &http.Server{Addr: address, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Go Algorithm API Server listening on %s; Python module=%s", address, config.PythonModule)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
