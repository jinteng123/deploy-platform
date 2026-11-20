package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type notificationWebhookReceipt struct {
	eventID        string
	body           []byte
	signatureValid bool
}

// TestNotificationWebhookE2E exercises the real API process and MySQL outbox
// against a local HTTPS receiver trusted by an explicitly configured private CA.
func TestNotificationWebhookE2E(t *testing.T) {
	if os.Getenv("KB_E2E_NOTIFICATION") != "1" {
		t.Skip("KB_E2E_NOTIFICATION != 1 — skipping MySQL-backed webhook E2E")
	}
	mysql := startMigrationE2EMySQL(t)
	e2eDB = mysql.database
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "036")
	seedTerminalNotificationSource(t, mysql.db)

	secret := strings.Repeat("notification-e2e-secret-", 2)
	receipts := make(chan notificationWebhookReceipt, 4)
	sink := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body read failed", http.StatusBadRequest)
			return
		}
		timestamp := r.Header.Get("X-Deploy-Timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		want := "v1=" + hex.EncodeToString(mac.Sum(nil))
		valid := r.Method == http.MethodPost && r.Header.Get("Content-Type") == "application/json" &&
			timestamp != "" && hmac.Equal([]byte(r.Header.Get("X-Deploy-Signature")), []byte(want))
		receipts <- notificationWebhookReceipt{eventID: r.Header.Get("X-Deploy-Event-ID"), body: body, signatureValid: valid}
		if !valid {
			http.Error(w, "signature invalid", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)
	caFile := filepath.Join(t.TempDir(), "notification-e2e-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sink.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	server := startServerWithOverrides(t, map[string]string{
		"MOCK_RUNTIME":                 "true",
		"DATABASE_DSN":                 mysql.dsn,
		"NOTIFICATION_WEBHOOK_URL":     sink.URL,
		"NOTIFICATION_WEBHOOK_SECRET":  secret,
		"NOTIFICATION_WEBHOOK_CA_FILE": caFile,
	})
	loginE2EUser(t, "admin", e2ePassword)

	var receipt notificationWebhookReceipt
	select {
	case receipt = <-receipts:
	case <-time.After(20 * time.Second):
		t.Fatalf("terminal notification was not delivered to the local receiver; server log: %s", tailLog(server.logPath))
	}
	if !receipt.signatureValid {
		t.Fatalf("local receiver rejected webhook HMAC: event_id=%q body=%s", receipt.eventID, receipt.body)
	}
	if receipt.eventID != "deploy:1:success" {
		t.Fatalf("event ID=%q, want stable source ID deploy:1:success", receipt.eventID)
	}
	var payload map[string]any
	if err := json.Unmarshal(receipt.body, &payload); err != nil {
		t.Fatalf("decode signed notification body: %v", err)
	}
	if payload["id"] != receipt.eventID || payload["event"] != "deploy_finished" {
		t.Fatalf("unexpected notification envelope: %v", payload)
	}
	if strings.Contains(string(receipt.body), secret) || strings.Contains(string(receipt.body), "error_message") {
		t.Fatalf("notification body contains secret or execution error detail: %s", receipt.body)
	}

	rows := waitForNotificationStatus(t, "delivered", 10*time.Second)
	if len(rows) != 1 || rows[0]["id"] != receipt.eventID || rows[0]["attempts"] != float64(1) {
		t.Fatalf("persisted notification state=%v, want one delivered attempt", rows)
	}
	if _, exposesPayload := rows[0]["payload"]; exposesPayload {
		t.Fatalf("admin notification API must not expose payload: %v", rows[0])
	}

	server.restart(t)
	loginE2EUser(t, "admin", e2ePassword)
	select {
	case duplicate := <-receipts:
		t.Fatalf("restart redelivered an acknowledged notification: event_id=%q body=%s", duplicate.eventID, duplicate.body)
	case <-time.After(3 * time.Second):
	}
	rows = waitForNotificationStatus(t, "delivered", time.Second)
	if len(rows) != 1 || rows[0]["attempts"] != float64(1) {
		t.Fatalf("restart changed durable delivery status or attempt count: %v", rows)
	}

	assertNotificationKeysetPaginationOnMySQL(t, mysql.db)
}

func assertNotificationKeysetPaginationOnMySQL(t *testing.T, db *sql.DB) {
	t.Helper()
	seed := "INSERT INTO delivery_notifications(id,event,payload,status,attempts,next_attempt_at,created_at,last_error) VALUES " +
		"('e2e-notify-z','deploy_failed',JSON_OBJECT('id','e2e-notify-z'),'dead',8,UTC_TIMESTAMP(),UTC_TIMESTAMP(),'http_status=503')," +
		"('e2e-notify-a','deploy_failed',JSON_OBJECT('id','e2e-notify-a'),'dead',8,UTC_TIMESTAMP(),UTC_TIMESTAMP(),'http_status=503')"
	if _, err := db.Exec(seed); err != nil {
		t.Fatalf("seed isolated active notification pages: %v", err)
	}
	status, first := api(t, http.MethodGet, "/api/v1/delivery/notifications/active?limit=1", nil)
	if status != http.StatusOK {
		t.Fatalf("read first indexed notification page: status=%d body=%v", status, first)
	}
	items, ok := first["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("first indexed notification page: %v", first)
	}
	firstItem, ok := items[0].(map[string]any)
	cursor, cursorOK := first["next_cursor"].(string)
	if !ok || firstItem["id"] != "e2e-notify-z" || !cursorOK || cursor == "" || first["has_more"] != true {
		t.Fatalf("first notification page did not return newest item and cursor: %v", first)
	}
	if _, exposed := firstItem["payload"]; exposed {
		t.Fatalf("indexed notification page exposed payload: %v", firstItem)
	}

	if _, err := db.Exec("UPDATE delivery_notifications SET status='delivered' WHERE id='e2e-notify-z'"); err != nil {
		t.Fatalf("mark page cursor delivered: %v", err)
	}
	status, second := api(t, http.MethodGet, "/api/v1/delivery/notifications/active?limit=1&cursor="+cursor, nil)
	secondItems, ok := second["items"].([]any)
	if status != http.StatusOK || !ok || len(secondItems) != 1 {
		t.Fatalf("read next indexed notification page after status change: status=%d body=%v", status, second)
	}
	secondItem, ok := secondItems[0].(map[string]any)
	if !ok || secondItem["id"] != "e2e-notify-a" || second["has_more"] != false {
		t.Fatalf("cursor row status change invalidated keyset pagination: %v", second)
	}
}

func seedTerminalNotificationSource(t *testing.T, db *sql.DB) {
	t.Helper()
	cluster, err := db.Exec(`INSERT INTO clusters(name,env_code,k8s_master_host,default_namespace)
		VALUES('Notification E2E','notify-e2e','127.0.0.1','default')`)
	if err != nil {
		t.Fatalf("seed notification cluster: %v", err)
	}
	clusterID, err := cluster.LastInsertId()
	if err != nil {
		t.Fatalf("read notification cluster ID: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,deploy_type,status,current_step,finished_at,created_at)
		VALUES(?,'notify-app','notify-app','web','default','v1','notification-e2e','deploy','success','done',UTC_TIMESTAMP(),UTC_TIMESTAMP())`, clusterID); err != nil {
		t.Fatalf("seed isolated terminal notification source: %v", err)
	}
}

func waitForNotificationStatus(t *testing.T, want string, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var rows []map[string]any
	for time.Now().Before(deadline) {
		rows = notificationRows(t)
		if len(rows) == 1 && rows[0]["status"] == want {
			return rows
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("notification status did not reach %q: %v", want, rows)
	return nil
}

func notificationRows(t *testing.T) []map[string]any {
	t.Helper()
	raw := apiArray(t, "/api/v1/delivery/notifications")
	rows := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		row, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("notification response contains unexpected value: %T", value)
		}
		rows = append(rows, row)
	}
	return rows
}

func tailLog(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	text := string(content)
	if len(text) > 4000 {
		text = text[len(text)-4000:]
	}
	return text
}
