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
	calculate(context.Context, workerPayload) (workerResult, *apiError)
	close() error
}

type pythonWorker struct {
	mu      sync.Mutex
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	nextID  atomic.Uint64
}

func startPythonWorker(ctx context.Context, command string, arguments ...string) (*pythonWorker, error) {
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
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pythonWorker{command: cmd, stdin: stdin, stdout: scanner}, nil
}

func (w *pythonWorker) calculate(ctx context.Context, payload workerPayload) (workerResult, *apiError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := strconv.FormatUint(w.nextID.Add(1), 10)
	request := workerEnvelope{ID: id, Payload: payload}
	raw, err := json.Marshal(request)
	if err != nil {
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

func (w *pythonWorker) close() error {
	_ = w.stdin.Close()
	return w.command.Wait()
}

func internalWorkerError(err error) *apiError {
	return &apiError{Code: "ALGORITHM_WORKER_UNAVAILABLE", Message: err.Error(), Retryable: true, Status: 503}
}
