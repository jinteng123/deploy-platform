package kube

import (
	"context"

	"k8s.io/client-go/rest"
	"strings"
	"testing"
)

func TestExecPodRequiresConfig(t *testing.T) {
	c := NewWithClientset(nil, nil, nil)
	err := c.ExecPod(context.Background(), "ns", "p", ExecSpec{Command: []string{"sh"}}, ExecIO{})
	if err == nil || !strings.Contains(err.Error(), "缺少集群连接配置") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecPodRequiresCommand(t *testing.T) {
	c := NewWithClientset(nil, nil, nil)
	c.streamCfg = &rest.Config{Host: "https://x"}
	err := c.ExecPod(context.Background(), "ns", "p", ExecSpec{}, ExecIO{})
	if err == nil || !strings.Contains(err.Error(), "命令不能为空") {
		t.Fatalf("err = %v", err)
	}
}

func TestSizeQueueAdapter(t *testing.T) {
	q := sizeQueue{q: &stubQueue{size: &TerminalSize{Width: 80, Height: 24}}}
	got := q.Next()
	if got == nil || got.Width != 80 || got.Height != 24 {
		t.Fatalf("size = %+v", got)
	}
	q = sizeQueue{q: &stubQueue{}}
	if q.Next() != nil {
		t.Fatal("nil size must end the queue")
	}
}

type stubQueue struct{ size *TerminalSize }

func (s *stubQueue) Next() *TerminalSize { return s.size }
