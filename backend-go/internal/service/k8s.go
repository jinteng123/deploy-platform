package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// KubernetesRuntime is the per-cluster K8s access surface used by handlers
// and the deploy/rollback pipelines.
type KubernetesRuntime interface {
	Pods(ctx context.Context, namespace string) ([]map[string]any, error)
	PodLogs(ctx context.Context, namespace, pod string, tail int64) (string, error)
	RestartPod(ctx context.Context, namespace, pod string) error
	DeploymentInfo(ctx context.Context, namespace, name string) (map[string]any, error)
	DeploymentEvents(ctx context.Context, namespace, name string) (string, error)
	CurrentImage(ctx context.Context, namespace, deployment, container string) (string, error)
	SetImage(ctx context.Context, namespace, deployment, container, image string) error
	// RolloutStatus blocks until the deployment's rollout completes, fails,
	// or ctx ends — `kubectl rollout status` semantics. The returned string
	// is a kubectl-style summary for pipeline step output.
	RolloutStatus(ctx context.Context, namespace, deployment string) (string, error)
	// ListResources/GetResource expose the allowlisted generic resource
	// layer. def always comes from kube.LookupResource — runtimes never
	// see an arbitrary client-supplied GVR.
	ListResources(ctx context.Context, def kube.ResourceDef, namespace string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	GetResource(ctx context.Context, def kube.ResourceDef, namespace, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error)
	// ServerVersion returns the API server's git version for the cluster
	// overview.
	ServerVersion(ctx context.Context) (string, error)
	// ListMetrics lists metrics.k8s.io/v1beta1 "nodes" or "pods" (namespace
	// only applies to pods, empty = all namespaces). metrics-server absence
	// surfaces as a normal error; callers degrade, never fail the page.
	ListMetrics(ctx context.Context, resource, namespace string) (*unstructured.UnstructuredList, error)
	// PatchResource applies a service-built patch and returns the updated
	// object; DeleteResource deletes one object (DeleteOptions may carry a
	// resourceVersion precondition). Conflicts/RBAC errors propagate
	// unchanged so handlers translate them faithfully.
	PatchResource(ctx context.Context, def kube.ResourceDef, namespace, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions) (*unstructured.Unstructured, error)
	DeleteResource(ctx context.Context, def kube.ResourceDef, namespace, name string, opts metav1.DeleteOptions) error
	// UpdateResource replaces a whole object (the YAML editor's save
	// path). The object's resourceVersion is the optimistic-concurrency
	// precondition — a stale value must surface as Conflict, never be
	// silently dropped.
	UpdateResource(ctx context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error)
	// CreateResource creates one allowlisted object from a manifest
	// (POST /resources — the create-dialog path). CreateOptions.DryRun
	// carries server-side dry run; AlreadyExists propagates so the
	// handler maps it to 409.
	CreateResource(ctx context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error)
	// PodLogStream opens a follow-capable log stream; the returned reader
	// is bound to ctx so a closed browser session kills the upstream
	// connection with it.
	PodLogStream(ctx context.Context, namespace, pod string, opts kube.LogOptions) (io.ReadCloser, error)
	// ExecPod runs an interactive session in a container and blocks until
	// it ends or ctx is cancelled. SSH runtimes return ErrExecUnsupported:
	// a piped kubectl exec cannot carry a real PTY or resize events.
	ExecPod(ctx context.Context, namespace, pod string, spec kube.ExecSpec, io kube.ExecIO) error
	// AccessRules returns the caller-identity's resource rules in one
	// namespace (SelfSubjectRulesReview semantics). It powers capability
	// introspection for UI gating — enforcement itself is the apiserver's
	// job via impersonation.
	AccessRules(ctx context.Context, namespace string) ([]kube.ResourceRule, error)
}

// ErrExecUnsupported marks exec requests on runtimes that cannot carry an
// interactive session (legacy_ssh). The terminal endpoint surfaces it as
// the session-end frame reason right after upgrade.
var ErrExecUnsupported = errors.New("该集群接入方式不支持容器终端")

// ErrClusterDisabled marks requests against a disabled cluster. Handlers
// translate it to 403 so callers can tell a platform-level rejection apart
// from a backend failure.
var ErrClusterDisabled = errors.New("已禁用")

// K8sService resolves the right runtime per cluster: mock when
// cfg.MockRuntime, a fresh client-go client decrypted per call for
// kubeconfig/token connections, otherwise kubectl over SSH for legacy_ssh.
type K8sService struct {
	mock   bool
	runner CommandRunner
	cipher *CredentialCipher
	// inject bypasses connection resolution in tests.
	inject KubernetesRuntime

	mu         sync.Mutex
	clients    map[clientKey]*kube.Client
	streams    map[int]map[int64]context.CancelFunc
	nextStream int64
}

func NewK8sService(cfg config.Config, runner CommandRunner, cipher *CredentialCipher) *K8sService {
	return &K8sService{
		mock:    cfg.MockRuntime,
		runner:  runner,
		cipher:  cipher,
		clients: map[clientKey]*kube.Client{},
		streams: map[int]map[int64]context.CancelFunc{},
	}
}

// clientKey pins a cached client to (cluster, credential version, K8s
// identity). UpdatedAt bumps on every cluster write — credential included —
// so an update naturally produces a new key and the stale entry is dropped
// by InvalidateCluster. The key NEVER carries credential material.
type clientKey struct {
	clusterID int
	version   time.Time
	ident     string
}

// clientCacheCap bounds cached clients; clusters×identities stay far below
// this in practice, and a full flush is cheaper than a real LRU here.
const clientCacheCap = 128

// identKey renders a user's K8s identity for the cache key — users sharing
// an impersonation target share the client (and its discovery cache).
func identKey(u *User) string {
	if u == nil || u.K8sUsername == "" {
		return ""
	}
	return u.K8sUsername + "\x00" + strings.Join(u.K8sGroups, "\x00")
}

// InvalidateCluster drops every cached client for the cluster and cancels
// its tracked live streams. Called after update/disable/delete so the next
// request rebuilds from the new credential and no watcher outlives the
// cluster it was opened against.
func (s *K8sService) InvalidateCluster(clusterID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.clients {
		if k.clusterID == clusterID {
			delete(s.clients, k)
		}
	}
	for id, cancel := range s.streams[clusterID] {
		cancel()
		delete(s.streams[clusterID], id)
	}
	delete(s.streams, clusterID)
}

// TrackStream registers a live session's cancel func under the cluster so
// InvalidateCluster can terminate it. The returned func untracks on
// session end.
func (s *K8sService) TrackStream(clusterID int, cancel context.CancelFunc) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextStream
	s.nextStream++
	if s.streams[clusterID] == nil {
		s.streams[clusterID] = map[int64]context.CancelFunc{}
	}
	s.streams[clusterID][id] = cancel
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.streams[clusterID], id)
	}
}

func (s *K8sService) For(cluster Cluster) (KubernetesRuntime, error) {
	return s.ForUser(cluster, nil)
}

// ForUser resolves the runtime bound to a platform user's Kubernetes
// identity. Direct clients set rest.Config impersonation; SSH prepends
// kubectl --as/--as-group flags; both end at the same apiserver RBAC
// check. A nil user (CD worker, webhook) keeps the credential identity —
// the platform's separate service identity.
func (s *K8sService) ForUser(cluster Cluster, u *User) (KubernetesRuntime, error) {
	if !cluster.Enabled {
		return nil, fmt.Errorf("集群 %s %w", cluster.Name, ErrClusterDisabled)
	}
	if s.mock {
		return MockKubernetes{}, nil
	}
	if s.inject != nil {
		return s.inject, nil
	}
	switch cluster.ConnectionType {
	case "kubeconfig", "token":
		return s.cachedClient(cluster, u)
	case "", "legacy_ssh":
		runner := s.runner
		if u != nil && u.K8sUsername != "" {
			runner = &impersonatingRunner{inner: runner, username: u.K8sUsername, groups: u.K8sGroups}
		}
		return &sshKubernetes{runner: runner, cluster: cluster}, nil
	default:
		return nil, fmt.Errorf("未知集群连接类型: %s", cluster.ConnectionType)
	}
}

// cachedClient resolves a direct-connection client through the
// (cluster, credential-version, identity) cache. The transient
// PlainCredential path (test-connection) bypasses the cache entirely —
// nothing plaintext is ever cached. A credential rotation bumps
// cluster.UpdatedAt, producing a new key; the stale entry dies on the next
// InvalidateCluster call from the update handler.
func (s *K8sService) cachedClient(cluster Cluster, u *User) (*kube.Client, error) {
	if cluster.PlainCredential == "" {
		key := clientKey{clusterID: cluster.ID, version: cluster.UpdatedAt, ident: identKey(u)}
		s.mu.Lock()
		if c, ok := s.clients[key]; ok {
			s.mu.Unlock()
			return c, nil
		}
		if len(s.clients) >= clientCacheCap {
			s.clients = map[clientKey]*kube.Client{}
		}
		s.mu.Unlock()
		c, err := s.buildUserClient(cluster, u)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.clients[key] = c
		s.mu.Unlock()
		return c, nil
	}
	return s.buildUserClient(cluster, u)
}

func (s *K8sService) buildUserClient(cluster Cluster, u *User) (*kube.Client, error) {
	c, err := s.directClient(cluster)
	if err != nil {
		return nil, err
	}
	if u == nil || u.K8sUsername == "" {
		return c, nil
	}
	return c.ForUser(kube.Identity{Username: u.K8sUsername, Groups: u.K8sGroups})
}

// impersonatingRunner injects kubectl impersonation flags into every
// remote command whose argv starts with "kubectl" — the ssh transport
// cannot set HTTP headers, so --as/--as-group carry the identity to the
// apiserver's own RBAC evaluation instead.
type impersonatingRunner struct {
	inner    CommandRunner
	username string
	groups   []string
}

func (r *impersonatingRunner) args(args []string) []string {
	if len(args) == 0 || args[0] != "kubectl" {
		return args
	}
	out := []string{"kubectl", "--as=" + r.username}
	for _, g := range r.groups {
		out = append(out, "--as-group="+g)
	}
	return append(out, args[1:]...)
}

func (r *impersonatingRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader) (string, string, error) {
	return r.inner.Run(ctx, name, args, stdin)
}
func (r *impersonatingRunner) RunSSH(ctx context.Context, cluster Cluster, args []string) (string, string, error) {
	return r.inner.RunSSH(ctx, cluster, r.args(args))
}
func (r *impersonatingRunner) RunSSHStdin(ctx context.Context, cluster Cluster, args []string, stdin io.Reader) (string, string, error) {
	return r.inner.RunSSHStdin(ctx, cluster, r.args(args), stdin)
}
func (r *impersonatingRunner) RunSSHStream(ctx context.Context, cluster Cluster, args []string) (io.ReadCloser, error) {
	return r.inner.RunSSHStream(ctx, cluster, r.args(args))
}

// directClient decrypts the stored credential and builds a fresh client —
// deliberately uncached so credential updates take effect immediately. A
// transient PlainCredential (test-connection flow) bypasses decryption.
func (s *K8sService) directClient(cluster Cluster) (*kube.Client, error) {
	plain := cluster.PlainCredential
	if plain == "" {
		if cluster.CredentialCiphertext == nil || *cluster.CredentialCiphertext == "" {
			return nil, fmt.Errorf("集群 %s 未配置凭据", cluster.Name)
		}
		if s.cipher == nil {
			return nil, ErrCipherNotConfigured
		}
		p, err := s.cipher.Decrypt(*cluster.CredentialCiphertext)
		if err != nil {
			return nil, fmt.Errorf("集群 %s 凭据解密失败", cluster.Name)
		}
		plain = p
	}
	switch cluster.ConnectionType {
	case "kubeconfig":
		return kube.NewFromKubeconfigData([]byte(plain))
	case "token":
		apiServer := ""
		if cluster.APIServer != nil {
			apiServer = *cluster.APIServer
		}
		var ca []byte
		if cluster.CACert != nil {
			ca = []byte(*cluster.CACert)
		}
		return kube.NewFromToken(apiServer, plain, ca, cluster.InsecureSkipTLSVerify)
	default:
		return nil, fmt.Errorf("未知集群连接类型: %s", cluster.ConnectionType)
	}
}

// TestConnection verifies the cluster is reachable. Direct clients call
// discovery ServerVersion; legacy_ssh runs a single read-only kubectl
// version command; mock returns "mock". It never falls back to mock.
func (s *K8sService) TestConnection(ctx context.Context, cluster Cluster) (string, error) {
	if s.mock {
		return "mock", nil
	}
	if s.inject != nil {
		return "mock", nil
	}
	if !cluster.Enabled {
		return "", fmt.Errorf("集群 %s %w", cluster.Name, ErrClusterDisabled)
	}
	switch cluster.ConnectionType {
	case "kubeconfig", "token":
		c, err := s.directClient(cluster)
		if err != nil {
			return "", err
		}
		return c.ServerVersion(ctx)
	case "", "legacy_ssh":
		stdout, stderr, err := s.runner.RunSSH(ctx, cluster, []string{"kubectl", "version", "--output=json"})
		if err != nil {
			return "", fmt.Errorf("连接测试失败: %s", errText(stderr, err))
		}
		return strings.TrimSpace(stdout), nil
	default:
		return "", fmt.Errorf("未知集群连接类型: %s", cluster.ConnectionType)
	}
}

type sshKubernetes struct {
	runner  CommandRunner
	cluster Cluster
}

func (s *sshKubernetes) kubectl(ctx context.Context, failPrefix string, args ...string) (string, error) {
	stdout, stderr, err := s.runner.RunSSH(ctx, s.cluster, append([]string{"kubectl"}, args...))
	if err != nil {
		return "", fmt.Errorf("%s: %s", failPrefix, errText(stderr, err))
	}
	return stdout, nil
}

func (s *sshKubernetes) Pods(ctx context.Context, namespace string) ([]map[string]any, error) {
	stdout, err := s.kubectl(ctx, "获取 Pod 列表失败", "get", "pods", "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		return nil, fmt.Errorf("解析 Pod 列表失败: %w", err)
	}
	items, _ := data["items"].([]any)
	result := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			result = append(result, kube.PodSummary(m))
		}
	}
	return result, nil
}

func (s *sshKubernetes) PodLogs(ctx context.Context, namespace, pod string, tail int64) (string, error) {
	return s.kubectl(ctx, "获取日志失败", "logs", pod, "-n", namespace, fmt.Sprintf("--tail=%d", tail))
}

func (s *sshKubernetes) RestartPod(ctx context.Context, namespace, pod string) error {
	_, err := s.kubectl(ctx, "重启 Pod 失败", "delete", "pod", pod, "-n", namespace)
	return err
}

func (s *sshKubernetes) deploymentJSON(ctx context.Context, namespace, name string) (map[string]any, error) {
	stdout, err := s.kubectl(ctx, "获取 Deployment 信息失败", "get", "deployment/"+name, "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		return nil, fmt.Errorf("解析 Deployment 信息失败: %w", err)
	}
	return data, nil
}

func (s *sshKubernetes) DeploymentInfo(ctx context.Context, namespace, name string) (map[string]any, error) {
	data, err := s.deploymentJSON(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return kube.DeploymentInfoFromMap(data), nil
}

func (s *sshKubernetes) DeploymentEvents(ctx context.Context, namespace, name string) (string, error) {
	return s.kubectl(ctx, "获取事件失败",
		"get", "events", "-n", namespace,
		"--field-selector", "involvedObject.name="+name,
		"--sort-by=.lastTimestamp")
}

func (s *sshKubernetes) CurrentImage(ctx context.Context, namespace, deployment, container string) (string, error) {
	data, err := s.deploymentJSON(ctx, namespace, deployment)
	if err != nil {
		return "", err
	}
	return kube.ContainerImage(data, container)
}

func (s *sshKubernetes) SetImage(ctx context.Context, namespace, deployment, container, image string) error {
	_, err := s.kubectl(ctx, "设置镜像失败",
		"set", "image", "deployment/"+deployment, container+"="+image, "-n", namespace)
	return err
}

// RolloutWaitTimeout bounds the post-set-image health wait. Without it a
// rollout stuck on ImagePullBackOff/ProgressDeadlineExceeded would hold the
// worker forever (kubectl's own --timeout defaults to 0 = wait forever).
const RolloutWaitTimeout = 2 * time.Minute

func (s *sshKubernetes) RolloutStatus(ctx context.Context, namespace, deployment string) (string, error) {
	return s.kubectl(ctx, "等待滚动更新失败", "rollout", "status", "deployment/"+deployment,
		"-n", namespace, fmt.Sprintf("--timeout=%ds", int(RolloutWaitTimeout/time.Second)))
}

// PodLogStream pipes `kubectl logs -f` stdout through the SSH session.
// The stream is bound to ctx — closing it (or a disconnect) kills the
// ssh process and the remote kubectl with it.
func (s *sshKubernetes) PodLogStream(ctx context.Context, namespace, pod string, opts kube.LogOptions) (io.ReadCloser, error) {
	args := []string{"kubectl", "logs", pod, "-n", namespace}
	if opts.Follow {
		args = append(args, "-f")
	}
	if opts.Tail != nil {
		args = append(args, fmt.Sprintf("--tail=%d", *opts.Tail))
	}
	if opts.Container != "" {
		args = append(args, "-c", opts.Container)
	}
	return s.runner.RunSSHStream(ctx, s.cluster, args)
}

// ExecPod is unsupported over SSH: `kubectl exec` through a piped ssh
// command cannot carry an interactive PTY or window-resize events.
func (s *sshKubernetes) ExecPod(context.Context, string, string, kube.ExecSpec, kube.ExecIO) error {
	return ErrExecUnsupported
}

// AccessRules shells out to `kubectl auth can-i --list` — the SSH runtime
// has no REST path to SelfSubjectRulesReview, but can-i evaluates the
// same server-side answer (the impersonatingRunner has already injected
// --as flags when the session is user-bound).
func (s *sshKubernetes) AccessRules(ctx context.Context, namespace string) ([]kube.ResourceRule, error) {
	args := []string{"kubectl", "auth", "can-i", "--list"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, stderr, err := s.runner.RunSSH(ctx, s.cluster, args)
	if err != nil {
		return nil, fmt.Errorf("权限规则查询失败: %s", errText(stderr, err))
	}
	rules := parseCanIList(out)
	if strings.TrimSpace(out) == "" || (len(rules) == 0 && !strings.Contains(out, "Resources")) {
		return nil, fmt.Errorf("权限规则查询返回了无法识别的结果")
	}
	return rules, nil
}

// parseCanIList reads the fixed `kubectl auth can-i --list` table:
//
//	Resources                          Non-Resource URLs  Resource Names  Verbs
//	pods                               []                 []              [get list watch]
//	deployments.apps                   []                 []              [get list]
//
// Resource names carry a dotted API group suffix; "*" means wildcard.
func parseCanIList(out string) []kube.ResourceRule {
	var rules []kube.ResourceRule
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (i == 0 && strings.HasPrefix(line, "Resources")) {
			continue
		}
		res, resourceNames, verbs, ok := canIFields(line)
		if !ok {
			continue
		}
		group := ""
		if idx := strings.IndexByte(res, '.'); idx >= 0 {
			res, group = res[:idx], res[idx+1:]
		}
		rules = append(rules, kube.ResourceRule{
			Resources:     []string{res},
			APIGroups:     []string{group},
			ResourceNames: resourceNames,
			Verbs:         verbs,
		})
	}
	return rules
}

// canIFields extracts the resource, resource-name and verb columns from one
// can-i row. Resource-name restrictions must be preserved so callers do not
// mistake access to one named object for a resource-wide permission.
func canIFields(line string) (resource string, resourceNames, verbs []string, ok bool) {
	open := strings.Index(line, "[")
	if open <= 0 {
		return "", nil, nil, false
	}
	resource = strings.TrimSpace(line[:open])
	rest := line[open:]
	var cols [][]string
	for len(rest) > 0 {
		if !strings.HasPrefix(rest, "[") {
			return "", nil, nil, false
		}
		end := strings.Index(rest, "]")
		if end < 0 {
			return "", nil, nil, false
		}
		cols = append(cols, strings.Fields(rest[1:end]))
		rest = strings.TrimSpace(rest[end+1:])
	}
	if len(cols) != 3 {
		return "", nil, nil, false
	}
	return resource, cols[1], cols[2], true
}

// kubectlArgs renders the resource selector plus namespace/selector flags
// for a registry def. Namespaced resources with an empty namespace list
// across all namespaces; cluster-scoped resources never get -n.
func kubectlResourceArgs(def kube.ResourceDef, namespace string, opts metav1.ListOptions) []string {
	args := []string{"get", def.KubectlResource()}
	if def.Namespaced {
		if namespace == "" {
			args = append(args, "--all-namespaces")
		} else {
			args = append(args, "-n", namespace)
		}
	}
	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}
	if opts.FieldSelector != "" {
		args = append(args, "--field-selector", opts.FieldSelector)
	}
	return args
}

// kubectlErr maps kubectl stderr to a Kubernetes-flavored error so
// handlers can apply the same status translation as the direct client.
// kubectl prints apiserver rejections as `Error from server (Reason): …`
// — mapping each reason keeps HTTP semantics (404/403/409/400) and audit
// classification identical across the direct and SSH runtimes.
func kubectlErr(prefix string, stderr string, err error, def kube.ResourceDef, name string) error {
	text := errText(stderr, err)
	gr := def.GVR().GroupResource()
	switch {
	case strings.Contains(text, "(NotFound)"):
		return apierrors.NewNotFound(gr, name)
	case strings.Contains(text, "(Forbidden)"):
		return apierrors.NewForbidden(gr, name, errors.New(text))
	case strings.Contains(text, "(AlreadyExists)"):
		return apierrors.NewAlreadyExists(gr, name)
	case strings.Contains(text, "(Conflict)"):
		return apierrors.NewConflict(gr, name, errors.New(text))
	case strings.Contains(text, "(Invalid)"):
		// kubectl's text IS the apiserver's Status message — keep it
		// verbatim instead of re-deriving field causes from nothing.
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusUnprocessableEntity,
			Reason:  metav1.StatusReasonInvalid,
			Details: &metav1.StatusDetails{Group: gr.Group, Kind: def.GVK().Kind, Name: name},
			Message: text,
		}}
	case strings.Contains(text, "(BadRequest)"):
		return apierrors.NewBadRequest(text)
	default:
		return fmt.Errorf("%s: %s", prefix, text)
	}
}

func (s *sshKubernetes) ListResources(ctx context.Context, def kube.ResourceDef, namespace string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	args := append(kubectlResourceArgs(def, namespace, opts), "-o", "json")
	stdout, stderr, err := s.runner.RunSSH(ctx, s.cluster, append([]string{"kubectl"}, args...))
	if err != nil {
		return nil, kubectlErr("获取资源列表失败", stderr, err, def, "")
	}
	var list unstructured.UnstructuredList
	if err := list.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析资源列表失败: %w", err)
	}
	return &list, nil
}

func (s *sshKubernetes) GetResource(ctx context.Context, def kube.ResourceDef, namespace, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error) {
	args := []string{"get", def.KubectlResource(), name, "-o", "json"}
	if def.Namespaced && namespace != "" {
		args = append(args, "-n", namespace)
	}
	stdout, stderr, err := s.runner.RunSSH(ctx, s.cluster, append([]string{"kubectl"}, args...))
	if err != nil {
		return nil, kubectlErr("获取资源详情失败", stderr, err, def, name)
	}
	var obj unstructured.Unstructured
	if err := obj.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析资源详情失败: %w", err)
	}
	return &obj, nil
}

func (s *sshKubernetes) ServerVersion(ctx context.Context) (string, error) {
	stdout, err := s.kubectl(ctx, "获取集群版本失败", "version", "-o", "json")
	if err != nil {
		return "", err
	}
	var v struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		return "", fmt.Errorf("解析集群版本失败: %w", err)
	}
	return v.ServerVersion.GitVersion, nil
}

// PatchResource shells out to kubectl patch; -o json returns the updated
// object so callers see server-side truth. resourceVersion rides inside
// the patch body, so the apiserver enforces it on this path too.
func (s *sshKubernetes) PatchResource(ctx context.Context, def kube.ResourceDef, namespace, name string, pt types.PatchType, data []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	patchType := ""
	switch pt {
	case types.JSONPatchType:
		patchType = "json"
	case types.MergePatchType:
		patchType = "merge"
	case types.StrategicMergePatchType:
		patchType = "strategic"
	default:
		return nil, NewValidationError("SSH kubectl 不支持该 patch 类型")
	}
	args := []string{"patch", def.KubectlResource(), name, "--type=" + patchType, "-p", string(data), "-o", "json"}
	if def.Namespaced && namespace != "" {
		args = append(args, "-n", namespace)
	}
	stdout, stderr, err := s.runner.RunSSH(ctx, s.cluster, append([]string{"kubectl"}, args...))
	if err != nil {
		return nil, kubectlErr("更新资源失败", stderr, err, def, name)
	}
	var obj unstructured.Unstructured
	if err := obj.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析更新结果失败: %w", err)
	}
	return &obj, nil
}

func (s *sshKubernetes) DeleteResource(ctx context.Context, def kube.ResourceDef, namespace, name string, opts metav1.DeleteOptions) error {
	// kubectl delete can't carry a resourceVersion precondition, so a
	// requested rv is enforced here: fetch, compare, then delete. A
	// mismatch surfaces the same 409 Conflict the apiserver would emit.
	rv := ""
	if opts.Preconditions != nil && opts.Preconditions.ResourceVersion != nil {
		rv = strings.TrimSpace(*opts.Preconditions.ResourceVersion)
	}
	if rv != "" {
		obj, err := s.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if obj.GetResourceVersion() != rv {
			return apierrors.NewConflict(def.GVR().GroupResource(), name,
				fmt.Errorf("resourceVersion %q does not match current %q", rv, obj.GetResourceVersion()))
		}
	}
	args := []string{"delete", def.KubectlResource(), name}
	if def.Namespaced && namespace != "" {
		args = append(args, "-n", namespace)
	}
	_, stderr, err := s.runner.RunSSH(ctx, s.cluster, append([]string{"kubectl"}, args...))
	if err != nil {
		return kubectlErr("删除资源失败", stderr, err, def, name)
	}
	return nil
}

// UpdateResource pipes the manifest JSON into `kubectl replace -f -`.
// kubectl replace requires metadata.resourceVersion and forwards it to
// the apiserver, so optimistic concurrency holds on this path too —
// the stdin channel is the only reason RunSSHStdin exists.
func (s *sshKubernetes) UpdateResource(ctx context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("序列化资源失败: %w", err)
	}
	args := []string{"replace", "-f", "-", "-o", "json"}
	// Server-side dry run validates against the apiserver (RBAC,
	// webhooks, schema) without persisting — the YAML editor's
	// pre-apply check.
	if len(opts.DryRun) > 0 {
		args = append(args, "--dry-run=server")
	}
	if def.Namespaced && namespace != "" {
		args = append(args, "-n", namespace)
	}
	stdout, stderr, err := s.runner.RunSSHStdin(ctx, s.cluster, append([]string{"kubectl"}, args...), strings.NewReader(string(data)))
	if err != nil {
		return nil, kubectlErr("更新资源失败", stderr, err, def, obj.GetName())
	}
	var out unstructured.Unstructured
	if err := out.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析更新结果失败: %w", err)
	}
	return &out, nil
}

// CreateResource pipes the manifest JSON into `kubectl create -f -`,
// the stdin twin of UpdateResource's replace path.
func (s *sshKubernetes) CreateResource(ctx context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	data, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("序列化资源失败: %w", err)
	}
	args := []string{"create", "-f", "-", "-o", "json"}
	// Server-side dry run validates against the apiserver without
	// persisting — the create dialog's pre-apply check.
	if len(opts.DryRun) > 0 {
		args = append(args, "--dry-run=server")
	}
	if def.Namespaced && namespace != "" {
		args = append(args, "-n", namespace)
	}
	stdout, stderr, err := s.runner.RunSSHStdin(ctx, s.cluster, append([]string{"kubectl"}, args...), strings.NewReader(string(data)))
	if err != nil {
		return nil, kubectlErr("创建资源失败", stderr, err, def, obj.GetName())
	}
	var out unstructured.Unstructured
	if err := out.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析创建结果失败: %w", err)
	}
	return &out, nil
}

// ListMetrics reads the aggregated metrics API through kubectl --raw so
// the SSH path never depends on a metrics.k8s.io typed resource existing
// in kubectl's own discovery cache.
func (s *sshKubernetes) ListMetrics(ctx context.Context, resource, namespace string) (*unstructured.UnstructuredList, error) {
	gvr, err := kube.MetricsResource(resource)
	if err != nil {
		return nil, err
	}
	path := "/apis/" + gvr.Group + "/" + gvr.Version + "/"
	if resource == "pods" && namespace != "" {
		path += "namespaces/" + namespace + "/"
	}
	stdout, err := s.kubectl(ctx, "获取资源指标失败", "get", "--raw", path+gvr.Resource)
	if err != nil {
		return nil, err
	}
	var list unstructured.UnstructuredList
	if err := list.UnmarshalJSON([]byte(stdout)); err != nil {
		return nil, fmt.Errorf("解析资源指标失败: %w", err)
	}
	return &list, nil
}

var mockServices = []struct {
	name     string
	tag      string
	port     int
	restarts int
	phase    string
}{
	{"demo-gateway", "20260709175002", 8080, 0, "Running"},
	{"demo-hospital-api", "20260702152220", 8080, 1, "Running"},
	{"demo-hospital-web", "20260708110530", 80, 0, "Running"},
	{"demo-user-api", "20260707185356", 8080, 0, "Running"},
	{"demo-user-web", "20260707190012", 80, 0, "Running"},
	{"demo-agent-core-api", "20260706140800", 8080, 0, "Running"},
	{"demo-ai-assistant-web", "20260705093000", 80, 0, "Running"},
	{"demo-angel-agent-web", "20260704160000", 80, 0, "Running"},
	{"demo-data-asset-api", "20260703120000", 8080, 2, "Running"},
	{"demo-data-asset-web", "20260703121500", 80, 0, "Running"},
	{"demo-out-gateway", "20260701080000", 8080, 0, "Running"},
	{"demo-platform-map-web", "20260630150000", 80, 0, "Running"},
	{"demo-proxy", "20260709200000", 80, 0, "Running"},
}

const mockRegistry = "registry.example.com/Demo"
const mockHash = "7b8c9"

// MockKubernetes returns predictable data mirroring the Python dev mock.
type MockKubernetes struct{}

func (MockKubernetes) Pods(_ context.Context, namespace string) ([]map[string]any, error) {
	ns := namespace
	if ns == "" {
		ns = "dev-demo-002"
	}
	result := make([]map[string]any, 0, len(mockServices))
	for _, svc := range mockServices {
		deploy := ns + "-" + svc.name
		result = append(result, map[string]any{
			"name":      deploy + "-" + mockHash,
			"status":    svc.phase,
			"ready":     "1/1",
			"restarts":  svc.restarts,
			"image":     mockRegistry + "/" + svc.name + ":" + svc.tag,
			"app_label": deploy,
		})
	}
	return result, nil
}

func (MockKubernetes) PodLogs(_ context.Context, _, pod string, _ int64) (string, error) {
	lines := []string{
		fmt.Sprintf("2026-07-10 10:00:01 INFO  [main] Application starting... (pod: %s)", pod),
		"2026-07-10 10:00:02 INFO  [main] Connected to database",
		"2026-07-10 10:00:03 INFO  [main] Listening on port 8080",
		"2026-07-10 10:00:05 INFO  [main] Health check endpoint registered",
		"2026-07-10 10:00:10 INFO  [http] GET /health 200 2ms",
		"2026-07-10 10:01:00 INFO  [http] GET /api/v1/status 200 5ms",
		"2026-07-10 10:01:15 INFO  [http] POST /api/v1/data 201 45ms",
		"2026-07-10 10:02:30 WARN  [pool] Connection pool nearly full (8/10)",
		"2026-07-10 10:03:00 INFO  [http] GET /api/v1/users 200 12ms",
		"2026-07-10 10:05:00 INFO  [scheduler] Heartbeat OK",
		"[MOCK] 以上为模拟日志数据",
	}
	return strings.Join(lines, "\n"), nil
}

func (MockKubernetes) RestartPod(_ context.Context, _, _ string) error { return nil }

func (MockKubernetes) DeploymentInfo(_ context.Context, namespace, name string) (map[string]any, error) {
	ns := namespace
	if ns == "" {
		ns = "dev-demo-002"
	}
	svcName := strings.TrimPrefix(name, ns+"-")
	svc := mockServices[0]
	for _, s := range mockServices {
		if s.name == svcName {
			svc = s
			break
		}
	}
	img := mockRegistry + "/" + svc.name + ":" + svc.tag
	raw := map[string]any{
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"replicas": 1,
			"strategy": map[string]any{"type": "Recreate"},
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{
					"name":  svc.name,
					"image": img,
					"ports": []any{map[string]any{"containerPort": svc.port}},
					"envFrom": []any{
						map[string]any{"configMapRef": map[string]any{"name": ns + "-demo-common-config"}},
						map[string]any{"configMapRef": map[string]any{"name": ns + "-" + svc.name + "-config"}},
					},
					"env": []any{
						map[string]any{"name": "SPRING_PROFILES_ACTIVE", "value": "test"},
						map[string]any{"name": "SERVER_PORT", "value": fmt.Sprintf("%d", svc.port)},
						map[string]any{"name": "DB_PASSWORD", "valueFrom": map[string]any{
							"secretKeyRef": map[string]any{"name": ns + "-demo-db-secrets", "key": "mysql-root-password"},
						}},
					},
					"resources": map[string]any{},
				},
			}}},
		},
		"status": map[string]any{
			"replicas": 1, "readyReplicas": 1, "availableReplicas": 1,
			"conditions": []any{
				map[string]any{"type": "Available", "status": "True", "lastTransitionTime": "2026-07-09T10:00:00Z"},
				map[string]any{"type": "Progressing", "status": "True", "lastTransitionTime": "2026-07-09T10:00:00Z"},
			},
		},
	}
	return kube.DeploymentInfoFromMap(raw), nil
}

func (MockKubernetes) DeploymentEvents(_ context.Context, _ string, name string) (string, error) {
	obj := name
	if obj == "" {
		obj = "dev-demo-002-demo-gateway"
	}
	lines := []string{
		"LAST SEEN   TYPE     REASON             OBJECT                                      MESSAGE",
		fmt.Sprintf("5m          Normal   ScalingReplicaSet   deployment/%s   Scaled up replica set %s-%s to 1", obj, obj, mockHash),
		fmt.Sprintf("5m          Normal   Scheduled           pod/%s-%s  Successfully assigned ns/%s-%s to worker-111", obj, mockHash, obj, mockHash),
		fmt.Sprintf("5m          Normal   Pulled              pod/%s-%s  Container image already present on machine", obj, mockHash),
		fmt.Sprintf("5m          Normal   Created             pod/%s-%s  Created container", obj, mockHash),
		fmt.Sprintf("5m          Normal   Started             pod/%s-%s  Started container", obj, mockHash),
		"[MOCK] 以上为模拟事件数据",
	}
	return strings.Join(lines, "\n"), nil
}

func (MockKubernetes) CurrentImage(_ context.Context, _, _, container string) (string, error) {
	return mockRegistry + "/" + container + ":mock-current", nil
}

func (MockKubernetes) SetImage(_ context.Context, _, _, _, _ string) error { return nil }

func (MockKubernetes) RolloutStatus(_ context.Context, _, deployment string) (string, error) {
	return fmt.Sprintf("deployment %q successfully rolled out", deployment), nil
}

// mockResourceItem builds one deterministic unstructured object for a def.
func mockResourceItem(def kube.ResourceDef, namespace, name string) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": def.APIVersion(),
		"kind":       def.Kind,
		"metadata": map[string]any{
			"name":              name,
			"uid":               "mock-" + name,
			"resourceVersion":   "1",
			"creationTimestamp": "2026-07-01T00:00:00Z",
		},
		"spec":   map[string]any{},
		"status": map[string]any{},
	}
	if def.Namespaced {
		obj["metadata"].(map[string]any)["namespace"] = namespace
	}
	// ConfigMap/Secret items carry a small data map so the data-edit
	// endpoint can exercise real merge semantics (set/delete/keep)
	// instead of editing an empty object.
	switch def.Resource {
	case "deployments":
		obj["spec"] = map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "registry.example.com/demo/app:v1"}}}}}
	case "configmaps":
		obj["data"] = map[string]any{"keep": "v1", "drop": "gone"}
	case "secrets":
		obj["type"] = "Opaque"
		obj["data"] = map[string]any{
			"keep": base64.StdEncoding.EncodeToString([]byte("v1")),
			"drop": base64.StdEncoding.EncodeToString([]byte("gone")),
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

// Mock resource reads return deterministic objects so the resource API can
// be exercised without a cluster. name "missing" yields a Kubernetes
// NotFound error to keep the error contract testable.
func (MockKubernetes) ListResources(_ context.Context, def kube.ResourceDef, namespace string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	ns := namespace
	if ns == "" {
		ns = "default"
	}
	items := []unstructured.Unstructured{}
	if def.Resource != "resourcequotas" {
		items = []unstructured.Unstructured{
			*mockResourceItem(def, ns, "mock-"+def.Resource+"-1"),
			*mockResourceItem(def, ns, "mock-"+def.Resource+"-2"),
		}
	}
	list := &unstructured.UnstructuredList{Items: items}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: def.Group, Version: def.Version, Kind: def.Kind + "List"})
	return list, nil
}

func (MockKubernetes) GetResource(_ context.Context, def kube.ResourceDef, namespace, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if name == "" || name == "missing" {
		return nil, apierrors.NewNotFound(def.GVR().GroupResource(), name)
	}
	ns := namespace
	if ns == "" {
		ns = "default"
	}
	return mockResourceItem(def, ns, name), nil
}

func (MockKubernetes) ServerVersion(context.Context) (string, error) {
	return "v1.29.0-mock", nil
}

// ListMetrics returns deterministic metrics.k8s.io-shaped items so the
// overview shows a populated metrics section under the mock runtime.
func (MockKubernetes) ListMetrics(_ context.Context, resource, namespace string) (*unstructured.UnstructuredList, error) {
	gvr, err := kube.MetricsResource(resource)
	if err != nil {
		return nil, err
	}
	ns := namespace
	if ns == "" {
		ns = "default"
	}
	usage := func(cpu, mem string) map[string]any {
		return map[string]any{"cpu": cpu, "memory": mem}
	}
	items := make([]unstructured.Unstructured, 0, 2)
	for i, name := range []string{"mock-a", "mock-b"} {
		obj := map[string]any{
			"apiVersion": "metrics.k8s.io/v1beta1",
			"timestamp":  "2026-07-01T00:00:00Z",
			"metadata":   map[string]any{"name": name},
		}
		if resource == "nodes" {
			obj["kind"] = "NodeMetrics"
			obj["usage"] = usage(fmt.Sprintf("%dm", 100+i*50), fmt.Sprintf("%dMi", 512+i*256))
		} else {
			obj["kind"] = "PodMetrics"
			obj["metadata"].(map[string]any)["namespace"] = ns
			obj["containers"] = []any{
				map[string]any{"name": "app", "usage": usage(fmt.Sprintf("%dm", 30+i*10), fmt.Sprintf("%dMi", 128+i*64))},
				map[string]any{"name": "sidecar", "usage": usage("5m", "32Mi")},
			}
		}
		items = append(items, unstructured.Unstructured{Object: obj})
	}
	list := &unstructured.UnstructuredList{Items: items}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: "List"})
	return list, nil
}

// mergeMaps applies merge-patch semantics: maps merge recursively,
// scalars and arrays replace wholesale.
func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeMaps(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// PatchResource merges the patch into a fresh mock object and returns
// it. resourceVersion "stale" in the patch simulates a lost optimistic
// race → Kubernetes Conflict, keeping the 409 contract testable.
func (MockKubernetes) PatchResource(_ context.Context, def kube.ResourceDef, namespace, name string, _ types.PatchType, data []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	if name == "" || name == "missing" {
		return nil, apierrors.NewNotFound(def.GVR().GroupResource(), name)
	}
	ns := namespace
	if ns == "" {
		ns = "default"
	}
	obj := mockResourceItem(def, ns, name)
	var patch map[string]any
	if err := json.Unmarshal(data, &patch); err == nil {
		if rv, _, _ := unstructured.NestedString(patch, "metadata", "resourceVersion"); rv == "stale" {
			return nil, apierrors.NewConflict(def.GVR().GroupResource(), name, errors.New("the object has been modified"))
		}
		mergeMaps(obj.Object, patch)
	}
	return obj, nil
}

// DeleteResource removes the mock object; name "missing" → NotFound and
// a "stale" resourceVersion precondition → Conflict.
func (MockKubernetes) DeleteResource(_ context.Context, def kube.ResourceDef, namespace, name string, opts metav1.DeleteOptions) error {
	if name == "" || name == "missing" {
		return apierrors.NewNotFound(def.GVR().GroupResource(), name)
	}
	if opts.Preconditions != nil && opts.Preconditions.ResourceVersion != nil && *opts.Preconditions.ResourceVersion == "stale" {
		return apierrors.NewConflict(def.GVR().GroupResource(), name, errors.New("the object has been modified"))
	}
	return nil
}

// UpdateResource mirrors the mock patch contract: name "missing" →
// NotFound, resourceVersion "stale" → Conflict, otherwise the submitted
// object is returned with resourceVersion bumped like a real update.
func (MockKubernetes) UpdateResource(_ context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	if obj.GetName() == "" || obj.GetName() == "missing" {
		return nil, apierrors.NewNotFound(def.GVR().GroupResource(), obj.GetName())
	}
	if obj.GetResourceVersion() == "stale" {
		return nil, apierrors.NewConflict(def.GVR().GroupResource(), obj.GetName(), errors.New("the object has been modified"))
	}
	out := obj.DeepCopy()
	// A dry run validates but never persists — no resourceVersion bump,
	// matching the apiserver contract.
	if len(opts.DryRun) == 0 {
		out.SetResourceVersion("2")
	}
	if def.Namespaced && out.GetNamespace() == "" {
		out.SetNamespace(namespace)
	}
	return out, nil
}

// CreateResource mirrors a real create: name "exists" → AlreadyExists,
// name "missing" → NotFound; the returned object gets the synthetic
// server-owned fields a real apiserver would stamp, except on dry run.
func (MockKubernetes) CreateResource(_ context.Context, def kube.ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	if obj.GetName() == "" || obj.GetName() == "missing" {
		return nil, apierrors.NewNotFound(def.GVR().GroupResource(), obj.GetName())
	}
	if obj.GetName() == "exists" {
		return nil, apierrors.NewAlreadyExists(def.GVR().GroupResource(), obj.GetName())
	}
	out := obj.DeepCopy()
	// A dry run validates but never persists — no server-stamped fields,
	// matching the apiserver contract.
	if len(opts.DryRun) == 0 {
		out.SetResourceVersion("1")
		out.SetUID("mock-uid")
		out.SetCreationTimestamp(metav1.NewTime(time.Unix(1700000000, 0).UTC()))
	}
	if def.Namespaced && out.GetNamespace() == "" {
		out.SetNamespace(namespace)
	}
	return out, nil
}

// PodLogStream returns a deterministic mock log stream. Follow mode
// emits five timed lines so dev/mock mode exercises the same read loop
// as a real follow; without it the backlog closes immediately.
func (MockKubernetes) PodLogStream(ctx context.Context, _, pod string, opts kube.LogOptions) (io.ReadCloser, error) {
	if pod == "missing" {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod)
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		fmt.Fprintf(pw, "mock log backlog for pod %s (tail=%d)\n", pod, tailOr(opts.Tail, 200))
		if !opts.Follow {
			return
		}
		for i := 1; i <= 5; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
				fmt.Fprintf(pw, "mock follow line %d\n", i)
			}
		}
	}()
	return pr, nil
}

func tailOr(t *int64, d int64) int64 {
	if t == nil {
		return d
	}
	return *t
}

// ExecPod is a minimal fake shell for dev/mock mode: a banner, line
// echo, and `exit` to end the session — enough to exercise the full
// WebSocket pipeline without a cluster.
func (MockKubernetes) ExecPod(ctx context.Context, _, pod string, spec kube.ExecSpec, io_ kube.ExecIO) error {
	if pod == "missing" {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod)
	}
	out := io_.Stdout
	fmt.Fprintf(out, "mock terminal: pod %s container %q cmd %v (type 'exit' to quit)\n", pod, spec.Container, spec.Command)
	if io_.Stdin == nil {
		return nil
	}
	fmt.Fprint(out, "mock$ ")
	buf := make([]byte, 256)
	var line []byte
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := io_.Stdin.Read(buf)
		if n > 0 {
			line = append(line, buf[:n]...)
			for {
				idx := strings.IndexByte(string(line), '\r')
				if idx < 0 {
					break
				}
				cmd := strings.TrimSpace(string(line[:idx]))
				line = line[idx+1:]
				out.Write([]byte("\r\n"))
				if cmd == "exit" {
					fmt.Fprintln(out, "mock terminal closed")
					return nil
				}
				if cmd != "" {
					fmt.Fprintf(out, "mock echo: %s\r\n", cmd)
				}
				fmt.Fprint(out, "mock$ ")
			}
		}
		if err != nil {
			return nil
		}
	}
}

// AccessRules returns a permissive rule set in mock mode — tests that
// need a restricted identity stub the runtime or use the ssh path.
func (MockKubernetes) AccessRules(context.Context, string) ([]kube.ResourceRule, error) {
	return []kube.ResourceRule{
		{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
	}, nil
}
