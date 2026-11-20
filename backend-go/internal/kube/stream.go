package kube

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// LogOptions carries the streaming parameters for PodLogStream.
// Container may be empty for single-container pods; Tail bounds the
// backlog sent before live data; Follow keeps the stream open after the
// backlog is drained.
type LogOptions struct {
	Container string
	Tail      *int64
	Follow    bool
}

// PodLogStream opens a follow-capable log stream. The returned reader is
// bound to ctx — cancelling it closes the upstream HTTP stream, so a
// disconnected browser session cannot leak an apiserver connection.
func (c *Client) PodLogStream(ctx context.Context, namespace, pod string, opts LogOptions) (io.ReadCloser, error) {
	req := c.client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: opts.Container,
		TailLines: opts.Tail,
		Follow:    opts.Follow,
	})
	return req.Stream(ctx)
}

// TerminalSize is a TTY resize event in character cells.
type TerminalSize struct {
	Width  uint16
	Height uint16
}

// TerminalSizeQueue mirrors remotecommand.TerminalSizeQueue without
// leaking client-go types into callers. Next blocks until a size is
// available or the session ends (return nil to stop resize handling).
type TerminalSizeQueue interface {
	Next() *TerminalSize
}

// sizeQueue adapts our TerminalSizeQueue to remotecommand's.
type sizeQueue struct{ q TerminalSizeQueue }

func (a sizeQueue) Next() *remotecommand.TerminalSize {
	s := a.q.Next()
	if s == nil {
		return nil
	}
	return &remotecommand.TerminalSize{Width: s.Width, Height: s.Height}
}

// ExecSpec describes one exec session. Command is the argv run inside
// the container (e.g. []string{"/bin/sh"}); TTY requests a pseudo-
// terminal — required for interactive shells.
type ExecSpec struct {
	Container string
	Command   []string
	TTY       bool
}

// ExecIO wires the exec channels. Stdin may be nil (no stdin channel);
// Stderr is ignored when TTY is set (a TTY merges the streams). Resize
// may be nil for non-TTY sessions.
type ExecIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Resize TerminalSizeQueue
}

// ExecPod opens an interactive exec session and blocks until it ends or
// ctx is cancelled. WebSocket is tried first (client-go 0.34 supports
// both channels); a genuine upgrade failure falls back to SPDY.
func (c *Client) ExecPod(ctx context.Context, namespace, pod string, spec ExecSpec, io_ ExecIO) error {
	if c.streamCfg == nil {
		return fmt.Errorf("exec 不可用：缺少集群连接配置")
	}
	if len(spec.Command) == 0 {
		return fmt.Errorf("exec 命令不能为空")
	}
	req := c.client.CoreV1().RESTClient().Post().
		Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: spec.Container,
			Command:   spec.Command,
			Stdin:     io_.Stdin != nil,
			Stdout:    io_.Stdout != nil,
			Stderr:    io_.Stderr != nil && !spec.TTY,
			TTY:       spec.TTY,
		}, scheme.ParameterCodec)
	url := req.URL()

	wsExec, wsErr := remotecommand.NewWebSocketExecutor(c.streamCfg, "GET", url.String())
	spdyExec, spdyErr := remotecommand.NewSPDYExecutor(c.streamCfg, "POST", url)
	var executor remotecommand.Executor
	switch {
	case wsErr == nil && spdyErr == nil:
		executor, _ = remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	case wsErr == nil:
		executor = wsExec
	case spdyErr == nil:
		executor = spdyExec
	default:
		return fmt.Errorf("exec 通道初始化失败: %w", wsErr)
	}

	opts := remotecommand.StreamOptions{
		Stdin:  io_.Stdin,
		Stdout: io_.Stdout,
		Tty:    spec.TTY,
	}
	if !spec.TTY {
		opts.Stderr = io_.Stderr
	}
	if io_.Resize != nil {
		opts.TerminalSizeQueue = sizeQueue{io_.Resize}
	}
	return executor.StreamWithContext(ctx, opts)
}
