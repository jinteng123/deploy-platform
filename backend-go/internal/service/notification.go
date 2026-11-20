package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DeliveryNotification is a durable, deduplicated delivery attempt. Payload is
// immutable; receivers must deduplicate the stable event ID (at least once).
type DeliveryNotification struct {
	ID            string    `json:"id"`
	Event         string    `json:"event"`
	Summary       string    `json:"summary,omitempty"`
	Payload       string    `json:"-"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	CreatedAt     time.Time `json:"created_at"`
	LastError     string    `json:"last_error,omitempty"`
}
type DeliveryNotificationPage struct {
	Items      []DeliveryNotification `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
	HasMore    bool                   `json:"has_more"`
}
type DeliveryNotificationCursor struct {
	CreatedAt time.Time
	ID        string
}

var ErrInvalidNotificationCursor = errors.New("invalid notification cursor")

type NotificationPersistence interface {
	Notifications() ([]DeliveryNotification, error)
	SaveNotification(DeliveryNotification) error
}
type NotificationAlertPagePersistence interface {
	NotificationAlertsPage(context.Context, *DeliveryNotificationCursor, int) ([]DeliveryNotification, error)
}
type NotificationQueuePersistence interface {
	DueNotifications(context.Context, time.Time, int) ([]DeliveryNotification, error)
	InsertNotificationIfAbsent(context.Context, DeliveryNotification) (bool, error)
	RetryNotification(string, time.Time) error
}

func (s *Store) LoadNotifications(rows []DeliveryNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = rows
}
func (s *Store) Notifications() []DeliveryNotification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]DeliveryNotification{}, s.notifications...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	for i := range out {
		out[i].Summary = deliveryNotificationSummary(out[i])
	}
	return out
}

func (s *Store) NotificationHistory() ([]DeliveryNotification, error) {
	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence != nil {
		reader, ok := persistence.(NotificationPersistence)
		if !ok {
			return nil, fmt.Errorf("通知存储不可用")
		}
		rows, err := reader.Notifications()
		if err != nil {
			return nil, err
		}
		return summarizeDeliveryNotifications(rows), nil
	}
	return s.Notifications(), nil
}

func (s *Store) DueNotifications(ctx context.Context, now time.Time, limit int) ([]DeliveryNotification, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("notification batch limit must be between 1 and 100")
	}
	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence != nil {
		queue, ok := persistence.(NotificationQueuePersistence)
		if !ok {
			return nil, fmt.Errorf("通知存储不支持数据库队列查询")
		}
		return queue.DueNotifications(ctx, now, limit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows := s.Notifications()
	due := make([]DeliveryNotification, 0, limit)
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if row.Status != "pending" || row.NextAttemptAt.After(now) {
			continue
		}
		due = append(due, row)
		if len(due) == limit {
			break
		}
	}
	return due, nil
}

func (s *Store) EnableNotificationCapture() {
	s.mu.Lock()
	s.notificationCaptureEnabled = true
	s.mu.Unlock()
}

func (s *Store) notificationCaptureFailureVersion() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notificationCaptureFailures
}

type notificationCursorToken struct {
	Version   int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeNotificationCursor(cursor DeliveryNotificationCursor) string {
	raw, _ := json.Marshal(notificationCursorToken{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), ID: cursor.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeNotificationCursor(value string) (*DeliveryNotificationCursor, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidNotificationCursor
	}
	var token notificationCursorToken
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil || token.Version != 1 || token.CreatedAt.IsZero() || token.ID == "" || len(token.ID) > 128 {
		return nil, ErrInvalidNotificationCursor
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidNotificationCursor
	}
	return &DeliveryNotificationCursor{CreatedAt: token.CreatedAt, ID: token.ID}, nil
}

// NotificationAlertsPage returns pending/dead notifications newest first.
// Persistent stores use keyset pagination so page reads do not scan the
// in-memory outbox or depend on the cursor row remaining active.
func (s *Store) NotificationAlertsPage(ctx context.Context, cursor string, limit int) (DeliveryNotificationPage, error) {
	if limit < 1 || limit > 100 {
		return DeliveryNotificationPage{}, fmt.Errorf("limit must be between 1 and 100")
	}
	decoded, err := decodeNotificationCursor(cursor)
	if err != nil {
		return DeliveryNotificationPage{}, err
	}
	s.mu.RLock()
	paged, supportsPaging := s.persistence.(NotificationAlertPagePersistence)
	s.mu.RUnlock()
	if supportsPaging {
		rows, err := paged.NotificationAlertsPage(ctx, decoded, limit+1)
		if err != nil {
			return DeliveryNotificationPage{}, err
		}
		return buildNotificationPage(summarizeDeliveryNotifications(rows), limit), nil
	}
	if err := ctx.Err(); err != nil {
		return DeliveryNotificationPage{}, err
	}
	rows := s.Notifications()
	items := make([]DeliveryNotification, 0, limit+1)
	for _, row := range rows {
		if decoded != nil && !notificationBeforeCursor(row, *decoded) {
			continue
		}
		if row.Status != "pending" && row.Status != "dead" {
			continue
		}
		items = append(items, row)
		if len(items) > limit {
			break
		}
	}
	return buildNotificationPage(summarizeDeliveryNotifications(items), limit), nil
}

func summarizeDeliveryNotifications(rows []DeliveryNotification) []DeliveryNotification {
	for i := range rows {
		rows[i].Summary = deliveryNotificationSummary(rows[i])
	}
	return rows
}

func deliveryNotificationSummary(row DeliveryNotification) string {
	if row.Event != "image_drift_detected" && row.Event != "image_drift_recovered" {
		return ""
	}
	var payload struct {
		Data struct {
			ClusterID  int    `json:"cluster_id"`
			AppName    string `json:"app_name"`
			Namespace  string `json:"namespace"`
			Deployment string `json:"deployment"`
			Container  string `json:"container"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return ""
	}
	data := payload.Data
	appName := strings.TrimSpace(data.AppName)
	if appName == "" || data.ClusterID <= 0 || strings.TrimSpace(data.Namespace) == "" || strings.TrimSpace(data.Deployment) == "" || strings.TrimSpace(data.Container) == "" {
		return ""
	}
	return fmt.Sprintf("%s · 集群 #%d · %s/%s/%s", appName, data.ClusterID, data.Namespace, data.Deployment, data.Container)
}

func notificationBeforeCursor(row DeliveryNotification, cursor DeliveryNotificationCursor) bool {
	if row.CreatedAt.Equal(cursor.CreatedAt) {
		return row.ID < cursor.ID
	}
	return row.CreatedAt.Before(cursor.CreatedAt)
}

func buildNotificationPage(rows []DeliveryNotification, limit int) DeliveryNotificationPage {
	items := rows
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if items == nil {
		items = []DeliveryNotification{}
	}
	page := DeliveryNotificationPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		page.NextCursor = encodeNotificationCursor(DeliveryNotificationCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page
}
func (s *Store) saveNotification(n DeliveryNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistence != nil {
		p, ok := s.persistence.(NotificationPersistence)
		if !ok {
			return fmt.Errorf("通知存储不可用")
		}
		return p.SaveNotification(n)
	}
	for i, old := range s.notifications {
		if old.ID == n.ID {
			s.notifications[i] = n
			return nil
		}
	}
	s.notifications = append(s.notifications, n)
	return nil
}

func notificationEnvelope(id, event string, body any, at, now time.Time) (DeliveryNotification, error) {
	payload, err := json.Marshal(struct {
		ID         string    `json:"id"`
		Event      string    `json:"event"`
		OccurredAt time.Time `json:"occurred_at"`
		Data       any       `json:"data"`
	}{id, event, at, body})
	if err != nil {
		return DeliveryNotification{}, err
	}
	return DeliveryNotification{ID: id, Event: event, Payload: string(payload), Status: "pending", NextAttemptAt: now, CreatedAt: at}, nil
}

func (s *Store) insertNotificationIfAbsent(ctx context.Context, row DeliveryNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertNotificationIfAbsentLocked(ctx, row)
}

func (s *Store) insertNotificationIfAbsentLocked(ctx context.Context, row DeliveryNotification) error {
	if s.persistence != nil {
		queue, ok := s.persistence.(NotificationQueuePersistence)
		if !ok {
			s.notificationCaptureFailures++
			return fmt.Errorf("通知存储不支持幂等事件写入")
		}
		if _, err := queue.InsertNotificationIfAbsent(ctx, row); err != nil {
			s.notificationCaptureFailures++
			return err
		}
		return nil
	}
	for _, existing := range s.notifications {
		if existing.ID == row.ID {
			return nil
		}
	}
	s.notifications = append(s.notifications, row)
	return nil
}

func logNotification(logRow DeployLog, cluster Cluster, publicURL string, now time.Time) (DeliveryNotification, error) {
	if !isTerminalDeployStatus(logRow.Status) {
		return DeliveryNotification{}, fmt.Errorf("deploy notification source is not terminal")
	}
	event := "deploy_finished"
	if logRow.Status != "success" {
		event = "deploy_failed"
	}
	at := logRow.CreatedAt
	if logRow.FinishedAt != nil {
		at = *logRow.FinishedAt
	}
	taskURL := fmt.Sprintf("/tasks/logs/%d", logRow.ID)
	if publicURL != "" {
		taskURL = fmt.Sprintf("%s%s", publicURL, taskURL)
	}
	failureStep := ""
	if logRow.Status != "success" && logRow.CurrentStep != nil {
		failureStep = *logRow.CurrentStep
	}
	nextStep := "无需操作。"
	if logRow.Status == "interrupted" {
		nextStep = "打开任务详情核对集群实际状态；确认未知写入前不要直接重试。"
	} else if logRow.Status != "success" {
		nextStep = "打开任务详情查看失败步骤和目标状态，修复原因后再重新发起。"
	}
	environmentTier := ""
	if logRow.EnvironmentTierSnapshot != nil {
		environmentTier = *logRow.EnvironmentTierSnapshot
	}
	body := struct {
		LogID           int64   `json:"log_id"`
		ClusterID       int     `json:"cluster_id"`
		EnvironmentCode string  `json:"environment_code,omitempty"`
		EnvironmentName string  `json:"environment_name,omitempty"`
		EnvironmentTier string  `json:"environment_tier,omitempty"`
		AppName         string  `json:"app_name"`
		Namespace       string  `json:"namespace,omitempty"`
		DeploymentName  string  `json:"deployment_name,omitempty"`
		ContainerName   string  `json:"container_name,omitempty"`
		Status          string  `json:"status"`
		Image           *string `json:"image"`
		TaskURL         string  `json:"task_url"`
		FailureStep     string  `json:"failure_step,omitempty"`
		NextStep        string  `json:"next_step"`
	}{
		LogID:           logRow.ID,
		ClusterID:       logRow.ClusterID,
		EnvironmentCode: cluster.EnvCode,
		EnvironmentName: cluster.Name,
		EnvironmentTier: environmentTier,
		AppName:         logRow.AppName,
		Namespace:       logRow.Namespace,
		DeploymentName:  logRow.DeploymentName,
		ContainerName:   logRow.ContainerName,
		Status:          logRow.Status,
		Image:           logRow.ImageFull,
		TaskURL:         taskURL,
		FailureStep:     failureStep,
		NextStep:        nextStep,
	}
	return notificationEnvelope(fmt.Sprintf("deploy:%d:%s", logRow.ID, logRow.Status), event, body, at, now)
}

func promotionNotification(p PromotionRequest, now time.Time) (DeliveryNotification, error) {
	at := p.CreatedAt
	if p.DecidedAt != nil {
		at = *p.DecidedAt
	}
	body := struct {
		RequestID int64  `json:"request_id"`
		ReleaseID int64  `json:"release_id"`
		ClusterID int    `json:"cluster_id"`
		LogID     *int64 `json:"log_id"`
	}{p.ID, p.ReleaseID, p.ProdClusterID, p.DeployLogID}
	return notificationEnvelope(fmt.Sprintf("approval:%d", p.ID), "promote_approved", body, at, now)
}

// DeliveryDriftNotification creates alerts only for a confirmed transition
// into drift or back to the expected image. Repeated observations and unknown
// reads do not create notification noise.
func DeliveryDriftNotification(event DeliveryDriftEvent, now time.Time) (DeliveryNotification, bool, error) {
	notificationEvent := ""
	switch {
	case event.Status == "drifted" && event.PreviousStatus != "drifted":
		notificationEvent = "image_drift_detected"
	case event.Status == "in_sync" && event.DriftDurationSecs != nil:
		notificationEvent = "image_drift_recovered"
	default:
		return DeliveryNotification{}, false, nil
	}
	if event.ID <= 0 {
		return DeliveryNotification{}, false, NewValidationError("漂移通知缺少稳定的历史事件 ID")
	}
	at := event.ObservedAt
	if at.IsZero() {
		at = now
	}
	body := struct {
		DriftEventID    int64      `json:"drift_event_id"`
		ChangeKind      string     `json:"change_kind"`
		ClusterID       int        `json:"cluster_id"`
		AppID           int        `json:"app_id"`
		AppName         string     `json:"app_name"`
		Namespace       string     `json:"namespace"`
		Deployment      string     `json:"deployment"`
		Container       string     `json:"container"`
		ExpectedImage   *string    `json:"expected_image"`
		ActualImage     *string    `json:"actual_image"`
		DriftStartedAt  *time.Time `json:"drift_started_at,omitempty"`
		DurationSeconds *int64     `json:"drift_duration_seconds,omitempty"`
		ObservedBy      string     `json:"observed_by"`
		Source          string     `json:"source"`
	}{
		DriftEventID: event.ID, ChangeKind: event.ChangeKind, ClusterID: event.ClusterID,
		AppID: event.AppID, AppName: event.AppName, Namespace: event.Namespace,
		Deployment: event.Workload, Container: event.Container, ExpectedImage: copyString(event.ExpectedImage),
		ActualImage: copyString(event.ActualImage), DriftStartedAt: copyTime(event.DriftStartedAt),
		DurationSeconds: copyInt64(event.DriftDurationSecs), ObservedBy: event.ObservedBy, Source: event.Source,
	}
	row, err := notificationEnvelope(fmt.Sprintf("drift:%d", event.ID), notificationEvent, body, at, now)
	if err != nil {
		return DeliveryNotification{}, false, err
	}
	return row, true, nil
}

func (s *Store) captureTerminalLogLocked(logRow DeployLog) {
	if !s.notificationCaptureEnabled || !isTerminalDeployStatus(logRow.Status) {
		return
	}
	cluster := Cluster{}
	for _, candidate := range s.clusters {
		if candidate.ID == logRow.ClusterID {
			cluster = candidate
			break
		}
	}
	row, err := logNotification(logRow, cluster, s.notificationPublicURL, time.Now())
	if err != nil {
		log.Printf("[notification] 捕获发布事件失败: %v", err)
		s.notificationCaptureFailures++
		return
	}
	if err := s.insertNotificationIfAbsentLocked(context.Background(), row); err != nil {
		log.Printf("[notification] 捕获发布事件失败: %v", err)
	}
}

func (s *Store) captureApprovedPromotionLocked(p PromotionRequest) {
	if !s.notificationCaptureEnabled || p.Status != "approved" {
		return
	}
	row, err := promotionNotification(p, time.Now())
	if err != nil {
		log.Printf("[notification] 捕获审批事件失败: %v", err)
		s.notificationCaptureFailures++
		return
	}
	if err := s.insertNotificationIfAbsentLocked(context.Background(), row); err != nil {
		log.Printf("[notification] 捕获审批事件失败: %v", err)
	}
}

// CaptureNotifications derives events from durable terminal tasks, approved
// decisions and drift history. It runs at notifier startup and after a failed
// event insertion; normal drift transitions are also captured transactionally.
func (s *Store) CaptureNotifications(ctx context.Context, now time.Time) error {
	clusters := s.Clusters()
	s.mu.RLock()
	publicURL := s.notificationPublicURL
	s.mu.RUnlock()
	clustersByID := make(map[int]Cluster, len(clusters))
	for _, cluster := range clusters {
		clustersByID[cluster.ID] = cluster
	}
	for _, l := range s.Logs(0, "", "", 0) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isTerminalDeployStatus(l.Status) {
			continue
		}
		row, err := logNotification(l, clustersByID[l.ClusterID], publicURL, now)
		if err == nil {
			err = s.insertNotificationIfAbsent(ctx, row)
		}
		if err != nil {
			return err
		}
	}
	for _, p := range s.PromotionRequests("approved", 0) {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := promotionNotification(p, now)
		if err == nil {
			err = s.insertNotificationIfAbsent(ctx, row)
		}
		if err != nil {
			return err
		}
	}
	var driftEventID int64
	for {
		events, err := s.DeliveryDriftEventsAfter(ctx, driftEventID, 250)
		if errors.Is(err, ErrDeliveryDriftEventPagingUnavailable) {
			break
		}
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.ID <= driftEventID {
				return fmt.Errorf("漂移事件分页游标未前进")
			}
			driftEventID = event.ID
			row, shouldNotify, err := DeliveryDriftNotification(event, now)
			if err != nil {
				return err
			}
			if shouldNotify {
				if err := s.insertNotificationIfAbsent(ctx, row); err != nil {
					return err
				}
			}
		}
		if len(events) < 250 {
			break
		}
	}
	return nil
}

type Notifier struct {
	store                *Store
	endpoint, secret     string
	client               *http.Client
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	captureMu            sync.Mutex
	capturedSources      bool
	capturedFailureCount uint64
}

func NewNotifier(store *Store, endpoint, secret string, allowHTTP bool) (*Notifier, error) {
	return NewNotifierWithCA(store, endpoint, secret, allowHTTP, "")
}

// NewNotifierWithCA adds an optional PEM CA bundle to the system trust roots.
// This supports private HTTPS receivers without disabling certificate checks.
func NewNotifierWithCA(store *Store, endpoint, secret string, allowHTTP bool, caFile string) (*Notifier, error) {
	return NewNotifierWithPublicURL(store, endpoint, secret, allowHTTP, caFile, "")
}

// NewNotifierWithPublicURL adds an optional platform base URL so webhook
// consumers can link directly to an authenticated deploy task.
func NewNotifierWithPublicURL(store *Store, endpoint, secret string, allowHTTP bool, caFile, platformPublicURL string) (*Notifier, error) {
	platformPublicURL = strings.TrimRight(strings.TrimSpace(platformPublicURL), "/")
	if platformPublicURL != "" {
		u, err := url.Parse(platformPublicURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(platformPublicURL, "#") || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
			return nil, fmt.Errorf("PLATFORM_PUBLIC_URL 必须为 HTTPS 绝对地址且不能包含用户信息、查询参数或片段；HTTP 仅允许 mock")
		}
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	n := &Notifier{store: store, endpoint: endpoint, secret: secret, client: client}
	if endpoint == "" {
		return n, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) || len(secret) < 32 {
		return nil, fmt.Errorf("通知 URL 必须为 HTTPS 且无用户信息，HMAC secret 至少 32 字节；HTTP 仅允许 mock")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		bundle, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("读取通知 CA 文件失败: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("读取系统 CA 根证书失败: %w", err)
		}
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(bundle) {
			return nil, fmt.Errorf("通知 CA 文件不含有效 PEM 证书")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	n.client.Transport = transport
	store.mu.Lock()
	store.notificationPublicURL = platformPublicURL
	store.mu.Unlock()
	return n, nil
}
func (n *Notifier) Start() {
	if n.endpoint == "" {
		return
	}
	n.store.EnableNotificationCapture()
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := n.dispatch(ctx, time.Now()); err != nil {
				log.Printf("[notification] 状态保存失败: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (n *Notifier) Close() {
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
}
func (n *Notifier) dispatch(ctx context.Context, now time.Time) error {
	n.store.EnableNotificationCapture()
	if err := n.captureSourcesIfNeeded(ctx, now); err != nil {
		return err
	}
	rows, err := n.store.DueNotifications(ctx, now, 25)
	if err != nil {
		return err
	}
	// The bounded queue returns older due events first; a failed event does not block later ones.
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewBufferString(row.Payload))
		if err != nil {
			return err
		}
		timestamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(n.secret))
		mac.Write([]byte(timestamp + "." + row.Payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Deploy-Event-ID", row.ID)
		req.Header.Set("X-Deploy-Timestamp", timestamp)
		req.Header.Set("X-Deploy-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
		res, err := n.client.Do(req)
		row.Attempts++
		row.LastError = ""
		if err != nil {
			row.LastError = "network_error"
		} else {
			res.Body.Close()
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				row.LastError = fmt.Sprintf("http_status=%d", res.StatusCode)
			}
		}
		if row.LastError == "" {
			row.Status = "delivered"
		} else if row.Attempts >= 8 {
			row.Status = "dead"
		} else {
			delay := time.Duration(1<<uint(row.Attempts-1)) * time.Second
			row.NextAttemptAt = now.Add(delay)
		}
		if err := n.store.saveNotification(row); err != nil {
			return err
		}
	}
	return nil
}

func (n *Notifier) captureSourcesIfNeeded(ctx context.Context, now time.Time) error {
	n.captureMu.Lock()
	defer n.captureMu.Unlock()
	failureCount := n.store.notificationCaptureFailureVersion()
	if n.capturedSources && failureCount <= n.capturedFailureCount {
		return nil
	}
	if err := n.store.CaptureNotifications(ctx, now); err != nil {
		return err
	}
	n.capturedSources = true
	n.capturedFailureCount = failureCount
	return nil
}

func (s *Store) RetryNotification(id string) error {
	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence != nil {
		queue, ok := persistence.(NotificationQueuePersistence)
		if !ok {
			return fmt.Errorf("通知存储不支持失败项重投")
		}
		return queue.RetryNotification(id, time.Now())
	}
	for _, row := range s.Notifications() {
		if row.ID != id {
			continue
		}
		if row.Status != "dead" {
			return newConflict("仅失败通知可重新投递")
		}
		row.Status = "pending"
		row.Attempts = 0
		row.LastError = ""
		row.NextAttemptAt = time.Now()
		return s.saveNotification(row)
	}
	return ErrNotFound
}
