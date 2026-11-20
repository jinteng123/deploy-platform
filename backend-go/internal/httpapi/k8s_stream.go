package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/kube"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Stream session bounds (playbook step 8): every connection is tied to
// the request context, has an idle window and a hard max duration, and
// counts against a global per-kind concurrency budget.
const (
	logMaxDuration  = 60 * time.Minute
	logIdleTimeout  = 15 * time.Minute
	execMaxDuration = 30 * time.Minute
	execIdleTimeout = 10 * time.Minute
	wsPingInterval  = 25 * time.Second
	wsWriteTimeout  = 10 * time.Second
)

var streamLimits = struct {
	logs chan struct{}
	exec chan struct{}
}{
	logs: make(chan struct{}, 32),
	exec: make(chan struct{}, 8),
}

// acquire takes one slot from a session budget; false means 429.
func acquire(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(slots chan struct{}) { <-slots }

// Exec frame channels: every WebSocket message is binary with a 1-byte
// channel prefix. Client→server: 0=stdin, 4=resize(JSON cols/rows).
// Server→client: 1=stdout, 2=stderr, 3=session ended (UTF-8 reason).
const (
	chStdin  byte = 0
	chStdout byte = 1
	chStderr byte = 2
	chExit   byte = 3
	chResize byte = 4
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The SPA is served through the vite dev proxy (Origin localhost:3000,
	// Host 127.0.0.1:8000) and behind reverse proxies in production —
	// strict same-origin checks would break both. Access control belongs
	// to the platform auth layer (step 9), not Origin sniffing.
	CheckOrigin: func(*http.Request) bool { return true },
}

// wsWriter serializes frame writes — gorilla allows only one concurrent
// writer, and stdout/stderr pumps race otherwise. Each write carries a
// deadline so a stuck client cannot wedge the session.
type wsWriter struct {
	conn *websocket.Conn
	mu   chan struct{}
}

func newWSWriter(conn *websocket.Conn) *wsWriter {
	w := &wsWriter{conn: conn, mu: make(chan struct{}, 1)}
	w.mu <- struct{}{}
	return w
}

func (w *wsWriter) write(channel byte, data []byte) error {
	frame := make([]byte, 1+len(data))
	frame[0] = channel
	copy(frame[1:], data)
	<-w.mu
	defer func() { w.mu <- struct{}{} }()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return w.conn.WriteMessage(websocket.BinaryMessage, frame)
}

// chanWriter adapts a WS channel into an io.Writer for exec pumps.
type chanWriter struct {
	w    *wsWriter
	ch   byte
	done <-chan struct{}
}

func (c chanWriter) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}
	if err := c.w.write(c.ch, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// wsSizeQueue feeds resize events from the WS read loop into the
// exec TerminalSizeQueue. A small buffer drops bursts; close ends it.
type wsSizeQueue struct {
	ch chan kube.TerminalSize
}

func newWSSizeQueue() *wsSizeQueue { return &wsSizeQueue{ch: make(chan kube.TerminalSize, 16)} }

func (q *wsSizeQueue) Next() *kube.TerminalSize {
	s, ok := <-q.ch
	if !ok {
		return nil
	}
	size := s
	return &size
}

func (q *wsSizeQueue) push(cols, rows uint16) {
	select {
	case q.ch <- kube.TerminalSize{Width: cols, Height: rows}:
	default: // drop a burst rather than block the read loop
	}
}

func (q *wsSizeQueue) close() { close(q.ch) }

// pingLoop sends protocol pings until ctx ends, keeping proxies and
// load balancers from reaping quiet sessions. WriteControl is safe to
// call concurrently with data writes.
func pingLoop(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout))
		}
	}
}

// wsClose sends a close frame (bounded reason) then closes the socket —
// clients see a clean shutdown instead of a dropped connection.
func wsClose(conn *websocket.Conn, code int, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	payload := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, payload, time.Now().Add(2*time.Second))
	_ = conn.Close()
}

// logStreamParams validates the log-stream query beyond the shared
// cluster/namespace context: container must be a DNS-1123 label when
// given, tail is bounded, follow defaults to true.
func logStreamParams(c *gin.Context) (container string, tail int64, follow bool, ok bool) {
	container = c.Query("container")
	if container != "" && len(validation.IsDNS1123Label(container)) > 0 {
		fail(c, http.StatusBadRequest, "非法参数 container")
		return "", 0, false, false
	}
	t, ok := queryIntRange(c, "tail", 200, 1, 5000)
	if !ok {
		return "", 0, false, false
	}
	follow = true
	if raw := c.Query("follow"); raw != "" {
		switch raw {
		case "true", "1":
			follow = true
		case "false", "0":
			follow = false
		default:
			fail(c, http.StatusBadRequest, "非法参数 follow")
			return "", 0, false, false
		}
	}
	return container, int64(t), follow, true
}

// k8sErr maps a Kubernetes API error to an HTTP status for the
// pre-upgrade failure window.
func k8sErr(c *gin.Context, err error) {
	code := http.StatusInternalServerError
	switch {
	case apierrors.IsNotFound(err):
		code = http.StatusNotFound
	case apierrors.IsForbidden(err):
		code = http.StatusForbidden
	case apierrors.IsBadRequest(err):
		code = http.StatusBadRequest
	}
	fail(c, code, err.Error())
}

// handlePodLogStream upgrades to WebSocket and streams pod logs as
// binary frames. The K8s stream opens before the upgrade so API errors
// still map to honest HTTP codes; afterwards failures ride close frames.
func handlePodLogStream(c *gin.Context, store *service.Store, k8s *service.K8sService) {
	rt, cluster, ok := resolveCluster(c, store, k8s)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	if !nonEmpty(ns) {
		ns = cluster.DefaultNamespace
	}
	pod := c.Param("pod")
	if pod == "" || len(validation.IsDNS1123Subdomain(pod)) > 0 {
		fail(c, http.StatusBadRequest, "非法 pod 名称")
		return
	}
	container, tail, follow, ok := logStreamParams(c)
	if !ok {
		return
	}
	if !acquire(streamLimits.logs) {
		fail(c, http.StatusTooManyRequests, "日志会话数已达上限")
		return
	}
	defer release(streamLimits.logs)

	ctx, cancel := context.WithTimeout(c.Request.Context(), logMaxDuration)
	defer cancel()
	// A cluster disable/update/delete cancels this session's ctx — no
	// watcher may outlive the cluster it was opened against.
	untrack := k8s.TrackStream(cluster.ID, cancel)
	defer untrack()

	stream, err := rt.PodLogStream(ctx, ns, pod, kube.LogOptions{
		Container: container,
		Tail:      &tail,
		Follow:    follow,
	})
	if err != nil {
		k8sErr(c, err)
		return
	}
	defer stream.Close()

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade already wrote the HTTP rejection
	}
	defer conn.Close()

	go pingLoop(ctx, conn)

	// Detect client disconnect: the read pump surfaces a close/error on
	// the client side, letting us cancel the upstream stream promptly.
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	// Pump upstream chunks through a channel so reads and writes never
	// block each other and the idle timer sees real traffic only.
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 16*1024)
		for {
			n, rerr := stream.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case chunks <- chunk{data: data}:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				select {
				case chunks <- chunk{err: rerr}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	idle := time.NewTimer(logIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			cancel()
			return
		case <-idle.C:
			wsClose(conn, websocket.CloseGoingAway, "idle timeout")
			return
		case ch := <-chunks:
			if ch.err != nil {
				if !errors.Is(ch.err, io.EOF) {
					wsClose(conn, websocket.CloseInternalServerErr, ch.err.Error())
				}
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.BinaryMessage, ch.data); err != nil {
				return
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(logIdleTimeout)
		}
	}
}

// execCommand parses the optional `command` query into argv.
func execCommand(raw string) []string {
	if raw == "" {
		return []string{"/bin/sh"}
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return []string{"/bin/sh"}
	}
	return fields
}

// handlePodTerminal upgrades to WebSocket and bridges frames to a
// remotecommand exec session (stdin/stdout/stderr/resize channels).
func handlePodTerminal(c *gin.Context, store *service.Store, k8s *service.K8sService) {
	rt, cluster, ok := resolveCluster(c, store, k8s)
	if !ok {
		return
	}
	ns := c.Query("namespace")
	if !nonEmpty(ns) {
		ns = cluster.DefaultNamespace
	}
	pod := c.Param("pod")
	if pod == "" || len(validation.IsDNS1123Subdomain(pod)) > 0 {
		fail(c, http.StatusBadRequest, "非法 pod 名称")
		return
	}
	container := c.Query("container")
	if container != "" && len(validation.IsDNS1123Label(container)) > 0 {
		fail(c, http.StatusBadRequest, "非法参数 container")
		return
	}
	tty := true
	if raw := c.Query("tty"); raw == "false" || raw == "0" {
		tty = false
	}
	command := execCommand(c.Query("command"))

	if !acquire(streamLimits.exec) {
		fail(c, http.StatusTooManyRequests, "终端会话数已达上限")
		return
	}
	defer release(streamLimits.exec)

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade already wrote the HTTP rejection
	}
	defer conn.Close()
	conn.SetReadLimit(64 * 1024)

	// Audit the session once the upgrade succeeded — exec is the most
	// sensitive operation in the console.
	cid, _ := strconv.Atoi(c.Query("cluster_id"))
	audit(c, store, "exec", cid, ns, "pods", pod, strings.Join(command, " "), service.AuditSuccess)

	// Two contexts on purpose: sessCtx bounds the WS session (request ctx +
	// max duration) while execCtx drives the upstream remotecommand. They
	// are deliberately NOT linked — on teardown we first close stdin (a
	// protocol EOF lets a remote shell exit itself, unlike kubectl's
	// abrupt drop which orphans the process), wait briefly, and only then
	// cancel execCtx to drop the upstream connection.
	sessCtx, sessCancel := context.WithTimeout(c.Request.Context(), execMaxDuration)
	defer sessCancel()
	// Cluster lifecycle events (update/disable/delete) cancel sessCtx —
	// the session tears down gracefully rather than outliving its cluster.
	untrack := k8s.TrackStream(cluster.ID, sessCancel)
	defer untrack()
	execCtx, execCancel := context.WithCancel(context.Background())
	defer execCancel()
	go pingLoop(sessCtx, conn)

	w := newWSWriter(conn)
	done := make(chan struct{})
	defer close(done)

	sizes := newWSSizeQueue()
	defer sizes.close()

	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()

	execDone := make(chan error, 1)
	go func() {
		execDone <- rt.ExecPod(execCtx, ns, pod, kube.ExecSpec{
			Container: container,
			Command:   command,
			TTY:       tty,
		}, kube.ExecIO{
			Stdin:  stdinR,
			Stdout: chanWriter{w: w, ch: chStdout, done: done},
			Stderr: chanWriter{w: w, ch: chStderr, done: done},
			Resize: sizes,
		})
	}()

	// Read pump: conn.ReadMessage blocks, so it runs in its own goroutine
	// — the main loop stays free to see ctx, idle and execDone instantly.
	type wsMsg struct {
		typ  int
		data []byte
	}
	msgs := make(chan wsMsg, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				select {
				case readErr <- err:
				case <-sessCtx.Done():
				}
				return
			}
			select {
			case msgs <- wsMsg{typ: typ, data: data}:
			case <-sessCtx.Done():
				return
			}
		}
	}()

	// teardown ends the session. Escalation ladder, each step bounded:
	//   1. "exit\n" through stdin — a remote *shell* exits itself on the
	//      command even on a TTY, where a bare stdin-close is swallowed
	//      by the runtime and never becomes an EOF the shell can read.
	//      Harmless input for non-shell commands whose stdin is ending.
	//   2. stdin close — EOF for non-TTY readers (cat, scripts).
	//   3. execCtx cancel — drops the upstream SPDY/WebSocket connection.
	// execDone carries a single value — consume it exactly once.
	teardown := func() {
		_, _ = stdinW.Write([]byte("exit\n"))
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-execDone:
			return
		case <-timer.C:
		}
		_ = stdinW.Close()
		timer.Reset(3 * time.Second)
		select {
		case <-execDone:
		case <-timer.C:
		}
		execCancel()
	}

	idle := time.NewTimer(execIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-sessCtx.Done():
			teardown()
			return
		case <-idle.C:
			wsClose(conn, websocket.CloseGoingAway, "idle timeout")
			teardown()
			return
		case err := <-execDone:
			reason := "session ended"
			if err != nil {
				reason = err.Error()
			}
			_ = w.write(chExit, []byte(reason))
			return
		case <-readErr:
			// Client disconnected — graceful EOF then hard teardown.
			teardown()
			return
		case m := <-msgs:
			if m.typ != websocket.BinaryMessage || len(m.data) == 0 {
				continue
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(execIdleTimeout)
			switch m.data[0] {
			case chStdin:
				if _, err := stdinW.Write(m.data[1:]); err != nil {
					return
				}
			case chResize:
				var size struct {
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(m.data[1:], &size) == nil && size.Cols > 0 && size.Rows > 0 {
					sizes.push(size.Cols, size.Rows)
				}
			}
		}
	}
}

// registerK8sStreamAPI wires the WebSocket stream endpoints.
func registerK8sStreamAPI(api *gin.RouterGroup, store *service.Store, k8s *service.K8sService) {
	api.GET("/k8s/pods/:pod/logs/stream", func(c *gin.Context) { handlePodLogStream(c, store, k8s) })
	api.GET("/k8s/pods/:pod/terminal", requireRole(store, service.RoleOperator), func(c *gin.Context) { handlePodTerminal(c, store, k8s) })
}
