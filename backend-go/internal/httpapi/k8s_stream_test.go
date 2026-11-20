package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// wsServer wraps the gin engine in a real HTTP server so tests can drive
// genuine WebSocket handshakes (httptest.ResponseRecorder cannot upgrade).
func wsServer(t *testing.T, e *gin.Engine) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func wsDial(t *testing.T, base, path string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Cookie": []string{sessionCookie + "=" + testSessionToken}}
	conn, resp, err := websocket.DefaultDialer.Dial(base+path, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s → %v (HTTP %d)", path, err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// wsDialStatus asserts the handshake itself failed with a given HTTP
// status — the pre-upgrade rejection path (bad params, limits, auth).
func wsDialStatus(t *testing.T, base, path string, want int) {
	t.Helper()
	header := http.Header{"Cookie": []string{sessionCookie + "=" + testSessionToken}}
	conn, resp, err := websocket.DefaultDialer.Dial(base+path, header)
	if err == nil {
		conn.Close()
		t.Fatalf("dial %s unexpectedly succeeded", path)
	}
	if resp == nil || resp.StatusCode != want {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("dial %s → HTTP %d, want %d (err=%v)", path, got, want, err)
	}
}

// readAll collects binary frames until the server closes the connection.
func readAll(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var out []byte
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return out
		}
		if typ == websocket.BinaryMessage {
			out = append(out, data...)
		}
	}
}

// readFrames collects (channel, payload) pairs until close or timeout.
func readFrames(t *testing.T, conn *websocket.Conn) [][2][]byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frames [][2][]byte
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return frames
		}
		if typ == websocket.BinaryMessage && len(data) > 0 {
			frames = append(frames, [2][]byte{{data[0]}, data[1:]})
		}
	}
}

func TestPodLogStreamFollowFalse(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	conn := wsDial(t, base, "/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&namespace=default&follow=false")
	got := string(readAll(t, conn))
	if !strings.Contains(got, "mock log backlog") {
		t.Fatalf("log stream = %q", got)
	}
}

func TestPodLogStreamFollowEmitsLines(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	conn := wsDial(t, base, "/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&follow=true")
	got := string(readAll(t, conn))
	if !strings.Contains(got, "mock follow line 5") {
		t.Fatalf("follow stream ended early: %q", got)
	}
}

func TestPodLogStreamParamContract(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/k8s/pods/p1/logs/stream", 400},                                   // missing cluster_id
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=x", 400},                      // bad cluster_id
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=999", 404},                    // unknown cluster
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&container=BAD_NAME", 400},   // bad container
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&tail=0", 400},               // tail below min
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&tail=99999", 400},           // tail above max
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&follow=maybe", 400},         // bad follow
		{"/api/v1/k8s/pods/missing/logs/stream?cluster_id=1", 404},                 // pod missing → k8s 404 pre-upgrade
		{"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1&container=c1&tail=50", 200}, // happy path still upgrades
	}
	for _, tc := range cases {
		if tc.want == 200 {
			conn := wsDial(t, base, tc.path)
			conn.Close()
			continue
		}
		wsDialStatus(t, base, tc.path, tc.want)
	}
}

func TestPodLogStreamPlainHTTPRejected(t *testing.T) {
	_, e, _ := newTestApp(t)
	srv, _ := wsServer(t, e)

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/k8s/pods/p1/logs/stream?cluster_id=1", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: testSessionToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 101 || resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("plain GET upgraded: %d %s", resp.StatusCode, body)
	}
}

func TestPodTerminalEchoAndExit(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	conn := wsDial(t, base, "/api/v1/k8s/pods/p1/terminal?cluster_id=1&namespace=default&command=/bin/sh")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Banner arrives on the stdout channel.
	typ, banner, err := conn.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage || len(banner) == 0 || banner[0] != chStdout {
		t.Fatalf("banner frame = type %v %q err %v", typ, banner, err)
	}
	if !strings.Contains(string(banner[1:]), "mock terminal") {
		t.Fatalf("banner = %q", banner)
	}

	// `exit` ends the mock shell → a channel-3 frame then server close.
	stdin := append([]byte{chStdin}, []byte("exit\r")...)
	if err := conn.WriteMessage(websocket.BinaryMessage, stdin); err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read before exit frame: %v", err)
		}
		if typ == websocket.BinaryMessage && len(data) > 0 && data[0] == chExit {
			return
		}
	}
}

func TestPodTerminalResizeAccepted(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	conn := wsDial(t, base, "/api/v1/k8s/pods/p1/terminal?cluster_id=1")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Drain the banner first.
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	// A resize frame must not disturb the session — mock echoes input.
	resize := append([]byte{chResize}, []byte(`{"cols":120,"rows":40}`)...)
	if err := conn.WriteMessage(websocket.BinaryMessage, resize); err != nil {
		t.Fatal(err)
	}
	stdin := append([]byte{chStdin}, []byte("hi\r")...)
	if err := conn.WriteMessage(websocket.BinaryMessage, stdin); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("session broke after resize: %v", err)
		}
		if len(data) > 0 && data[0] == chStdout && strings.Contains(string(data[1:]), "mock echo: hi") {
			return
		}
	}
}

func TestPodTerminalSSHUnsupported(t *testing.T) {
	runner := &scriptRunner{fn: func([]string) (string, string, error) {
		return "", "", nil
	}}
	e := newSSHEngine(t, runner)
	_, base := wsServer(t, e)

	conn := wsDial(t, base, "/api/v1/k8s/pods/p1/terminal?cluster_id=1")
	frames := readFrames(t, conn)
	if len(frames) == 0 || frames[0][0][0] != chExit {
		t.Fatalf("expected exit frame for ssh exec, got %v", frames)
	}
	if !strings.Contains(string(frames[0][1]), "不支持") {
		t.Fatalf("exit reason = %q", frames[0][1])
	}
}

func TestPodTerminalParamContract(t *testing.T) {
	_, e, _ := newTestApp(t)
	_, base := wsServer(t, e)

	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/k8s/pods/p1/terminal", 400},
		{"/api/v1/k8s/pods/p1/terminal?cluster_id=999", 404},
		{"/api/v1/k8s/pods/p1/terminal?cluster_id=1&container=BAD_NAME", 400},
	}
	for _, tc := range cases {
		wsDialStatus(t, base, tc.path, tc.want)
	}
}

func TestStreamSessionLimits(t *testing.T) {
	// Prior WS tests release their exec slots asynchronously — wait for
	// the budget to drain before asserting on it.
	deadline := time.Now().Add(3 * time.Second)
	for len(streamLimits.exec) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !acquire(streamLimits.logs) {
		t.Fatal("first acquire must succeed")
	}
	release(streamLimits.logs)
	if len(streamLimits.logs) != 0 {
		t.Fatal("release must free the slot")
	}
	for i := 0; i < cap(streamLimits.exec); i++ {
		if !acquire(streamLimits.exec) {
			t.Fatalf("slot %d of %d refused", i, cap(streamLimits.exec))
		}
	}
	if acquire(streamLimits.exec) {
		t.Fatal("acquire beyond capacity must fail")
	}
	for i := 0; i < cap(streamLimits.exec); i++ {
		release(streamLimits.exec)
	}
}

func TestExecCommandParsing(t *testing.T) {
	if got := execCommand(""); len(got) != 1 || got[0] != "/bin/sh" {
		t.Fatalf("default command = %v", got)
	}
	if got := execCommand("  "); len(got) != 1 || got[0] != "/bin/sh" {
		t.Fatalf("blank command = %v", got)
	}
	got := execCommand("/bin/bash -l")
	if len(got) != 2 || got[0] != "/bin/bash" || got[1] != "-l" {
		t.Fatalf("command = %v", got)
	}
}
