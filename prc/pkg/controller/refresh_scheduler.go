package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const defaultRefreshEventBuffer = 1024

// RefreshScheduler only owns per-NGD timers. Queue de-duplication, concurrent
// workers and error rate limiting are deliberately delegated to the native
// controller-runtime Refresh Controller.
type RefreshScheduler struct {
	mu     sync.Mutex
	tasks  map[types.NamespacedName]*refreshTask
	events chan event.GenericEvent
	ctx    context.Context
}

type refreshTask struct {
	uid        types.UID
	generation int64
	timer      *time.Timer
	cancel     context.CancelFunc
}

func NewRefreshScheduler(buffer int) *RefreshScheduler {
	if buffer <= 0 {
		buffer = defaultRefreshEventBuffer
	}
	return &RefreshScheduler{
		tasks:  make(map[types.NamespacedName]*refreshTask),
		events: make(chan event.GenericEvent, buffer),
	}
}

func (s *RefreshScheduler) Events() <-chan event.GenericEvent { return s.events }

// Start makes the scheduler lifecycle part of controller-runtime Manager.
func (s *RefreshScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	<-ctx.Done()
	s.mu.Lock()
	for key, task := range s.tasks {
		if task.timer != nil {
			task.timer.Stop()
		}
		if task.cancel != nil {
			task.cancel()
		}
		delete(s.tasks, key)
	}
	s.mu.Unlock()
	return nil
}

// NeedLeaderElection ensures only the active PRC replica schedules refreshes.
func (*RefreshScheduler) NeedLeaderElection() bool { return true }

// Upsert records the latest Kubernetes identity. A changed generation cancels
// an in-flight HTTP request; the Processor still performs a final stale check.
func (s *RefreshScheduler) Upsert(key types.NamespacedName, uid types.UID, generation int64) (created, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, found := s.tasks[key]
	if !found {
		s.tasks[key] = &refreshTask{uid: uid, generation: generation}
		return true, false
	}
	changed = task.uid != uid || task.generation != generation
	if changed {
		if task.timer != nil {
			task.timer.Stop()
			task.timer = nil
		}
		if task.cancel != nil {
			task.cancel()
			task.cancel = nil
		}
		task.uid = uid
		task.generation = generation
	}
	return false, changed
}

// Trigger publishes an external GenericEvent into the native controller queue.
func (s *RefreshScheduler) Trigger(ctx context.Context, key types.NamespacedName) error {
	s.mu.Lock()
	_, found := s.tasks[key]
	s.mu.Unlock()
	if !found {
		return nil
	}
	return s.emit(ctx, key)
}

// Begin attaches a cancellable execution context to the current task identity.
func (s *RefreshScheduler) Begin(parent context.Context, key types.NamespacedName) (context.Context, types.UID, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, found := s.tasks[key]
	if !found {
		return parent, "", 0, false
	}
	ctx, cancel := context.WithCancel(parent)
	task.cancel = cancel
	return ctx, task.uid, task.generation, true
}

func (s *RefreshScheduler) Finish(key types.NamespacedName, uid types.UID, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, found := s.tasks[key]; found && task.uid == uid && task.generation == generation {
		if task.cancel != nil {
			task.cancel()
		}
		task.cancel = nil
	}
}

func (s *RefreshScheduler) IsCurrent(key types.NamespacedName, uid types.UID, generation int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, found := s.tasks[key]
	return found && task.uid == uid && task.generation == generation
}

// ScheduleAfter starts the only custom timing primitive in the design. When it
// expires it emits an event; controller-runtime owns everything after that.
func (s *RefreshScheduler) ScheduleAfter(key types.NamespacedName, uid types.UID, generation int64, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	s.mu.Lock()
	task, found := s.tasks[key]
	if !found || task.uid != uid || task.generation != generation {
		s.mu.Unlock()
		return
	}
	if task.timer != nil {
		task.timer.Stop()
	}
	task.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		current, ok := s.tasks[key]
		if !ok || current.uid != uid || current.generation != generation {
			s.mu.Unlock()
			return
		}
		current.timer = nil
		ctx := s.ctx
		s.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		_ = s.emit(ctx, key)
	})
	s.mu.Unlock()
}

// Remove cancels both the next timer and a currently executing request.
func (s *RefreshScheduler) Remove(key types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, found := s.tasks[key]; found {
		if task.timer != nil {
			task.timer.Stop()
		}
		if task.cancel != nil {
			task.cancel()
		}
		delete(s.tasks, key)
	}
}

func (s *RefreshScheduler) emit(ctx context.Context, key types.NamespacedName) error {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(platformDemandGVK)
	object.SetName(key.Name)
	select {
	case s.events <- event.GenericEvent{Object: client.Object(object)}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("emit refresh event for %s: %w", key.String(), ctx.Err())
	}
}
