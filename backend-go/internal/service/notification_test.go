package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeployNotificationContainsActionableTaskLinkWithoutRawError(t *testing.T) {
	store := NewStore()
	now := time.Now().Truncate(time.Second)
	step := "apply_image"
	image := "registry.example.com/shop/api@sha256:" + strings.Repeat("ab", 32)
	secretError := "authorization: bearer should-never-be-in-notification"
	_, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "shop-api", DeploymentName: "shop-api-v2", ContainerName: "api", Namespace: "production",
		Status: "failed", CurrentStep: &step, ImageFull: &image, ErrorMessage: &secretError, FinishedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewNotifierWithPublicURL(store, "https://receiver.example/hook", strings.Repeat("s", 32), false, "", "https://deploy.example.com/platform/"); err != nil {
		t.Fatal(err)
	}
	store.EnableNotificationCapture()
	if err := store.CaptureNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	rows := store.Notifications()
	if len(rows) != 1 {
		t.Fatalf("notifications=%d, want 1", len(rows))
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(rows[0].Payload), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if got := data["task_url"]; got != "https://deploy.example.com/platform/tasks/logs/1" {
		t.Fatalf("task_url=%v", got)
	}
	if data["environment_code"] != "local" || data["environment_name"] != "本地环境" || data["environment_tier"] != "non_production" {
		t.Fatalf("environment metadata missing: %+v", data)
	}
	if data["failure_step"] != step || data["next_step"] == "" {
		t.Fatalf("failure guidance missing: %+v", data)
	}
	if data["app_name"] != "shop-api" || data["namespace"] != "production" || data["deployment_name"] != "shop-api-v2" || data["container_name"] != "api" {
		t.Fatalf("target metadata missing: %+v", data)
	}
	if strings.Contains(rows[0].Payload, secretError) || strings.Contains(rows[0].Payload, "should-never-be-in-notification") {
		t.Fatal("raw task error leaked into notification payload")
	}
}

func TestDeployNotificationUsesRelativeTaskLinkWithoutPublicURL(t *testing.T) {
	store := NewStore()
	logRow, err := store.AddLog(DeployLog{ClusterID: 1, AppName: "shop-api", Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := logNotification(logRow, store.Clusters()[0], "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &envelope); err != nil {
		t.Fatal(err)
	}
	if got, want := envelope.Data["task_url"], "/tasks/logs/1"; got != want {
		t.Fatalf("task_url=%v, want %q", got, want)
	}
	if _, ok := envelope.Data["failure_step"]; ok {
		t.Fatalf("successful task has failure_step: %+v", envelope.Data)
	}
}

func TestNotifierRejectsUnsafePlatformPublicURL(t *testing.T) {
	for _, value := range []string{
		"http://deploy.example.com",
		"https://user:pass@deploy.example.com",
		"https://deploy.example.com/path?token=secret",
		"https://deploy.example.com/#fragment",
		"https://:8443",
		"/relative/path",
	} {
		if _, err := NewNotifierWithPublicURL(NewStore(), "https://receiver.example/hook", strings.Repeat("x", 32), false, "", value); err == nil {
			t.Errorf("accepted unsafe PLATFORM_PUBLIC_URL %q", value)
		}
	}
	if _, err := NewNotifierWithPublicURL(NewStore(), "https://receiver.example/hook", strings.Repeat("x", 32), true, "", "http://localhost:3000"); err != nil {
		t.Fatalf("mock HTTP UI URL rejected: %v", err)
	}
}

func TestNotificationSignatureRetryAndRestartDedup(t *testing.T) {
	s := NewStore()
	now := time.Now().Truncate(time.Second)
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	l, err := s.AddLog(DeployLog{ClusterID: 1, AppName: "app", Status: "failed", ImageFull: &image, FinishedAt: &now})
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 32)
	calls := 0
	var stableID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-Deploy-Timestamp") + "." + string(body)))
		if r.Header.Get("X-Deploy-Signature") != "v1="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("incorrect signature")
		}
		id := r.Header.Get("X-Deploy-Event-ID")
		if stableID != "" && stableID != id {
			t.Error("retry event ID changed")
		}
		stableID = id
		if calls == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	n, err := NewNotifier(s, server.URL, secret, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = n.dispatch(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	rows := s.Notifications()
	if len(rows) != 1 || rows[0].Status != "pending" || rows[0].Attempts != 1 || rows[0].Event != "deploy_failed" {
		t.Fatalf("retry not persisted: %+v", rows)
	}
	if err = n.dispatch(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("backoff ignored")
	}
	if err = n.dispatch(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.Notifications()[0].Status != "delivered" {
		t.Fatal("ack not saved")
	}
	restarted := NewStoreWithData(s.Clusters(), nil, []DeployLog{l}, nil, nil)
	restarted.LoadNotifications(s.Notifications())
	n2, _ := NewNotifier(restarted, server.URL, secret, true)
	if err = n2.dispatch(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("restart redelivered acknowledged event: %d", calls)
	}
}

func TestNotificationHTTPSWithConfiguredPrivateCA(t *testing.T) {
	secret := strings.Repeat("p", 32)
	received := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	caFile := filepath.Join(t.TempDir(), "webhook-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	if _, err := store.AddLog(DeployLog{ClusterID: 1, AppName: "private-ca", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	notifier, err := NewNotifierWithCA(store, server.URL, secret, false, caFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.dispatch(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("HTTPS notification did not reach the private-CA receiver")
	}
	if got := store.Notifications()[0].Status; got != "delivered" {
		t.Fatalf("notification status=%q, want delivered", got)
	}
}

func TestNotifierRejectsInvalidConfiguredCA(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(caFile, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNotifierWithCA(NewStore(), "https://receiver.example/hook", strings.Repeat("s", 32), false, caFile); err == nil {
		t.Fatal("invalid CA bundle was accepted")
	}
}

func TestNotificationDeadLetterAndNoRedirect(t *testing.T) {
	calls := 0
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
	defer dest.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", dest.URL); w.WriteHeader(302) }))
	defer redirect.Close()
	s := NewStore()
	now := time.Now()
	_, _ = s.AddLog(DeployLog{ClusterID: 1, Status: "success"})
	n, _ := NewNotifier(s, redirect.URL, strings.Repeat("s", 32), true)
	for i := 0; i < 8; i++ {
		if err := n.dispatch(context.Background(), now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	row := s.Notifications()[0]
	if row.Status != "dead" || row.Attempts != 8 || row.LastError != "http_status=302" || calls != 0 {
		t.Fatalf("dead letter/redirect: %+v calls=%d", row, calls)
	}
	for _, url := range []string{"http://host/hook", "https://user:secret@host/hook", "bad"} {
		if _, err := NewNotifier(s, url, strings.Repeat("s", 32), false); err == nil {
			t.Fatalf("invalid config accepted: %s", url)
		}
	}
}

type fakeNotificationQueuePersistence struct {
	failPersistence
	rows        map[string]DeliveryNotification
	insertCalls int
	dueCalls    int
}

func (p *fakeNotificationQueuePersistence) Notifications() ([]DeliveryNotification, error) {
	rows := make([]DeliveryNotification, 0, len(p.rows))
	for _, row := range p.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (p *fakeNotificationQueuePersistence) SaveNotification(row DeliveryNotification) error {
	if p.rows == nil {
		p.rows = map[string]DeliveryNotification{}
	}
	p.rows[row.ID] = row
	return nil
}

func (p *fakeNotificationQueuePersistence) DueNotifications(_ context.Context, now time.Time, limit int) ([]DeliveryNotification, error) {
	p.dueCalls++
	rows := make([]DeliveryNotification, 0, limit)
	for _, row := range p.rows {
		if row.Status == "pending" && !row.NextAttemptAt.After(now) {
			rows = append(rows, row)
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (p *fakeNotificationQueuePersistence) InsertNotificationIfAbsent(_ context.Context, row DeliveryNotification) (bool, error) {
	p.insertCalls++
	if p.rows == nil {
		p.rows = map[string]DeliveryNotification{}
	}
	if _, exists := p.rows[row.ID]; exists {
		return false, nil
	}
	p.rows[row.ID] = row
	return true, nil
}

func (p *fakeNotificationQueuePersistence) RetryNotification(id string, now time.Time) error {
	row, ok := p.rows[id]
	if !ok {
		return ErrNotFound
	}
	if row.Status != "dead" {
		return newConflict("仅失败通知可重新投递")
	}
	row.Status, row.Attempts, row.LastError, row.NextAttemptAt = "pending", 0, "", now
	p.rows[id] = row
	return nil
}

func TestPersistentNotifierReadsBoundedQueueWithoutCachingHistory(t *testing.T) {
	persistence := &fakeNotificationQueuePersistence{rows: map[string]DeliveryNotification{}}
	now := time.Now().UTC().Truncate(time.Second)
	store := NewStoreWithData(nil, nil, []DeployLog{{ID: 41, ClusterID: 1, AppName: "orders", Status: "running", CreatedAt: now}}, nil, persistence)
	secret := strings.Repeat("q", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	notifier, err := NewNotifier(store, server.URL, secret, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.dispatch(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	terminal, err := store.FindLog(41)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Status = "success"
	terminal.FinishedAt = &now
	if err := store.UpdateLogExecution(terminal); err != nil {
		t.Fatal(err)
	}
	if err := notifier.dispatch(context.Background(), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := notifier.dispatch(context.Background(), now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || persistence.insertCalls != 1 || persistence.dueCalls != 3 {
		t.Fatalf("notifier did not use one-time source capture and bounded queue reads: calls=%d inserts=%d due_reads=%d", calls, persistence.insertCalls, persistence.dueCalls)
	}
	if len(store.Notifications()) != 0 {
		t.Fatalf("persistent outbox was copied into process memory: %+v", store.Notifications())
	}
	history, err := store.NotificationHistory()
	if err != nil || len(history) != 1 || history[0].Status != "delivered" {
		t.Fatalf("durable notification history=%+v err=%v", history, err)
	}
}

func TestNotificationAlertCursorUsesStableTimestampAndIDKeyset(t *testing.T) {
	store := NewStore()
	createdAt := time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC)
	store.LoadNotifications([]DeliveryNotification{
		{ID: "a", Status: "dead", CreatedAt: createdAt},
		{ID: "b", Status: "pending", CreatedAt: createdAt},
		{ID: "c", Status: "dead", CreatedAt: createdAt},
		{ID: "d", Status: "delivered", CreatedAt: createdAt},
	})

	first, err := store.NotificationAlertsPage(context.Background(), "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "c" || !first.HasMore || first.NextCursor == "c" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	decoded, err := decodeNotificationCursor(first.NextCursor)
	if err != nil || decoded.ID != "c" || !decoded.CreatedAt.Equal(createdAt) {
		t.Fatalf("cursor did not retain its stable sort key: %+v err=%v", decoded, err)
	}

	rows := store.Notifications()
	for i := range rows {
		if rows[i].ID == "c" {
			rows[i].Status = "delivered"
		}
	}
	store.LoadNotifications(rows)
	second, err := store.NotificationAlertsPage(context.Background(), first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "b" || !second.HasMore {
		t.Fatalf("second page after cursor row delivery=%+v err=%v", second, err)
	}

	if _, err := store.NotificationAlertsPage(context.Background(), "missing", 1); !errors.Is(err, ErrInvalidNotificationCursor) {
		t.Fatalf("malformed cursor error=%v", err)
	}
}

type failNotificationPersistence struct{ failPersistence }

func (failNotificationPersistence) Notifications() ([]DeliveryNotification, error) { return nil, nil }
func (failNotificationPersistence) SaveNotification(DeliveryNotification) error {
	return errors.New("offline")
}
func TestNotificationDoesNotSendBeforeDurableCapture(t *testing.T) {
	s := NewStoreWithData(nil, nil, []DeployLog{{ID: 1, Status: "success"}}, nil, failNotificationPersistence{})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	n, _ := NewNotifier(s, server.URL, strings.Repeat("s", 32), true)
	if err := n.dispatch(context.Background(), time.Now()); err == nil || calls != 0 || len(s.Notifications()) != 0 {
		t.Fatalf("non-durable send calls=%d err=%v", calls, err)
	}
}

func TestDeliveryMetricsUnknownDORAAndTerminalDenominator(t *testing.T) {
	now := time.Now()
	created := now.Add(-time.Hour)
	start := now.Add(-10 * time.Second)
	s := NewStoreWithData([]Cluster{{ID: 1, EnvironmentTier: "production"}, {ID: 2, EnvironmentTier: "non_production"}}, nil, []DeployLog{
		{ID: 1, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: &start, FinishedAt: &now},
		{ID: 2, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "failed", CreatedAt: created, StartedAt: &start, FinishedAt: &now},
		{ID: 3, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "pending", CreatedAt: created},
		{ID: 4, ClusterID: 2, EnvironmentTierSnapshot: strPtr("non_production"), Status: "failed", CreatedAt: created},
	}, nil, nil)
	m := s.DeliveryMetricsForWindow("", 30, now)
	if m.ProductionAttempts != 3 || m.ActiveTasks != 1 || m.ExecutionFailureRate == nil || *m.ExecutionFailureRate != .5 || *m.ExecutionP95Seconds != 10 {
		t.Fatalf("wrong denominator: %+v", m)
	}
	if m.WindowDays != 30 || !m.AsOf.Equal(now.UTC()) || !m.WindowStart.Equal(now.UTC().Add(-30*24*time.Hour)) || m.DurationSamples != 2 {
		t.Fatalf("missing metric time boundary/sample count: %+v", m)
	}
	for name, v := range m.DORA {
		if v.Value != nil || v.MissingData == "" {
			t.Fatalf("fabricated DORA %s: %+v", name, v)
		}
	}
}

func TestDeliveryMetricsAggregateOperatorIncidentRecoveryWithoutClaimingDORA(t *testing.T) {
	asOf := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	firstOccurred, firstIntervention, firstRecovery := asOf.Add(-6*time.Hour), asOf.Add(-5*time.Hour), asOf.Add(-4*time.Hour)
	secondOccurred, secondRecovery := asOf.Add(-2*time.Hour), asOf.Add(-time.Hour)
	unresolvedOccurred := asOf.Add(-30 * time.Minute)
	futureRecovery := asOf.Add(time.Hour)
	futureRecoveryOccurred := asOf.Add(-3 * time.Hour)
	oldOccurred, otherAppOccurred, otherTargetOccurred := asOf.Add(-8*24*time.Hour), asOf.Add(-time.Hour), asOf.Add(-time.Hour)
	remediationLogID := int64(101)
	store := NewStore()
	store.LoadDeliveryIncidents([]DeliveryIncident{
		{ID: 1, ClusterID: 1, AppName: "shop", Status: "recovered", OccurredAt: firstOccurred, InterventionStartedAt: &firstIntervention, RecoveryOccurredAt: &firstRecovery},
		{ID: 2, ClusterID: 1, AppName: "shop", Status: "recovered", OccurredAt: secondOccurred, RecoveryOccurredAt: &secondRecovery},
		{ID: 3, ClusterID: 1, AppName: "shop", Status: "open", OccurredAt: unresolvedOccurred},
		{ID: 4, ClusterID: 1, AppName: "shop", Status: "recovered", OccurredAt: futureRecoveryOccurred, RecoveryOccurredAt: &futureRecovery},
		{ID: 5, ClusterID: 1, AppName: "shop", Status: "recovered", OccurredAt: oldOccurred, RecoveryOccurredAt: &secondRecovery},
		{ID: 6, ClusterID: 1, AppName: "other", Status: "recovered", OccurredAt: otherAppOccurred, RecoveryOccurredAt: &secondRecovery},
		{ID: 7, ClusterID: 2, AppName: "shop", Status: "recovered", OccurredAt: otherTargetOccurred, RecoveryOccurredAt: &secondRecovery},
	}, []DeliveryIncidentEvent{
		{ID: 1, IncidentID: 1, EventType: "causality_assessed", CausalityStatus: DeliveryIncidentNotCausedByDeployment},
		{ID: 2, IncidentID: 1, EventType: "causality_assessed", CausalityStatus: DeliveryIncidentCausedByDeployment},
		{ID: 3, IncidentID: 1, EventType: "remediation_classified", RemediationType: DeliveryIncidentRemediationHotfix, RemediationPlanStatus: DeliveryIncidentRemediationPlanned},
		{ID: 4, IncidentID: 1, EventType: "remediation_classified", RemediationType: DeliveryIncidentRemediationRollback, RemediationDeployLogID: &remediationLogID, RemediationPlanStatus: DeliveryIncidentRemediationUnplanned},
		{ID: 5, IncidentID: 3, EventType: "causality_assessed", CausalityStatus: DeliveryIncidentNotCausedByDeployment},
		{ID: 6, IncidentID: 3, EventType: "remediation_classified", RemediationType: DeliveryIncidentRemediationNoAction, RemediationPlanStatus: DeliveryIncidentRemediationNotApplicable},
		{ID: 7, IncidentID: 4, EventType: "remediation_classified", RemediationType: DeliveryIncidentRemediationHotfix, RemediationPlanStatus: DeliveryIncidentRemediationPlanned},
	})
	samples, err := store.deliveryIncidentRecoverySamples("shop", 1, asOf.Add(-7*24*time.Hour))
	if err != nil || len(samples) != 4 || samples[0].CausalityStatus != DeliveryIncidentCausedByDeployment || samples[0].RemediationType != DeliveryIncidentRemediationRollback || samples[0].RemediationDeployLogID == nil || *samples[0].RemediationDeployLogID != remediationLogID {
		t.Fatalf("in-memory incident samples did not use the latest assessment events: samples=%+v err=%v", samples, err)
	}

	metrics := store.DeliveryMetricsForTarget("shop", 1, 7, asOf)
	incident := metrics.IncidentRecovery
	if incident.IncidentSamples != 4 || incident.RecoveredSamples != 2 || incident.UnrecoveredSamples != 2 || incident.InvalidTimestampSamples != 1 {
		t.Fatalf("incident samples were not scoped or invalid data was hidden: %+v", incident)
	}
	if incident.MeanRecoverySeconds == nil || *incident.MeanRecoverySeconds != 90*60 || incident.InterventionRecoverySamples != 1 || incident.MeanInterventionToRecoverySeconds == nil || *incident.MeanInterventionToRecoverySeconds != 60*60 {
		t.Fatalf("wrong observed recovery means or coverage: %+v", incident)
	}
	if incident.CausalityAssessedSamples != 2 || incident.CausalityPendingSamples != 2 || incident.RemediationClassifiedSamples != 2 || incident.RemediationPendingSamples != 2 {
		t.Fatalf("registered incident decision coverage is incomplete or misclassified: %+v", incident)
	}
	if got := metrics.DORA["failed_deployment_recovery_time"]; got.Value != nil || got.MissingData == "" {
		t.Fatalf("operator-linked incident data was misrepresented as DORA: %+v", got)
	}
}

func TestMetricsExposeInvalidTimesInsteadOfNegativeDurations(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	s := NewStoreWithData([]Cluster{{ID: 1, EnvironmentTier: "production"}}, nil, []DeployLog{{ID: 1, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "interrupted", CreatedAt: now, StartedAt: &future, FinishedAt: &now}}, nil, nil)
	m := s.DeliveryMetricsForWindow("", 30, now)
	if m.ExecutionP95Seconds != nil || m.InvalidTimestampSamples != 1 {
		t.Fatalf("invalid duration reported: %+v", m)
	}
}

func TestDeliveryMetricsCountMissingAndZeroExecutionTimestamps(t *testing.T) {
	asOf := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	created := asOf.Add(-time.Hour)
	started, finished := asOf.Add(-20*time.Second), asOf.Add(-10*time.Second)
	zero := time.Time{}
	store := NewStoreWithData(nil, nil, []DeployLog{
		{ID: 1, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: &started, FinishedAt: &finished},
		{ID: 2, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "failed", CreatedAt: created, FinishedAt: &finished},
		{ID: 3, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "interrupted", CreatedAt: created, StartedAt: &started},
		{ID: 4, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: &zero, FinishedAt: &finished},
		{ID: 5, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: &started, FinishedAt: &zero},
		{ID: 6, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: ptrTime(created.Add(-time.Second)), FinishedAt: ptrTime(created.Add(time.Second))},
	}, nil, nil)
	store.LoadPromotions([]PromotionRequest{{ID: 1, Status: "approved", CreatedAt: created}})

	metrics := store.DeliveryMetricsForWindow("", 30, asOf)
	if metrics.DurationSamples != 1 || metrics.ExecutionP95Seconds == nil || *metrics.ExecutionP95Seconds != 10 {
		t.Fatalf("only the complete execution should contribute duration: %+v", metrics)
	}
	if metrics.InvalidTimestampSamples != 6 || metrics.ApprovalWaitSamples != 0 {
		t.Fatalf("missing execution or approval timestamps should be reported and excluded: %+v", metrics)
	}
	if metrics.SuccessfulDeployments != 4 || metrics.ExecutionFailures != 2 || metrics.ExecutionFailureRate == nil || *metrics.ExecutionFailureRate != 1.0/3.0 {
		t.Fatalf("duration anomalies must not change status-based failure metrics: %+v", metrics)
	}
}

func TestDeliveryMetricsWindowFiltersAttemptsAndApprovalsByApp(t *testing.T) {
	asOf := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	createdSuccess := asOf.Add(-2 * 24 * time.Hour)
	createdFailure := asOf.Add(-3 * 24 * time.Hour)
	createdActive := asOf.Add(-24 * time.Hour)
	createdOld := asOf.Add(-45 * 24 * time.Hour)
	startSuccess, finishSuccess := asOf.Add(-20*time.Second), asOf.Add(-15*time.Second)
	startFailure, finishFailure := asOf.Add(-12*time.Second), asOf.Add(-4*time.Second)
	s := NewStoreWithData(
		[]Cluster{{ID: 1, EnvironmentTier: "production"}, {ID: 2, EnvironmentTier: "non_production"}}, nil,
		[]DeployLog{
			{ID: 1, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), AppName: "shop", CreatedAt: createdSuccess, Status: "success", StartedAt: &startSuccess, FinishedAt: &finishSuccess},
			{ID: 2, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), AppName: "shop", CreatedAt: createdFailure, Status: "failed", StartedAt: &startFailure, FinishedAt: &finishFailure},
			{ID: 3, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), AppName: "shop", CreatedAt: createdActive, Status: "pending"},
			{ID: 4, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), AppName: "shop", CreatedAt: createdOld, Status: "success"},
			{ID: 5, ClusterID: 2, EnvironmentTierSnapshot: strPtr("non_production"), AppName: "shop", CreatedAt: createdActive, Status: "success"},
		},
		[]ImageRelease{{ID: 1, AppName: "shop"}, {ID: 2, AppName: "other"}}, nil,
	)
	approvedCreated, approvedAt := asOf.Add(-72*time.Hour), asOf.Add(-48*time.Hour)
	rejectedCreated, rejectedAt := asOf.Add(-6*24*time.Hour), asOf.Add(-5*24*time.Hour)
	otherCreated, otherApprovedAt := asOf.Add(-24*time.Hour), asOf.Add(-12*time.Hour)
	s.LoadPromotions([]PromotionRequest{
		{ID: 1, ReleaseID: 1, Status: "approved", CreatedAt: approvedCreated, DecidedAt: &approvedAt},
		{ID: 2, ReleaseID: 1, Status: "pending", CreatedAt: createdActive},
		{ID: 3, ReleaseID: 1, Status: "rejected", CreatedAt: rejectedCreated, DecidedAt: &rejectedAt},
		{ID: 4, ReleaseID: 1, Status: "approved", CreatedAt: createdOld, DecidedAt: &createdOld},
		{ID: 5, ReleaseID: 2, Status: "approved", CreatedAt: otherCreated, DecidedAt: &otherApprovedAt},
	})

	metrics := s.DeliveryMetricsForWindow("shop", 7, asOf)
	if metrics.ProductionAttempts != 3 || metrics.SuccessfulDeployments != 1 || metrics.ExecutionFailures != 1 || metrics.ActiveTasks != 1 {
		t.Fatalf("attempt cohort was not filtered by window/production/app: %+v", metrics)
	}
	if metrics.DurationSamples != 2 || metrics.ExecutionP95Seconds == nil || *metrics.ExecutionP95Seconds != 8 {
		t.Fatalf("execution duration sample window = %+v", metrics)
	}
	if metrics.ApprovalWaitSamples != 2 || metrics.ApprovalWaitP95Seconds == nil || *metrics.ApprovalWaitP95Seconds != float64(24*60*60) || metrics.PendingApprovals != 1 {
		t.Fatalf("approval samples/window/app filter = %+v", metrics)
	}
	if metrics.DORA["deployment_frequency"].Value != nil {
		t.Fatalf("partial task count fabricated DORA deployment frequency: %+v", metrics.DORA["deployment_frequency"])
	}
}

func TestDeliveryMetricsForTargetScopesProductionAttemptsAndApprovals(t *testing.T) {
	asOf := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := asOf.Add(-time.Hour)
	started, finished := asOf.Add(-30*time.Second), asOf.Add(-20*time.Second)
	s := NewStoreWithData(
		[]Cluster{{ID: 1, EnvironmentTier: "production"}, {ID: 2, EnvironmentTier: "production"}}, nil,
		[]DeployLog{
			{ID: 1, ClusterID: 1, AppName: "shop", EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, StartedAt: &started, FinishedAt: &finished},
			{ID: 2, ClusterID: 2, AppName: "shop", EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created, FinishedAt: &finished},
			{ID: 3, ClusterID: 1, AppName: "shop", EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: asOf.Add(-8 * 24 * time.Hour), FinishedAt: ptrTime(asOf.Add(-6 * 24 * time.Hour))},
			{ID: 4, ClusterID: 1, AppName: "shop", EnvironmentTierSnapshot: strPtr("non_production"), Status: "success", CreatedAt: created, FinishedAt: &finished},
		},
		[]ImageRelease{{ID: 1, AppName: "shop"}}, nil,
	)
	decided := asOf.Add(-30 * time.Minute)
	s.LoadPromotions([]PromotionRequest{
		{ID: 1, ReleaseID: 1, ProdClusterID: 1, Status: "approved", CreatedAt: created, DecidedAt: &decided},
		{ID: 2, ReleaseID: 1, ProdClusterID: 2, Status: "pending", CreatedAt: created},
	})

	metrics := s.DeliveryMetricsForTarget("shop", 1, 7, asOf)
	if metrics.ClusterID != 1 || metrics.ProductionAttempts != 1 || metrics.SuccessfulDeployments != 1 || metrics.ExecutionFailures != 0 {
		t.Fatalf("target execution metrics were not scoped: %+v", metrics)
	}
	if metrics.ApprovalWaitSamples != 1 || metrics.PendingApprovals != 0 || metrics.ApprovalWaitP95Seconds == nil {
		t.Fatalf("target approval metrics were not scoped: %+v", metrics)
	}
	if metrics.DORA["deployment_frequency"].Value == nil || *metrics.DORA["deployment_frequency"].Value != 2 {
		t.Fatalf("target-scoped successful deployment frequency = %+v", metrics.DORA["deployment_frequency"])
	}
}

func TestDeliveryFrequencyRequiresExactScopeAndCompleteDeploymentTimes(t *testing.T) {
	asOf := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := asOf.Add(-time.Hour)
	store := NewStoreWithData(
		[]Cluster{{ID: 1, EnvironmentTier: "production"}}, nil,
		[]DeployLog{{ID: 1, ClusterID: 1, AppName: "shop", EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created}},
		nil, nil,
	)

	allTargets := store.DeliveryMetricsForWindow("shop", 7, asOf)
	if got := allTargets.DORA["deployment_frequency"]; got.Value != nil || got.MissingData == "" {
		t.Fatalf("cross-target frequency should remain unknown: %+v", got)
	}
	missingFinish := store.DeliveryMetricsForTarget("shop", 1, 7, asOf)
	if got := missingFinish.DORA["deployment_frequency"]; got.Value != nil || got.MissingData == "" {
		t.Fatalf("missing completion time should remain unknown: %+v", got)
	}
}

func TestDeliveryMetricsKeepCreationTimeEnvironmentTier(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	store := NewStoreWithData(
		[]Cluster{{ID: 1, EnvironmentTier: "production"}}, nil,
		[]DeployLog{
			{ID: 1, ClusterID: 1, EnvironmentTierSnapshot: strPtr("production"), Status: "success", CreatedAt: created},
			{ID: 2, ClusterID: 1, EnvironmentTierSnapshot: strPtr("non_production"), Status: "success", CreatedAt: created},
			{ID: 3, ClusterID: 1, Status: "success", CreatedAt: created},
		}, nil, nil,
	)

	metrics := store.DeliveryMetricsForWindow("", 7, now)
	if metrics.ProductionAttempts != 1 || metrics.SuccessfulDeployments != 1 || metrics.UnknownTierSamples != 1 {
		t.Fatalf("metrics reclassified historical attempts or hid unknown tier rows: %+v", metrics)
	}
	if metrics.ExecutionFailureRate == nil || *metrics.ExecutionFailureRate != 0 {
		t.Fatalf("production success should form a zero-failure observed sample: %+v", metrics)
	}
}

func TestDeployLogSnapshotsClusterTierAtCreation(t *testing.T) {
	store := NewStore()
	cluster, err := store.FindCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	cluster.EnvironmentTier = "production"
	cluster.DeployPolicy = "manual"
	if _, err := store.UpdateCluster(cluster); err != nil {
		t.Fatal(err)
	}

	logRow, err := store.AddDeployLog(DeployLog{
		ClusterID: 1, AppName: "shop", Namespace: "prod", DeploymentName: "shop-api",
		ContainerName: "web", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if logRow.EnvironmentTierSnapshot == nil || *logRow.EnvironmentTierSnapshot != "production" {
		t.Fatalf("creation-time tier was not captured: %+v", logRow.EnvironmentTierSnapshot)
	}

	cluster.EnvironmentTier = "non_production"
	if _, err := store.UpdateCluster(cluster); err != nil {
		t.Fatal(err)
	}
	metrics := store.DeliveryMetricsForWindow("shop", 7, time.Now())
	if metrics.ProductionAttempts != 1 {
		t.Fatalf("cluster tier update reclassified the existing production task: %+v", metrics)
	}
}
