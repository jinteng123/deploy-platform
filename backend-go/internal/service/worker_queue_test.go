package service

import (
	"context"
	"reflect"
	"testing"
)

type pendingDeployScannerStub struct {
	Persistence
	ids       []int64
	afterID   int64
	limit     int
	callCount int
}

func (s *pendingDeployScannerStub) PendingDeployLogIDsAfter(afterID int64, limit int) ([]int64, error) {
	s.afterID = afterID
	s.limit = limit
	s.callCount++
	return append([]int64(nil), s.ids...), nil
}

func TestPendingDeployLogIDsAfterUsesBoundedOldestFirstMemoryPages(t *testing.T) {
	store := NewStoreWithData(nil, nil, []DeployLog{
		{ID: 1, Status: "success"},
		{ID: 2, Status: "pending"},
		{ID: 3, Status: "running"},
		{ID: 4, Status: "pending"},
		{ID: 5, Status: "pending"},
	}, nil, nil)

	ids, err := store.PendingDeployLogIDsAfter(0, 2)
	if err != nil || !reflect.DeepEqual(ids, []int64{2, 4}) {
		t.Fatalf("first pending page=%v err=%v", ids, err)
	}
	ids, err = store.PendingDeployLogIDsAfter(4, 2)
	if err != nil || !reflect.DeepEqual(ids, []int64{5}) {
		t.Fatalf("next pending page=%v err=%v", ids, err)
	}
}

func TestPendingDeployLogIDsAfterUsesPersistentScanner(t *testing.T) {
	scanner := &pendingDeployScannerStub{ids: []int64{17, 22}}
	store := NewStore()
	store.persistence = scanner

	ids, err := store.PendingDeployLogIDsAfter(10, 25)
	if err != nil || !reflect.DeepEqual(ids, []int64{17, 22}) {
		t.Fatalf("persistent pending page=%v err=%v", ids, err)
	}
	if scanner.afterID != 10 || scanner.limit != 25 || scanner.callCount != 1 {
		t.Fatalf("persistent scanner args after=%d limit=%d calls=%d", scanner.afterID, scanner.limit, scanner.callCount)
	}
}

func TestWorkerPendingScanWrapsCursorWithoutDuplicatingQueuedTasks(t *testing.T) {
	store := NewStoreWithData(nil, nil, []DeployLog{
		{ID: 1, Status: "pending"},
		{ID: 2, Status: "success"},
		{ID: 3, Status: "pending"},
	}, nil, nil)
	worker := newWorker(&Pipeline{store: store}, 1, false)
	defer worker.Close()

	worker.schedulePending()
	worker.schedulePending() // wraps at the end and sees the same queued rows

	if worker.pendingScanAfterID != 3 {
		t.Fatalf("scan cursor=%d, want 3 after wrap", worker.pendingScanAfterID)
	}
	if got := len(worker.jobs); got != 2 {
		t.Fatalf("duplicate scan grew queue to %d, want 2", got)
	}
	first, second := <-worker.jobs, <-worker.jobs
	if first != 1 || second != 3 {
		t.Fatalf("queued IDs=(%d,%d), want oldest pending IDs (1,3)", first, second)
	}
}

func TestWorkerSubmitDoesNotQueueSameIDTwice(t *testing.T) {
	worker := newWorker(nil, 1, false)
	defer worker.Close()

	if err := worker.Submit(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if err := worker.Submit(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if got := len(worker.jobs); got != 1 {
		t.Fatalf("same task was queued %d times", got)
	}
}
