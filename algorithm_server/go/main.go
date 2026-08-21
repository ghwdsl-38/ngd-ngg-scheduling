// main.go 是 Algorithm API Server 的正式进程入口。
// 它负责启动 Go HTTP 服务、Prometheus 指标刷新任务和唯一 Python 算法 Worker。
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
	// SIGINT/SIGTERM 统一取消根 Context，使 HTTP、指标协程和 Worker 一起退出。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Python 只运行算法，不监听 HTTP 端口；模块名允许测试或扩展时覆盖。
	python := env("PYTHON_EXECUTABLE", "python3")
	module := env("PYTHON_WORKER_MODULE", "algorithm_worker.worker")
	worker, err := startPythonWorker(ctx, os.Getenv("ALGORITHM_WORKER_EVIDENCE_DIR"), python, "-m", module)
	if err != nil {
		log.Fatalf("start Python algorithm worker: %v", err)
	}
	defer worker.close()

	// 指标目录在 Mock 与真实 Prometheus 间共用，避免两套指标名和单位漂移。
	catalogue, err := loadMetricCatalogue(os.Getenv("PROMETHEUS_METRICS_CONFIG_FILE"))
	if err != nil {
		log.Fatalf("load Prometheus metrics catalogue: %v", err)
	}
	token, err := loadBearerToken()
	if err != nil {
		log.Fatalf("load Prometheus authentication: %v", err)
	}
	refresh := secondsEnv("PROMETHEUS_REFRESH_SECONDS", 30)
	client, err := newPrometheusHTTPClient(secondsEnv("PROMETHEUS_REQUEST_TIMEOUT_SECONDS", 5))
	if err != nil {
		log.Fatalf("configure Prometheus HTTP client: %v", err)
	}
	metrics := &metricsCache{
		baseURL: os.Getenv("PROMETHEUS_URL"), definitions: catalogue.Metrics,
		catalogueVersion: catalogue.Version, bearerToken: token,
		nodeLabel: env("PROMETHEUS_NODE_LABEL", catalogue.NodeLabel),
		interval:  refresh, staleAfter: secondsEnv("PROMETHEUS_STALE_SECONDS", 120), client: client,
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
	// healthz 只表示进程存活；readyz 不等待静态快照或 Prometheus 预热。
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
	// PRC 先上传以内容 Hash 标识的 Node 静态快照，再提交任务级计算请求。
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
	// legacy=true 时只转换旧 PRC 所需的组 ID/拓扑字段，不改变分数与排序。
	return func(w http.ResponseWriter, r *http.Request) {
		// 业务处理时间从HTTP Handler接收请求开始，到候选结果完成为止；
		// 不包含响应JSON编码、网络传输和测试证据写盘。
		acceptedAt := time.Now()
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
		response["timing"] = map[string]any{
			"unit":                  "ms",
			"algorithmProcessingMs": float64(time.Since(acceptedAt).Microseconds()) / 1000,
			"boundary":              "HTTP handler accepted request -> candidate result ready",
		}
		writeJSON(w, 200, response)
	}
}

func cacheStatus(app *service) map[string]any {
	// Node 动态状态明确标记为 request-scoped，防止被误认为跨请求缓存。
	return map[string]any{"bootId": app.bootID, "runtime": "go", "nodeStatic": app.static.status(), "schedulerState": map[string]any{"cached": false, "mode": "request-scoped"}, "metrics": app.metrics.status()}
}

func decodeBody(r *http.Request) (map[string]any, *apiError) {
	// 限制请求体为 32 MiB，足以承载 1000 Node 快照并避免无限制读入内存。
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
