package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CommandRunner executes local commands and remote commands on a cluster's
// K8s master over SSH.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin io.Reader) (stdout, stderr string, err error)
	RunSSH(ctx context.Context, cluster Cluster, args []string) (stdout, stderr string, err error)
	// RunSSHStdin pipes stdin into the remote command — needed by
	// `kubectl replace -f -`, which reads the manifest from stdin.
	RunSSHStdin(ctx context.Context, cluster Cluster, args []string, stdin io.Reader) (stdout, stderr string, err error)
	// RunSSHStream starts a remote command and returns its live stdout
	// stream — needed by `kubectl logs -f`. Cancelling ctx (or closing the
	// stream) kills the ssh process, so nothing outlives the session.
	RunSSHStream(ctx context.Context, cluster Cluster, args []string) (io.ReadCloser, error)
}

// OSRunner runs real commands via exec.CommandContext — never through a shell.
type OSRunner struct {
	LocalTimeout time.Duration
	SSHTimeout   time.Duration
}

func (r OSRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader) (string, string, error) {
	if r.LocalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.LocalTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// RunSSH invokes the system ssh client so agent/config/known_hosts keep
// working. Every remote argument is POSIX single-quoted before joining.
func (r OSRunner) RunSSH(ctx context.Context, cluster Cluster, args []string) (string, string, error) {
	if r.SSHTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.SSHTimeout)
		defer cancel()
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", strconv.Itoa(cluster.K8sMasterPort)}
	if cluster.K8sMasterKeyPath != nil && *cluster.K8sMasterKeyPath != "" {
		sshArgs = append(sshArgs, "-i", *cluster.K8sMasterKeyPath)
	}
	sshArgs = append(sshArgs, "--", fmt.Sprintf("%s@%s", cluster.K8sMasterUser, cluster.K8sMasterHost), shellJoin(args))
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// RunSSHStdin is RunSSH with stdin forwarded to the remote command —
// required for `kubectl replace -f -`.
func (r OSRunner) RunSSHStdin(ctx context.Context, cluster Cluster, args []string, stdin io.Reader) (string, string, error) {
	if r.SSHTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.SSHTimeout)
		defer cancel()
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", strconv.Itoa(cluster.K8sMasterPort)}
	if cluster.K8sMasterKeyPath != nil && *cluster.K8sMasterKeyPath != "" {
		sshArgs = append(sshArgs, "-i", *cluster.K8sMasterKeyPath)
	}
	sshArgs = append(sshArgs, "--", fmt.Sprintf("%s@%s", cluster.K8sMasterUser, cluster.K8sMasterHost), shellJoin(args))
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = stdin
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// sshStream is the live half of RunSSHStream: reads come from the ssh
// process's stdout; Close kills the process and reaps it. ctx cancel
// also kills it via CommandContext.
type sshStream struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
	done   chan error
}

func (s *sshStream) Read(p []byte) (int, error) { return s.stdout.Read(p) }

func (s *sshStream) Close() error {
	_ = s.stdout.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return <-s.done
}

// RunSSHStream starts the remote command with a piped stdout instead of
// buffering — the caller reads until EOF or ctx cancel.
func (r OSRunner) RunSSHStream(ctx context.Context, cluster Cluster, args []string) (io.ReadCloser, error) {
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", strconv.Itoa(cluster.K8sMasterPort)}
	if cluster.K8sMasterKeyPath != nil && *cluster.K8sMasterKeyPath != "" {
		sshArgs = append(sshArgs, "-i", *cluster.K8sMasterKeyPath)
	}
	sshArgs = append(sshArgs, "--", fmt.Sprintf("%s@%s", cluster.K8sMasterUser, cluster.K8sMasterHost), shellJoin(args))
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &sshStream{stdout: stdout, cmd: cmd, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	return s, nil
}

// shellJoin renders args as a remote command line with strict POSIX
// single-quote escaping on each argument.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// MockRunner produces deterministic command results for dev/mock mode.
type MockRunner struct {
	mu    sync.Mutex
	Calls []string
}

func (m *MockRunner) Run(_ context.Context, name string, args []string, _ io.Reader) (string, string, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	m.mu.Lock()
	m.Calls = append(m.Calls, cmd)
	m.mu.Unlock()
	if name == "docker" && len(args) > 0 && args[0] == "load" {
		return "Loaded image: mock-image:latest", "", nil
	}
	return fmt.Sprintf("[MOCK] 命令已记录: %s", cmd), "", nil
}

func (m *MockRunner) RunSSH(_ context.Context, cluster Cluster, args []string) (string, string, error) {
	remote := strings.Join(args, " ")
	m.mu.Lock()
	m.Calls = append(m.Calls, fmt.Sprintf("ssh %s@%s %s", cluster.K8sMasterUser, cluster.K8sMasterHost, remote))
	m.mu.Unlock()
	return fmt.Sprintf("[MOCK] ssh %s@%s: %s", cluster.K8sMasterUser, cluster.K8sMasterHost, remote), "", nil
}

// RunSSHStdin mirrors RunSSH for the mock — stdin content is ignored,
// the canned response keeps dev/mock mode deterministic.
func (m *MockRunner) RunSSHStdin(ctx context.Context, cluster Cluster, args []string, _ io.Reader) (string, string, error) {
	return m.RunSSH(ctx, cluster, args)
}

// RunSSHStream mirrors RunSSH for the mock — a deterministic canned
// stream so dev/mock mode exercises the same read loop.
func (m *MockRunner) RunSSHStream(ctx context.Context, cluster Cluster, args []string) (io.ReadCloser, error) {
	remote := strings.Join(args, " ")
	m.mu.Lock()
	m.Calls = append(m.Calls, fmt.Sprintf("ssh %s@%s %s", cluster.K8sMasterUser, cluster.K8sMasterHost, remote))
	m.mu.Unlock()
	return io.NopCloser(strings.NewReader(fmt.Sprintf("[MOCK] stream %s@%s: %s\n", cluster.K8sMasterUser, cluster.K8sMasterHost, remote))), nil
}

// Recorded returns a copy of the commands seen so far.
func (m *MockRunner) Recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.Calls...)
}
