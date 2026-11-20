package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

var ErrWorkerClosed = errors.New("worker 已关闭")

// Worker processes queued deploy-log IDs through the pipeline.
type Worker struct {
	pipeline           *Pipeline
	jobs               chan int64
	mu                 sync.Mutex
	queued             map[int64]struct{}
	processing         map[int64]struct{}
	closed             bool
	stop               chan struct{}
	wg                 sync.WaitGroup
	recoveryMu         sync.Mutex
	interrupted        map[int64]string
	pendingScanAfterID int64
}

func NewWorker(pipeline *Pipeline, size int) *Worker {
	if size < 1 {
		size = 1
	}
	return newWorker(pipeline, size, true)
}

func newWorker(pipeline *Pipeline, size int, start bool) *Worker {
	w := &Worker{
		pipeline:    pipeline,
		jobs:        make(chan int64, 100),
		queued:      map[int64]struct{}{},
		processing:  map[int64]struct{}{},
		stop:        make(chan struct{}),
		interrupted: map[int64]string{},
	}
	if start {
		w.start(size)
	}
	return w
}

// NewRecoveredWorker restores durable state before starting consumers. This
// prevents a scanner from claiming a task while startup recovery resets it.
func NewRecoveredWorker(ctx context.Context, pipeline *Pipeline, size int) (*Worker, error) {
	w := newWorker(pipeline, size, false)
	if err := RecoverDeploys(ctx, pipeline.store, w); err != nil {
		w.Close()
		return nil, err
	}
	w.start(size)
	return w, nil
}
func (w *Worker) start(size int) {
	if size < 1 {
		size = 1
	}
	for i := 0; i < size; i++ {
		w.wg.Add(1)
		go w.run()
	}
	w.wg.Add(1)
	go w.scanPending()
}

// Submit queues a log for execution. It returns an error when the worker is
// closed or the context is done. The queue is a wakeup hint; the durable
// pending tasks remain discoverable if the channel is full.
func (w *Worker) Submit(ctx context.Context, id int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWorkerClosed
	}
	if _, exists := w.queued[id]; exists {
		return nil
	}
	if _, exists := w.processing[id]; exists {
		return nil
	}
	select {
	case w.jobs <- id:
		w.queued[id] = struct{}{}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// The log is durable; a full wakeup channel must not discard its task.
		return nil
	}
}

// ReconcileAndResume reopens only after the operator-triggered live readback
// confirms that the interrupted task's exact target image is already active.
func (w *Worker) ReconcileAndResume(ctx context.Context, id int64) error {
	if err := w.pipeline.ReconcileInterrupted(ctx, id); err != nil {
		return err
	}
	return w.Submit(ctx, id)
}

// Close stops accepting jobs, drains the queue and waits for workers.
func (w *Worker) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.stop)
		close(w.jobs)
	}
	w.mu.Unlock()
	w.wg.Wait()
}

func (w *Worker) run() {
	defer w.wg.Done()
	for {
		select {
		case id, ok := <-w.jobs:
			if !ok {
				return
			}
			w.mu.Lock()
			delete(w.queued, id)
			w.processing[id] = struct{}{}
			w.mu.Unlock()
			w.process(id)
			w.mu.Lock()
			delete(w.processing, id)
			w.mu.Unlock()
		}
	}
}

// scanPending is the single periodic scheduler for all local workers. Durable
// stores use bounded indexed keyset pages; memory mode uses the local Store.
// The cursor wraps when it reaches the end so a temporarily unclaimable row
// cannot starve later pending work.
func (w *Worker) scanPending() {
	defer w.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.retryInterrupted()
			w.schedulePending()
		}
	}
}

func (w *Worker) retryInterrupted() {
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	if w.pipeline == nil || w.pipeline.store == nil {
		return
	}
	for id, msg := range w.interrupted {
		if err := w.pipeline.store.FailInterruptedLog(id, msg); err == nil {
			delete(w.interrupted, id)
		}
	}
}

func (w *Worker) schedulePending() {
	if w.pipeline == nil || w.pipeline.store == nil {
		return
	}
	const scanBatchSize = 100
	ids, err := w.pipeline.store.PendingDeployLogIDsAfter(w.pendingScanAfterID, scanBatchSize)
	if err != nil {
		log.Printf("[worker] 扫描待执行任务失败: %v", err)
		return
	}
	if len(ids) == 0 && w.pendingScanAfterID != 0 {
		w.pendingScanAfterID = 0
		ids, err = w.pipeline.store.PendingDeployLogIDsAfter(0, scanBatchSize)
		if err != nil {
			log.Printf("[worker] 从队列起点扫描待执行任务失败: %v", err)
			return
		}
	}
	if len(ids) == 0 {
		return
	}
	w.pendingScanAfterID = ids[len(ids)-1]
	for _, id := range ids {
		if err := w.Submit(context.Background(), id); errors.Is(err, ErrWorkerClosed) {
			return
		}
	}
}

func (w *Worker) process(id int64) {
	claimID, err := newWorkerClaimID()
	if err != nil {
		log.Printf("[worker] 生成任务 %d 的认领标识失败: %v", id, err)
		return
	}
	claimed, err := w.pipeline.store.ClaimDeployLog(id, claimID)
	if err != nil {
		log.Printf("[worker] 领取任务 %d 失败: %v", id, err)
		return
	}
	if !claimed {
		return
	}
	defer func() {
		if err := w.pipeline.store.ReleaseLogOwnership(id, claimID); err != nil {
			log.Printf("[worker] 释放任务 %d 的认领失败: %v", id, err)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[worker] panic processing log %d: %v", id, r)
			if w.holdBlueGreenForAttention(id, fmt.Sprintf("执行器发生 panic，外部变更结果需要核对: %v", r)) {
				return
			}
			if err := w.pipeline.MarkFailed(id, fmt.Sprintf("panic: %v", r)); err != nil {
				log.Printf("[worker] 标记日志 %d 为失败状态时出错: %v", id, err)
				w.recoveryMu.Lock()
				w.interrupted[id] = fmt.Sprintf("panic 后持久化失败，需要确认实际状态: %v", r)
				w.recoveryMu.Unlock()
			}
		}
	}()
	if err := w.pipeline.Run(context.Background(), id); err != nil {
		if errors.Is(err, ErrDeliveryRunStopped) {
			return
		}
		log.Printf("[worker] 日志 %d 流水线错误: %v", id, err)
		if w.holdBlueGreenForAttention(id, fmt.Sprintf("蓝绿流水线状态无法确认: %v", err)) {
			return
		}
		// Do not blindly replay after an ambiguous side effect. Once persistence
		// recovers, expose a terminal interrupted state for explicit reconciliation.
		msg := fmt.Sprintf("执行状态持久化失败，需要确认实际状态后重试: %v", err)
		w.recoveryMu.Lock()
		w.interrupted[id] = msg
		w.recoveryMu.Unlock()
	}
}

func (w *Worker) holdBlueGreenForAttention(id int64, message string) bool {
	if w == nil || w.pipeline == nil || w.pipeline.store == nil {
		return false
	}
	run, err := w.pipeline.store.DeliveryRunForDeployLog(id)
	if err != nil || run.Strategy != "blue_green" || run.Status != "running" && run.Status != "queued" {
		return false
	}
	logRow, err := w.pipeline.store.FindLog(id)
	if err != nil {
		log.Printf("[worker] 蓝绿任务 %d 需要核对，但读取任务失败: %v", id, err)
		return true
	}
	logRow.Status = "running"
	logRow.CurrentStep = strPtr("needs_attention")
	logRow.ErrorMessage = &message
	_, _, err = w.pipeline.store.TransitionDeliveryRun(logRow, run.Version, DeliveryRunEvent{
		EventKey:  deliveryRunAttemptEventKey(logRow, "worker:needs-attention"),
		EventType: "recovery_required", Stage: "needs_attention", Status: "running", Detail: message,
	})
	if err != nil {
		// Keep the durable resource locks even when the database is unavailable.
		// Startup recovery will also notice the running in-flight blue-green run.
		log.Printf("[worker] 蓝绿任务 %d 状态核对持久化失败；保留资源锁: %v", id, err)
	}
	return true
}
