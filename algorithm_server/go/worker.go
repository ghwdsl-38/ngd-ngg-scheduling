// worker.go 管理单一 Python 子进程，并实现 Go 与 Python 的 JSON Lines 请求/响应协议。
package main

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

func startPythonWorker(ctx context.Context, evidenceDir, command string, arguments ...string) (*pythonWorker, error) {
	// Python stderr 直接进入 Server 日志；stdout 专用于机器可读协议。
	cmd := exec.CommandContext(ctx, command, arguments...)
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
	// 1000 Node 上下文可能较大，因此把 Scanner 上限提升到 32 MiB。
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

func internalWorkerError(err error) *apiError {
	return &apiError{Code: "ALGORITHM_WORKER_UNAVAILABLE", Message: err.Error(), Retryable: true, Status: 503}
}
