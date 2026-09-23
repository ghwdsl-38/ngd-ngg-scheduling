// application.go 组装 Algorithm 的缓存、Prometheus 客户端、Python Worker 和 HTTP 路由。
// 生产入口与 Go Test 都通过 Application 使用同一套业务实现。
package algorithm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	topologypkg "demo.ngg/algorithm-server/topology"
	"go.uber.org/zap"
)

// Config 描述一个可独立启动的 Algorithm Application。
// 测试通过显式字段注入 Mock Prometheus 和 Python 路径，避免修改进程全局环境。
type Config struct {
	BootID                   string
	PrometheusURL            string
	PrometheusBearerToken    string
	PrometheusNodeLabel      string
	PrometheusMetricsFile    string
	PrometheusClient         *http.Client
	MetricsRefreshInterval   time.Duration
	MetricsStaleAfter        time.Duration
	DisableBackgroundMetrics bool
	PythonExecutable         string
	PythonModule             string
	PythonPath               string
	WorkerEvidenceDir        string
	TopologyConfigFile       string
	TopologySourceFile       string
	Logger                   *zap.Logger
	// Data fields are used by deterministic tests; production reads mounted files.
	TopologyConfigData []byte
	TopologySourceData []byte
}

// Application 是 Algorithm 进程内所有有状态组件的生命周期容器。
type Application struct {
	ctx         context.Context
	cancel      context.CancelFunc
	service     *service
	handler     http.Handler
	worker      *PythonWorker
	logger      *zap.SugaredLogger
	metricsOnce sync.Once
	once        sync.Once
}

// ConfigFromEnv 构造生产启动配置。认证与 TLS 仍沿用既有环境变量。
func ConfigFromEnv() (Config, error) {
	token, err := loadBearerToken()
	if err != nil {
		return Config{}, err
	}
	client, err := newPrometheusHTTPClient(secondsEnv("PROMETHEUS_REQUEST_TIMEOUT_SECONDS", 5))
	if err != nil {
		return Config{}, err
	}
	return Config{
		PrometheusURL:          os.Getenv("PROMETHEUS_URL"),
		PrometheusBearerToken:  token,
		PrometheusNodeLabel:    os.Getenv("PROMETHEUS_NODE_LABEL"),
		PrometheusMetricsFile:  os.Getenv("PROMETHEUS_METRICS_CONFIG_FILE"),
		PrometheusClient:       client,
		MetricsRefreshInterval: secondsEnv("PROMETHEUS_REFRESH_SECONDS", 15),
		MetricsStaleAfter:      secondsEnv("PROMETHEUS_STALE_SECONDS", 120),
		PythonExecutable:       env("PYTHON_EXECUTABLE", "python3"),
		PythonModule:           env("PYTHON_WORKER_MODULE", "algorithm_worker.worker"),
		PythonPath:             os.Getenv("PYTHONPATH"),
		WorkerEvidenceDir:      os.Getenv("ALGORITHM_WORKER_EVIDENCE_DIR"),
		TopologyConfigFile:     env("TOPOLOGY_CONFIG_FILE", "/etc/ngd-ngg/topology.yaml"),
		TopologySourceFile:     os.Getenv("TOPOLOGY_SOURCE_FILE"),
	}, nil
}

// NewApplication 创建真实Algorithm业务实例，但不自行监听TCP端口。
func NewApplication(config Config) (*Application, error) {
	if config.BootID == "" {
		config.BootID = fmt.Sprintf("algorithm-go-%d", time.Now().UnixNano())
	}
	if config.MetricsRefreshInterval <= 0 {
		config.MetricsRefreshInterval = 15 * time.Second
	}
	if config.MetricsStaleAfter <= 0 {
		config.MetricsStaleAfter = 120 * time.Second
	}
	if config.PrometheusClient == nil {
		config.PrometheusClient = &http.Client{Timeout: 5 * time.Second}
	}
	if config.PythonExecutable == "" {
		config.PythonExecutable = "python3"
	}
	if config.PythonModule == "" {
		config.PythonModule = "algorithm_worker.worker"
	}
	baseLogger := config.Logger
	if baseLogger == nil {
		baseLogger = zap.NewNop()
	}
	logger := baseLogger.With(zap.String("component", "algorithm")).Sugar()

	catalogue, err := loadMetricCatalogue(config.PrometheusMetricsFile)
	if err != nil {
		return nil, err
	}
	if config.PrometheusNodeLabel == "" {
		config.PrometheusNodeLabel = catalogue.NodeLabel
	}

	ctx, cancel := context.WithCancel(context.Background())
	var networkTopology *topologypkg.Cache
	if config.TopologySourceFile != "" || len(config.TopologySourceData) > 0 {
		networkTopology, err = topologypkg.LoadSource(config.TopologySourceFile, config.TopologySourceData)
	} else {
		networkTopology, err = topologypkg.LoadConfig(config.TopologyConfigFile, config.TopologyConfigData)
	}
	if err != nil {
		cancel()
		return nil, fmt.Errorf("load Algorithm network topology: %w", err)
	}
	topologyStatus := networkTopology.Status()
	logger.Infow("topology loaded", "event", "topology_loaded", "version", networkTopology.Version(), "topologyMode", networkTopology.Mode(),
		"snapshotId", shortLogID(networkTopology.SnapshotID()), "leafCount", topologyStatus["leafCount"],
		"roomCount", topologyStatus["roomCount"], "leafDomainCount", topologyStatus["leafDomainCount"],
		"spineDomainCount", topologyStatus["spineDomainCount"], "borderDomainCount", topologyStatus["borderDomainCount"],
		"spineOnlyLeafCount", topologyStatus["spineOnlyLeafCount"], "borderOnlyLeafCount", topologyStatus["borderOnlyLeafCount"],
		"layeredLeafCount", topologyStatus["layeredLeafCount"])
	worker, err := NewPythonWorker(ctx, WorkerConfig{
		EvidenceDir: config.WorkerEvidenceDir,
		Executable:  config.PythonExecutable,
		Module:      config.PythonModule,
		PythonPath:  config.PythonPath,
		Logger:      baseLogger,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start Python algorithm worker: %w", err)
	}
	metrics := &metricsCache{
		baseURL: config.PrometheusURL, definitions: catalogue.Metrics,
		catalogueVersion: catalogue.Version, bearerToken: config.PrometheusBearerToken,
		nodeLabel: config.PrometheusNodeLabel, interval: config.MetricsRefreshInterval,
		staleAfter: config.MetricsStaleAfter, client: config.PrometheusClient, logger: logger,
	}
	service := &service{bootID: config.BootID, logger: logger, static: &staticCache{}, topology: networkTopology, metrics: metrics, worker: worker}
	mux := http.NewServeMux()
	registerRoutes(mux, service)
	app := &Application{ctx: ctx, cancel: cancel, service: service, handler: mux, worker: worker, logger: logger}
	if !config.DisableBackgroundMetrics {
		app.StartBackgroundMetrics()
	}
	logger.Infow("application ready", "event", "application_ready", "bootId", config.BootID,
		"prometheusEnabled", metrics.enabled(), "metricCount", len(metrics.configuredDefinitions()),
		"topologySnapshotId", shortLogID(networkTopology.SnapshotID()))
	return app, nil
}

// Handler 返回生产和测试共用的HTTP Handler。
func (a *Application) Handler() http.Handler { return a.handler }

// StartBackgroundMetrics starts the immediate-and-periodic Prometheus refresh
// loop exactly once. Server uses this after the HTTP listener is accepting.
func (a *Application) StartBackgroundMetrics() {
	if a == nil || a.service == nil || a.service.metrics == nil {
		return
	}
	a.metricsOnce.Do(func() {
		a.logger.Infow("Prometheus refresh loop started", "event", "prometheus_refresh_loop_started",
			"enabled", a.service.metrics.enabled(), "interval", a.service.metrics.interval)
		go a.service.metrics.run(a.ctx)
	})
}

// RefreshMetrics 立即执行一次Prometheus拉取，供测试在计时前确定缓存已经Ready。
func (a *Application) RefreshMetrics(ctx context.Context) error {
	if !a.service.metrics.enabled() {
		return nil
	}
	a.service.metrics.refresh(ctx)
	snapshot, _, warnings := a.service.metrics.resolve()
	if snapshot == nil {
		return fmt.Errorf("Prometheus metrics are not ready: %v", warnings)
	}
	return nil
}

// CacheStatus 返回可序列化的缓存状态，便于测试保存静态和指标证据。
func (a *Application) CacheStatus() map[string]any { return cacheStatus(a.service) }

// Close 关闭唯一Python Worker和后台指标协程，可重复调用。
func (a *Application) Close() error {
	var closeErr error
	a.once.Do(func() {
		closeErr = a.worker.Close()
		a.cancel()
	})
	return closeErr
}

func registerRoutes(mux *http.ServeMux, app *service) {
	// healthz只表示进程存活；readyz不等待静态快照或Prometheus预热。
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
		started := time.Now()
		snapshotID := r.PathValue("snapshotID")
		app.logger.Debugw("static snapshot received", "event", "static_snapshot_received", "snapshotId", shortLogID(snapshotID))
		body, apiErr := decodeBody(r)
		if apiErr != nil {
			app.logger.Warnw("static snapshot rejected", "event", "static_snapshot_rejected", "snapshotId", shortLogID(snapshotID),
				"code", apiErr.Code, "elapsedMs", durationMilliseconds(started), "error", apiErr.Message)
			writeAPIError(w, apiErr)
			return
		}
		snapshot, err := app.static.put(snapshotID, body)
		if err != nil {
			app.logger.Warnw("static snapshot rejected", "event", "static_snapshot_rejected", "snapshotId", shortLogID(snapshotID),
				"code", "INVALID_REQUEST", "elapsedMs", durationMilliseconds(started), "error", err.Error())
			writeAPIError(w, &apiError{Code: "INVALID_REQUEST", Message: err.Error(), Status: 400})
			return
		}
		app.logger.Infow("static snapshot accepted", "event", "static_snapshot_accepted", "snapshotId", shortLogID(snapshot.SnapshotID),
			"nodeCount", len(snapshot.Nodes), "bootId", app.bootID, "elapsedMs", durationMilliseconds(started))
		writeJSON(w, 200, map[string]any{"accepted": true, "snapshotId": snapshot.SnapshotID, "acceptedSnapshotId": snapshot.SnapshotID, "algorithmBootId": app.bootID, "bootId": app.bootID, "nodeCount": len(snapshot.Nodes), "checksum": snapshot.SnapshotID})
	})
	mux.HandleFunc("POST /api/v1/allocate", calculateHandler(app))
}

func calculateHandler(app *service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acceptedAt := time.Now()
		body, apiErr := decodeBody(r)
		if apiErr != nil {
			app.logger.Warnw("allocation rejected", "event", "allocation_rejected", "code", apiErr.Code,
				"elapsedMs", durationMilliseconds(acceptedAt), "error", apiErr.Message)
			writeAPIError(w, apiErr)
			return
		}
		requestID := stringValue(body["requestId"])
		app.logger.Infow("allocation received", "event", "allocation_received", "requestId", requestID,
			"ngdUID", stringValue(body["ngdUID"]), "generation", body["ngdGeneration"])
		response, apiErr := app.allocate(r.Context(), body)
		if apiErr != nil {
			app.logger.Errorw("allocation failed", "event", "allocation_failed", "requestId", requestID, "code", apiErr.Code,
				"statusCode", apiErr.Status, "retryable", apiErr.Retryable, "elapsedMs", durationMilliseconds(acceptedAt), "error", apiErr.Message)
			writeAPIError(w, apiErr)
			return
		}
		processingMs := durationMilliseconds(acceptedAt)
		response["timing"] = map[string]any{
			"unit": "ms", "algorithmProcessingMs": processingMs,
			"boundary": "HTTP handler accepted request -> candidate result ready",
		}
		groups, _ := response["candidateNodeGroups"].([]map[string]any)
		app.logger.Infow("allocation completed", "event", "allocation_completed", "requestId", requestID,
			"status", stringValue(response["status"]), "candidateGroupCount", len(groups), "degraded", response["degraded"],
			"warningCount", len(stringSlice(response["warnings"])), "elapsedMs", processingMs)
		writeJSON(w, 200, response)
	}
}

func cacheStatus(app *service) map[string]any {
	return map[string]any{"bootId": app.bootID, "runtime": "go", "nodeStatic": app.static.status(), "networkTopology": app.topology.Status(), "schedulerState": map[string]any{"cached": false, "mode": "request-scoped"}, "metrics": app.metrics.status()}
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

func shortLogID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 20 {
		return value[:20]
	}
	return value
}

func durationMilliseconds(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

func stringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
