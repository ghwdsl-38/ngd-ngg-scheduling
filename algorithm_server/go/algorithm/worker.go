// worker.go 管理单一 Python 子进程，并实现 Go 与 Python 的 JSON Lines 请求/响应协议。
package algorithm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
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
	// gate 是可取消的串行入口；持有者独占当前进程及其管道。
	gate        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	start       func() (*workerProcess, error)
	process     *workerProcess
	nextStart   time.Time
	closeOnce   sync.Once
	closeErr    error
	nextID      atomic.Uint64
	evidenceDir string
}

const workerCleanupTimeout = 2 * time.Second
const workerRestartBackoff = 100 * time.Millisecond

// 每次重启创建独立的 Scanner/管道；旧进程的读取者绝不读取新进程。
type workerProcess struct {
	command    *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	stdoutPipe io.ReadCloser
	stopOnce   sync.Once
	reaped     chan struct{}
	stopping   bool // 仅由持有 gate 的调用方访问，不读取 exec.Cmd 的并发内部状态。
}

// 关闭 OS 管道可以解除阻塞的 Write/Scan；强制终止后限时等待回收。
// 不把主动终止产生的 ExitError 当成 Close 失败。
func (p *workerProcess) stop() error {
	p.stopping = true
	p.stopOnce.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdoutPipe.Close()
		_ = p.command.Process.Kill()
		go func() { _ = p.command.Wait(); close(p.reaped) }()
	})
	timer := time.NewTimer(workerCleanupTimeout)
	defer timer.Stop()
	select {
	case <-p.reaped:
		return nil
	case <-timer.C:
		return fmt.Errorf("Python worker process cleanup timed out")
	}
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
	lifetime, cancel := context.WithCancel(ctx)
	w := &pythonWorker{ctx: lifetime, cancel: cancel, gate: make(chan struct{}, 1), evidenceDir: evidenceDir}
	w.start = func() (*workerProcess, error) {
		cmd := exec.CommandContext(lifetime, command, arguments...)
		if pythonPath != "" {
			cmd.Env = append(os.Environ(), "PYTHONPATH="+pythonPath)
		}
		return startPythonWorkerCommand(cmd)
	}
	var err error
	w.process, err = w.start()
	if err != nil {
		cancel()
		return nil, err
	}
	// 父 Context 取消时也回收空闲 Worker，不依赖下一次请求触发清理。
	go func() { <-lifetime.Done(); _ = w.close() }()
	return w, nil
}

func startPythonWorkerCommand(cmd *exec.Cmd) (*workerProcess, error) {
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		return nil, err
	}
	log.Printf("component=algorithm event=python_worker_started pid=%d", cmd.Process.Pid)
	return &workerProcess{command: cmd, stdin: stdin, stdout: scanner, stdoutPipe: stdoutPipe, reaped: make(chan struct{})}, nil
}

// recycle 只在持有 gate 时调用。未完成回收的进程不能被新进程替代。
func (w *pythonWorker) recycle() error {
	if w.process == nil {
		return nil
	}
	if err := w.process.stop(); err != nil {
		return err
	}
	w.process = nil
	w.nextStart = time.Now().Add(workerRestartBackoff)
	return nil
}

func workerTimeout(err error) *apiError {
	return &apiError{Code: "ALGORITHM_TIMEOUT", Message: err.Error(), Retryable: true, Status: 504}
}

func (w *pythonWorker) calculate(ctx context.Context, payload workerPayload) (result workerResult, resultErr *apiError) {
	started := time.Now()
	requestID := stringValue(payload.Request["requestId"])
	log.Printf("component=algorithm event=python_worker_request_queued requestId=%s", requestID)
	defer func() {
		if resultErr != nil {
			log.Printf("component=algorithm event=python_worker_request_completed requestId=%s status=failed code=%s elapsedMs=%.3f",
				requestID, resultErr.Code, durationMilliseconds(started))
			return
		}
		log.Printf("component=algorithm event=python_worker_request_completed requestId=%s status=success candidateGroupCount=%d elapsedMs=%.3f",
			requestID, len(result.CandidateNodeGroups), durationMilliseconds(started))
	}()
	// 当前为单 Worker 串行模型；消息 ID 用于检测协议错位。
	select {
	case <-ctx.Done():
		return workerResult{}, workerTimeout(ctx.Err())
	case <-w.ctx.Done():
		return workerResult{}, internalWorkerError(fmt.Errorf("Python worker is closed"))
	case w.gate <- struct{}{}:
	}
	defer func() { <-w.gate }()
	if err := ctx.Err(); err != nil {
		return workerResult{}, workerTimeout(err)
	}
	if err := w.ctx.Err(); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	if w.process != nil && w.process.stopping {
		if err := w.recycle(); err != nil {
			return workerResult{}, internalWorkerError(err)
		}
	}
	if w.process == nil {
		log.Printf("component=algorithm event=python_worker_restart_wait requestId=%s backoffUntil=%s", requestID, w.nextStart.UTC().Format(time.RFC3339Nano))
		timer := time.NewTimer(time.Until(w.nextStart))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return workerResult{}, workerTimeout(ctx.Err())
		case <-w.ctx.Done():
			return workerResult{}, internalWorkerError(w.ctx.Err())
		case <-timer.C:
		}
		var err error
		w.process, err = w.start()
		if err != nil {
			w.nextStart = time.Now().Add(workerRestartBackoff)
			return workerResult{}, internalWorkerError(err)
		}
	}
	id := strconv.FormatUint(w.nextID.Add(1), 10)
	log.Printf("component=algorithm event=python_worker_jsonl_started requestId=%s workerRequestId=%s", requestID, id)
	request := workerEnvelope{ID: id, Payload: payload}
	raw, err := json.Marshal(request)
	if err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	if err := w.appendEvidence("go-to-python-request.jsonl", raw); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	type scanResult struct {
		raw []byte
		err error
	}
	done := make(chan scanResult, 1)
	p := w.process
	// Write 和 Scan 均在可取消的事务内；取消时关闭这一代进程的管道。
	go func() {
		if _, err := p.stdin.Write(append(raw, '\n')); err != nil {
			done <- scanResult{err: err}
			return
		}
		if !p.stdout.Scan() {
			done <- scanResult{err: p.stdout.Err()}
			return
		}
		done <- scanResult{raw: append([]byte(nil), p.stdout.Bytes()...)}
	}()
	var scanned scanResult
	// 请求超时先清理这一代进程，再返回 504，不能遗留读取者给下一请求。
	select {
	case <-ctx.Done():
		_ = w.recycle()
		<-done // 管道已关闭，等待本次读写者退出后才释放串行入口。
		return workerResult{}, workerTimeout(ctx.Err())
	case <-w.ctx.Done():
		_ = w.recycle()
		<-done
		return workerResult{}, internalWorkerError(w.ctx.Err())
	case scanned = <-done:
	}
	if scanned.err != nil {
		_ = w.recycle()
		return workerResult{}, internalWorkerError(scanned.err)
	}
	if scanned.raw == nil {
		_ = w.recycle()
		return workerResult{}, internalWorkerError(fmt.Errorf("Python worker closed stdout"))
	}
	if err := w.appendEvidence("python-to-go-response.jsonl", scanned.raw); err != nil {
		return workerResult{}, internalWorkerError(err)
	}
	var response struct {
		ID     string        `json:"id"`
		Result *workerResult `json:"result"`
		Error  *workerError  `json:"error"`
	}
	if err := json.Unmarshal(scanned.raw, &response); err != nil {
		_ = w.recycle()
		return workerResult{}, internalWorkerError(err)
	}
	if response.ID != id {
		_ = w.recycle()
		return workerResult{}, internalWorkerError(fmt.Errorf("worker response id %q, want %q", response.ID, id))
	}
	if (response.Result == nil) == (response.Error == nil) {
		_ = w.recycle()
		return workerResult{}, internalWorkerError(fmt.Errorf("worker response must contain exactly one of result or error"))
	}
	if response.Error != nil {
		status := response.Error.Status
		if status == 0 {
			status = 500
		}
		return workerResult{}, &apiError{RequestID: response.Error.RequestID, Code: response.Error.Code, Message: response.Error.Message, Retryable: response.Error.Retryable, Status: status}
	}
	return *response.Result, nil
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
	w.closeOnce.Do(func() {
		w.cancel()
		timer := time.NewTimer(2 * workerCleanupTimeout)
		defer timer.Stop()
		select {
		case w.gate <- struct{}{}:
			defer func() { <-w.gate }()
			w.closeErr = w.recycle()
		case <-timer.C:
			w.closeErr = fmt.Errorf("Python worker shutdown timed out waiting for active request")
		}
	})
	return w.closeErr
}

// Close取消排队和执行中的请求、终止并回收子进程，可重复调用。
func (w *PythonWorker) Close() error { return w.inner.close() }

func (w *PythonWorker) close() error { return w.Close() }

func internalWorkerError(err error) *apiError {
	return &apiError{Code: "ALGORITHM_WORKER_UNAVAILABLE", Message: err.Error(), Retryable: true, Status: 503}
}
