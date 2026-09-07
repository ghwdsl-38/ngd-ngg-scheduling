package algorithm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests exercise the production JSONL transport against an actual Python process.
func newRecoveryWorker(t *testing.T, ctx context.Context, args ...string) *pythonWorker {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Worker recovery tests require python3:", err)
	}
	args = append([]string{"testdata/recovery_worker.py"}, args...)
	w, err := startPythonWorkerWithPath(ctx, "", "", python, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	return w
}

func waitWorkerMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Python did not reach handshake:", path)
}

func successfulWorkerPID(t *testing.T, w *pythonWorker, echo string) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := w.calculate(ctx, workerPayload{Request: map[string]any{"echo": echo}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CandidateNodeGroups) != 1 || result.CandidateNodeGroups[0]["echo"] != echo {
		t.Fatalf("response was mixed with another request: %+v", result)
	}
	return result.CandidateNodeGroups[0]["pid"].(float64)
}

func receiveWorkerError(t *testing.T, done <-chan *apiError, want int) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil || err.Status != want {
			t.Fatalf("error=%v, want HTTP %d", err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Worker call did not stop")
	}
}

func TestPythonWorkerTimeoutRecovery(t *testing.T) {
	w := newRecoveryWorker(t, context.Background())
	oldPID := successfulWorkerPID(t, w, "before")
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		marker := filepath.Join(t.TempDir(), "entered")
		done := make(chan *apiError, 1)
		go func() {
			_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": "hang", "entered": marker}})
			done <- err
		}()
		waitWorkerMarker(t, marker)
		receiveWorkerError(t, done, 504)
		cancel()
		newPID := successfulWorkerPID(t, w, "after-timeout")
		if newPID == oldPID {
			t.Fatal("timed out process was reused")
		}
		if successfulWorkerPID(t, w, "reuse") != newPID {
			t.Fatal("healthy process was not reused")
		}
		oldPID = newPID
	}
}

func TestPythonWorkerQueuedCancellationDoesNotKillActive(t *testing.T) {
	w := newRecoveryWorker(t, context.Background())
	pid := successfulWorkerPID(t, w, "before")
	dir := t.TempDir()
	entered, release := filepath.Join(dir, "entered"), filepath.Join(dir, "release")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *apiError, 1)
	go func() {
		_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": "hold", "entered": entered, "release": release}})
		done <- err
	}()
	waitWorkerMarker(t, entered)
	queued, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	_, err := w.calculate(queued, workerPayload{})
	if err == nil || err.Status != 504 {
		t.Fatalf("queued cancellation: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not finish")
	}
	if successfulWorkerPID(t, w, "still-alive") != pid {
		t.Fatal("queued cancellation killed active process")
	}
}

func TestPythonWorkerProtocolFailureRecovery(t *testing.T) {
	for _, mode := range []string{"exit", "malformed", "mismatch", "missing-result"} {
		t.Run(mode, func(t *testing.T) {
			w := newRecoveryWorker(t, context.Background())
			oldPID := successfulWorkerPID(t, w, "before")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": mode}})
			if err == nil || err.Status != 503 {
				t.Fatalf("protocol failure: %v", err)
			}
			if successfulWorkerPID(t, w, mode) == oldPID {
				t.Fatal("broken process was reused")
			}
		})
	}
}

func TestPythonWorkerBusinessErrorKeepsProcess(t *testing.T) {
	w := newRecoveryWorker(t, context.Background())
	pid := successfulWorkerPID(t, w, "before")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": "business-error"}})
	if err == nil || err.Status != 422 {
		t.Fatalf("business error: %v", err)
	}
	if successfulWorkerPID(t, w, "after") != pid {
		t.Fatal("business rejection unnecessarily restarted process")
	}
}

func TestPythonWorkerBlockedWriteCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "no-read")
	w := newRecoveryWorker(t, context.Background(), marker)
	waitWorkerMarker(t, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan *apiError, 1)
	go func() {
		_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"large": strings.Repeat("x", 2<<20)}})
		done <- err
	}()
	receiveWorkerError(t, done, 504)
}

func TestPythonWorkerCloseAndParentCancellation(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		name := "close"
		if parentCancel {
			name = "parent-cancel"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := newRecoveryWorker(t, parent)
			marker := filepath.Join(t.TempDir(), "entered")
			done := make(chan *apiError, 1)
			go func() {
				_, err := w.calculate(context.Background(), workerPayload{Request: map[string]any{"mode": "hang", "entered": marker}})
				done <- err
			}()
			waitWorkerMarker(t, marker)
			if parentCancel {
				cancel()
			} else if err := w.close(); err != nil {
				t.Fatal(err)
			}
			receiveWorkerError(t, done, 503)
			if err := w.close(); err != nil {
				t.Fatal(err)
			}
			_, err := w.calculate(context.Background(), workerPayload{})
			if err == nil || err.Status != 503 {
				t.Fatalf("closed worker accepted request: %v", err)
			}
		})
	}
}

func TestPythonWorkerConcurrentRequests(t *testing.T) {
	w := newRecoveryWorker(t, context.Background())
	var wg sync.WaitGroup
	errors := make(chan *apiError, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": "ok"}})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestPythonWorkerRestartFailureIsRetryable(t *testing.T) {
	w := newRecoveryWorker(t, context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := w.calculate(ctx, workerPayload{Request: map[string]any{"mode": "exit"}})
	if err == nil {
		t.Fatal("expected failed process")
	}
	originalStart := w.start
	attempts := 0
	w.start = func() (*workerProcess, error) {
		attempts++
		return nil, fmt.Errorf("test executable temporarily unavailable")
	}
	_, err = w.calculate(ctx, workerPayload{})
	if err == nil || err.Status != 503 || !err.Retryable || attempts != 1 {
		t.Fatalf("restart should attempt once and return retryable error: %v, attempts=%d", err, attempts)
	}
	w.start = originalStart
	successfulWorkerPID(t, w, "recovered-start")
}

func TestPythonWorkerIdleParentCancellationReapsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newRecoveryWorker(t, ctx)
	p := w.process
	cancel()
	select {
	case <-p.reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("idle process was not reaped after parent cancellation")
	}
}
