package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestSSHPodLogStreamArgv(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return "line1\nline2\n", "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	tail := int64(50)
	stream, err := rt.PodLogStream(context.Background(), "prod", "web-1", kube.LogOptions{
		Container: "app", Tail: &tail, Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	got, _ := io.ReadAll(stream)
	if string(got) != "line1\nline2\n" {
		t.Fatalf("stream = %q", got)
	}
	want := "kubectl logs web-1 -n prod -f --tail=50 -c app"
	if len(runner.calls) != 1 || runner.calls[0] != want {
		t.Fatalf("argv = %v, want %q", runner.calls, want)
	}
}

func TestSSHPodLogStreamNoFollow(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return "x", "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	stream, err := rt.PodLogStream(context.Background(), "ns", "p", kube.LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if strings.Contains(runner.calls[0], "-f") {
		t.Fatalf("follow flag must not appear: %q", runner.calls[0])
	}
}

func TestSSHExecPodUnsupported(t *testing.T) {
	rt := &sshKubernetes{runner: &sshStubRunner{}, cluster: Cluster{}}
	err := rt.ExecPod(context.Background(), "ns", "p", kube.ExecSpec{Command: []string{"sh"}}, kube.ExecIO{})
	if !errors.Is(err, ErrExecUnsupported) {
		t.Fatalf("err = %v, want ErrExecUnsupported", err)
	}
}

func TestMockPodLogStream(t *testing.T) {
	tail := int64(10)
	stream, err := MockKubernetes{}.PodLogStream(context.Background(), "ns", "web", kube.LogOptions{
		Tail: &tail, Follow: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(stream)
	if !strings.Contains(string(got), "mock log backlog for pod web (tail=10)") {
		t.Fatalf("stream = %q", got)
	}

	if _, err := (MockKubernetes{}).PodLogStream(context.Background(), "ns", "missing", kube.LogOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing pod err = %v, want NotFound", err)
	}
}

func TestMockPodLogStreamFollowHonoursCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := MockKubernetes{}.PodLogStream(ctx, "ns", "web", kube.LogOptions{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(stream)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read did not unblock after ctx cancel")
	}
}

// lockedBuf is an io.Writer safe for concurrent producer/reader access.
type lockedBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMockExecPodEchoAndExit(t *testing.T) {
	out := &lockedBuf{}
	stdinR, stdinW := io.Pipe()
	mock := MockKubernetes{}
	done := make(chan error, 1)
	go func() {
		done <- mock.ExecPod(context.Background(), "ns", "web",
			kube.ExecSpec{Container: "app", Command: []string{"/bin/sh"}, TTY: true},
			kube.ExecIO{Stdin: stdinR, Stdout: out})
	}()
	stdinW.Write([]byte("hello\r"))
	deadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "mock echo: hello") {
		select {
		case <-deadline:
			t.Fatalf("no echo: %q", out.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	stdinW.Write([]byte("exit\r"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exec err = %v", err)
		}
	case <-deadline:
		t.Fatal("exec did not end after `exit`")
	}
	if !strings.Contains(out.String(), "mock terminal closed") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestMockExecPodMissing(t *testing.T) {
	err := MockKubernetes{}.ExecPod(context.Background(), "ns", "missing",
		kube.ExecSpec{Command: []string{"sh"}}, kube.ExecIO{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestMockRunnerRunSSHStream(t *testing.T) {
	m := &MockRunner{}
	stream, err := m.RunSSHStream(context.Background(), Cluster{K8sMasterUser: "u", K8sMasterHost: "h"}, []string{"kubectl", "logs", "p", "-f"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(stream)
	if !strings.Contains(string(got), "[MOCK] stream u@h: kubectl logs p -f") {
		t.Fatalf("stream = %q", got)
	}
}
