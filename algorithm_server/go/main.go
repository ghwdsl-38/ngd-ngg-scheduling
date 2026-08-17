package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	python := env("PYTHON_EXECUTABLE", "python3")
	module := env("PYTHON_WORKER_MODULE", "algorithm_worker.worker")
	worker, err := startPythonWorker(ctx, python, "-m", module)
	if err != nil {
		log.Fatalf("start Python algorithm worker: %v", err)
	}
	defer worker.close()

	refresh := secondsEnv("PROMETHEUS_REFRESH_SECONDS", 30)
	metrics := &metricsCache{
		baseURL: os.Getenv("PROMETHEUS_URL"), nodeLabel: env("PROMETHEUS_NODE_LABEL", "node"),
		cpuQuery:    env("PROMETHEUS_CPU_QUERY", `1 - avg by (node) (rate(node_cpu_seconds_total{mode="idle"}[5m]))`),
		memoryQuery: env("PROMETHEUS_MEMORY_QUERY", `1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`),
		interval:    refresh, staleAfter: secondsEnv("PROMETHEUS_STALE_SECONDS", 120),
		client: &http.Client{Timeout: secondsEnv("PROMETHEUS_REQUEST_TIMEOUT_SECONDS", 5)},
	}
	go metrics.run(ctx)
	app := &service{bootID: fmt.Sprintf("algorithm-go-%d", time.Now().UnixNano()), static: &staticCache{}, metrics: metrics, worker: worker}
	mux := http.NewServeMux()
	registerRoutes(mux, app)
	server := &http.Server{Addr: env("ALGORITHM_LISTEN_ADDRESS", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Go Algorithm API Server listening on %s; Python module=%s", server.Addr, module)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func registerRoutes(mux *http.ServeMux, app *service) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "bootId": app.bootID, "runtime": "go", "algorithmWorker": "python"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ready", "bootId": app.bootID})
	})
	mux.HandleFunc("GET /internal/v1/cache/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, cacheStatus(app)) })
	mux.HandleFunc("GET /internal/v1/node-static-cache/status", func(w http.ResponseWriter, _ *http.Request) {
		status := cacheStatus(app)
		node := status["nodeStatic"].(map[string]any)
		metrics := status["metrics"].(map[string]any)
		writeJSON(w, 200, map[string]any{"algorithmBootId": app.bootID, "ready": node["ready"], "acceptedSnapshotId": node["currentSnapshotId"], "previousSnapshotId": node["previousSnapshotId"], "nodeCount": node["nodeCount"], "metricSnapshotId": metrics["currentSnapshotId"]})
	})
	mux.HandleFunc("PUT /internal/v1/node-static-snapshots/{snapshotID}", func(w http.ResponseWriter, r *http.Request) {
		body, apiErr := decodeBody(r)
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		snapshot, err := app.static.put(r.PathValue("snapshotID"), body)
		if err != nil {
			writeAPIError(w, &apiError{Code: "INVALID_REQUEST", Message: err.Error(), Status: 400})
			return
		}
		writeJSON(w, 200, map[string]any{"accepted": true, "snapshotId": snapshot.SnapshotID, "acceptedSnapshotId": snapshot.SnapshotID, "algorithmBootId": app.bootID, "bootId": app.bootID, "nodeCount": len(snapshot.Nodes), "checksum": snapshot.SnapshotID})
	})
	mux.HandleFunc("POST /api/v1/allocate", calculateHandler(app, false))
	mux.HandleFunc("POST /api/v1/node-groups/calculate", calculateHandler(app, true))
}

func calculateHandler(app *service, legacy bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, apiErr := decodeBody(r)
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		response, apiErr := app.allocate(r.Context(), body, legacy)
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		writeJSON(w, 200, response)
	}
}

func cacheStatus(app *service) map[string]any {
	return map[string]any{"bootId": app.bootID, "runtime": "go", "nodeStatic": app.static.status(), "schedulerState": map[string]any{"cached": false, "mode": "request-scoped"}, "metrics": app.metrics.status()}
}

func decodeBody(r *http.Request) (map[string]any, *apiError) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, &apiError{Code: "INVALID_REQUEST", Message: err.Error(), Status: 400}
	}
	return body, nil
}

func writeAPIError(w http.ResponseWriter, err *apiError) {
	status := err.Status
	if status == 0 {
		status = 500
	}
	writeJSON(w, status, err)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func secondsEnv(name string, fallback float64) time.Duration {
	value, err := strconv.ParseFloat(env(name, fmt.Sprint(fallback)), 64)
	if err != nil || value <= 0 {
		value = fallback
	}
	return time.Duration(value * float64(time.Second))
}
