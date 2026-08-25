// worker.go 管理单一 Python 子进程，并实现 Go 与 Python 的 JSON Lines 请求/响应协议。
package algorithm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
)

type calculator interface {
	// calculator 便于 service 使用真实 Python Worker 或测试替身。
	calculate(context.Context, workerPayload) (workerResult, *apiError)
	close() error
}

// WorkerConfig定义Python Worker进程及模块位置。
type WorkerConfig struct {
	EvidenceDir string
	Executable  string
	Module      string
	PythonPath  string
}

// PythonWorker是生产Application和第一组Go Test共用的Worker生命周期入口。
type PythonWorker struct {
	inner *pythonWorker
}

// PreparedPythonRequest保存已经转换成正式Worker协议类型的请求。
// 测试可以在计时前完成通用map到内部类型的转换，确保计时只覆盖生产实际使用的
// JSONL写入、Python计算、JSONL读取以及Go响应解析。
type PreparedPythonRequest struct {
	payload workerPayload
}

// ParsedPythonResult保存Python JSONL响应已经被Go解析后的正式结果。
// AsMap转换只用于测试校验和证据输出，不属于Go调用Python的业务计时。
type ParsedPythonResult struct {
	result workerResult
}

type pythonWorker struct {
	// stdin/stdout 是共享流，互斥锁保证一次写入只匹配一次读取。
	mu      sync.Mutex
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	nextID  atomic.Uint64
	// evidenceDir 仅供演示的 evidence-run 使用。为空时完全不做协议落盘，
	// 因而 timing-run 不会受到文件 I/O 影响。
	evidenceDir string
}

// NewPythonWorker启动一个真实Python算法Worker。
func NewPythonWorker(ctx context.Context, config WorkerConfig) (*PythonWorker, error) {
	if config.Executable == "" {
		config.Executable = "python3"
	}
	if config.Module == "" {
		config.Module = "algorithm_worker.worker"
	}
	inner, err := startPythonWorkerWithPath(ctx, config.EvidenceDir, config.PythonPath, config.Executable, "-m", config.Module)
	if err != nil {
		return nil, err
	}
	return &PythonWorker{inner: inner}, nil
}

func startPythonWorkerWithPath(ctx context.Context, evidenceDir, pythonPath, command string, arguments ...string) (*pythonWorker, error) {
	cmd := exec.CommandContext(ctx, command, arguments...)
	if pythonPath != "" {
		cmd.Env = append(os.Environ(), "PYTHONPATH="+pythonPath)
	}
	return startPythonWorkerCommand(cmd, evidenceDir)
}

func startPythonWorkerCommand(cmd *exec.Cmd, evidenceDir string) (*pythonWorker, error) {
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pythonWorker{command: cmd, stdin: stdin, stdout: scanner, evidenceDir: evidenceDir}, nil
}

func (w *pythonWorker) calculate(ctx context.Context, payload workerPayload) (workerResult, *apiError) {
	// 当前为单 Worker 串行模型；消息 ID 用于检测协议错位。
	w.mu.Lock()
	defer w.mu.Unlock()
	id := strconv.FormatUint(w.nextID.Add(1), 10)
	request := workerEnvelope{ID: id, Payload: payload}
	raw, err := json.Marshal(request)
	if err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	if err := w.appendEvidence("go-to-python-request.jsonl", raw); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	if _, err := w.stdin.Write(append(raw, '\n')); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	type scanResult struct {
		raw []byte
		err error
	}
	done := make(chan scanResult, 1)
	go func() {
		if !w.stdout.Scan() {
			done <- scanResult{err: w.stdout.Err()}
			return
		}
		done <- scanResult{raw: append([]byte(nil), w.stdout.Bytes()...)}
	}()
	var scanned scanResult
	// HTTP 请求超时会立即返回 504；根 Context 取消还会终止 Python 进程。
	select {
	case <-ctx.Done():
		return workerResult{}, &apiError{Code: "ALGORITHM_TIMEOUT", Message: ctx.Err().Error(), Retryable: true, Status: 504}
	case scanned = <-done:
	}
	if scanned.err != nil {
		return workerResult{}, internalWorkerError(scanned.err)
	}
	if scanned.raw == nil {
		return workerResult{}, internalWorkerError(fmt.Errorf("Python worker closed stdout"))
	}
	if err := w.appendEvidence("python-to-go-response.jsonl", scanned.raw); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	var response workerEnvelope
	if err := json.Unmarshal(scanned.raw, &response); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	if response.ID != id {
		return workerResult{}, internalWorkerError(fmt.Errorf("worker response id %q, want %q", response.ID, id))
	}
	if response.Error != nil {
		status := response.Error.Status
		if status == 0 {
			status = 500
		}
		return workerResult{}, &apiError{RequestID: response.Error.RequestID, Code: response.Error.Code, Message: response.Error.Message, Retryable: response.Error.Retryable, Status: status}
	}
	return response.Result, nil
}

// Calculate把通用JSON对象转换为正式Worker Payload并返回通用JSON结果。
// 该接口避免测试依赖未导出的内部协议结构，同时仍执行完全相同的Worker代码。
func (w *PythonWorker) Calculate(ctx context.Context, payload map[string]any) (map[string]any, error) {
	prepared, err := PreparePythonRequest(payload)
	if err != nil {
		return nil, err
	}
	parsed, err := w.CalculatePrepared(ctx, prepared)
	if err != nil {
		return nil, err
	}
	return parsed.AsMap()
}

// PreparePythonRequest把展示用的通用JSON对象转换为正式Worker请求。
// 该方法应在性能计时开始前调用。
func PreparePythonRequest(payload map[string]any) (*PreparedPythonRequest, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var typed workerPayload
	if err := json.Unmarshal(raw, &typed); err != nil {
		return nil, err
	}
	return &PreparedPythonRequest{payload: typed}, nil
}

// CalculatePrepared执行生产代码使用的同一个JSONL调用入口。
// 返回时Python响应已经由Go解析为workerResult，但尚未转换为展示用map。
func (w *PythonWorker) CalculatePrepared(ctx context.Context, request *PreparedPythonRequest) (*ParsedPythonResult, error) {
	if request == nil {
		return nil, fmt.Errorf("prepared Python request must not be nil")
	}
	result, apiErr := w.inner.calculate(ctx, request.payload)
	if apiErr != nil {
		return nil, apiErr
	}
	return &ParsedPythonResult{result: result}, nil
}

// AsMap在计时结束后生成测试校验和证据文件使用的通用JSON对象。
func (r *ParsedPythonResult) AsMap() (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("parsed Python result must not be nil")
	}
	raw, err := json.Marshal(r.result)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return generic, nil
}

func (w *PythonWorker) calculate(ctx context.Context, payload workerPayload) (workerResult, *apiError) {
	return w.inner.calculate(ctx, payload)
}

func (w *pythonWorker) appendEvidence(name string, raw []byte) error {
	if w.evidenceDir == "" {
		return nil
	}
	if err := os.MkdirAll(w.evidenceDir, 0o755); err != nil {
		return fmt.Errorf("create Algorithm evidence directory: %w", err)
	}
	path := w.evidenceDir + string(os.PathSeparator) + name
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open Algorithm evidence file: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(append([]byte(nil), raw...), '\n')); err != nil {
		return fmt.Errorf("write Algorithm evidence file: %w", err)
	}
	return nil
}

func (w *pythonWorker) close() error {
	_ = w.stdin.Close()
	return w.command.Wait()
}

// Close要求Python Worker读到EOF后正常退出，可重复调用由Application统一保证。
func (w *PythonWorker) Close() error { return w.inner.close() }

func (w *PythonWorker) close() error { return w.Close() }

func internalWorkerError(err error) *apiError {
	return &apiError{Code: "ALGORITHM_WORKER_UNAVAILABLE", Message: err.Error(), Retryable: true, Status: 503}
}
