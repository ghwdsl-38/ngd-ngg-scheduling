package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestRefreshSchedulerTriggerAndTimer(t *testing.T) {
	scheduler := NewRefreshScheduler(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = scheduler.Start(ctx) }()

	key := types.NamespacedName{Name: "demand-a"}
	uid := types.UID("uid-a")
	created, changed := scheduler.Upsert(key, uid, 1)
	if !created || changed {
		t.Fatalf("first Upsert created=%t changed=%t", created, changed)
	}
	if err := scheduler.Trigger(ctx, key); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-scheduler.Events():
		if event.Object.GetName() != key.Name {
			t.Fatalf("event name=%s, want %s", event.Object.GetName(), key.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate event was not emitted")
	}

	scheduler.ScheduleAfter(key, uid, 1, 10*time.Millisecond)
	select {
	case event := <-scheduler.Events():
		if event.Object.GetName() != key.Name {
			t.Fatalf("timer event name=%s, want %s", event.Object.GetName(), key.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled event was not emitted")
	}
}

func TestRefreshSchedulerUpdateCancelsOldExecutionAndRemoveStopsTimer(t *testing.T) {
	scheduler := NewRefreshScheduler(4)
	key := types.NamespacedName{Name: "demand-b"}
	uid := types.UID("uid-b")
	scheduler.Upsert(key, uid, 1)
	execution, oldUID, oldGeneration, found := scheduler.Begin(context.Background(), key)
	if !found || oldUID != uid || oldGeneration != 1 {
		t.Fatalf("Begin found=%t uid=%s generation=%d", found, oldUID, oldGeneration)
	}
	_, changed := scheduler.Upsert(key, uid, 2)
	if !changed {
		t.Fatal("generation update was not detected")
	}
	select {
	case <-execution.Done():
	case <-time.After(time.Second):
		t.Fatal("old execution context was not cancelled")
	}
	if scheduler.IsCurrent(key, uid, 1) || !scheduler.IsCurrent(key, uid, 2) {
		t.Fatal("scheduler did not replace the current generation")
	}

	scheduler.ScheduleAfter(key, uid, 2, 20*time.Millisecond)
	scheduler.Remove(key)
	select {
	case <-scheduler.Events():
		t.Fatal("removed task still emitted a timer event")
	case <-time.After(60 * time.Millisecond):
	}
}
