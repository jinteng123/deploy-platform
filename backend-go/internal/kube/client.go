package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// Client implements the KubernetesRuntime interface via client-go. Beyond
// the typed clientset it carries a dynamic client and a lazily cached
// discovery RESTMapper that back the generic allowlisted resource layer,
// plus a timeout-free rest.Config for long-lived log/exec streams.
type Client struct {
	cfg          *rest.Config // request-timeout config, kept for ForUser clones
	client       kubernetes.Interface
	dyn          dynamic.Interface
	mapper       meta.RESTMapper
	metadataHTTP *http.Client
	streamCfg    *rest.Config
}

func New(kubeconfig string) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, err
	}
	return newClientForConfig(cfg)
}

// NewWithClientset wraps an existing clientset (used by tests with
// fake.NewSimpleClientset). dyn and mapper may be nil for tests that only
// exercise the typed API.
func NewWithClientset(client kubernetes.Interface, dyn dynamic.Interface, mapper meta.RESTMapper) *Client {
	return &Client{client: client, dyn: dyn, mapper: mapper}
}

// requestTimeout bounds each API request so a stalled apiserver cannot
// hold the worker goroutine forever — ServerVersion's ctx wrapper returns
// early, but the underlying request needs its own bound.
const requestTimeout = 15 * time.Second

// newClientForConfig wires the typed, dynamic and discovery clients from
// one rest.Config. The RESTMapper is a DeferredDiscoveryRESTMapper over a
// memory cache: discovery documents are fetched lazily per client
// instance and never persist credentials.
func newClientForConfig(cfg *rest.Config) (*Client, error) {
	// Long-lived streams (log follow, exec) must not inherit requestTimeout —
	// it would kill every session at 15s. Keep an untimed copy for them.
	streamCfg := *cfg
	streamCfg.Timeout = 0
	cfg.Timeout = requestTimeout
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	metadataHTTP, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	metadataHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.Discovery()))
	return &Client{cfg: cfg, client: c, dyn: dyn, mapper: mapper, metadataHTTP: metadataHTTP, streamCfg: &streamCfg}, nil
}

// Identity names a Kubernetes user for impersonation — the platform's own
// RBAC boundary. Empty Username means "use the credential's own identity".
type Identity struct {
	Username string
	Groups   []string
}

// ForUser returns a client that issues every request as the impersonated
// identity. The apiserver evaluates RBAC for that user — a caller with no
// impersonate verb gets a clean Forbidden from the apiserver itself.
// Timeouts are preserved from the base client.
func (c *Client) ForUser(id Identity) (*Client, error) {
	if c.cfg == nil || c.streamCfg == nil {
		return nil, fmt.Errorf("impersonation 需要完整 rest.Config（测试 client 不支持）")
	}
	if id.Username == "" {
		return c, nil
	}
	imp := func(in *rest.Config) *rest.Config {
		cp := rest.CopyConfig(in)
		cp.Impersonate = rest.ImpersonationConfig{UserName: id.Username, Groups: id.Groups}
		return cp
	}
	cfg := imp(c.cfg)
	cl, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	metadataHTTP, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	metadataHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cl.Discovery()))
	return &Client{cfg: cfg, client: cl, dyn: dyn, mapper: mapper, metadataHTTP: metadataHTTP, streamCfg: imp(c.streamCfg)}, nil
}

// NewFromKubeconfigData builds a client from raw kubeconfig bytes.
func NewFromKubeconfigData(data []byte) (*Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, err
	}
	return newClientForConfig(cfg)
}

// NewFromToken builds a client from an API server URL and a bearer token.
// caData may be nil to use the system roots; when insecure is true the CA
// data must be cleared or rest.Config validation rejects the combination.
func NewFromToken(apiServer, token string, caData []byte, insecure bool) (*Client, error) {
	if apiServer == "" || token == "" {
		return nil, fmt.Errorf("api_server 与 token 均为必填")
	}
	cfg := &rest.Config{
		Host:        apiServer,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: insecure,
		},
	}
	if !insecure {
		cfg.TLSClientConfig.CAData = caData
	}
	return newClientForConfig(cfg)
}

// ServerVersion returns the cluster's git version via discovery. The
// discovery client does not take a context; callers bound the wait with
// their own timeout around this call.
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	type result struct {
		v   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := c.client.Discovery().ServerVersion()
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{v: v.GitVersion}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.v, r.err
	}
}

func (c *Client) Pods(ctx context.Context, namespace string) ([]map[string]any, error) {
	list, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		m, err := toMap(&list.Items[i])
		if err != nil {
			return nil, err
		}
		result = append(result, PodSummary(m))
	}
	return result, nil
}

func (c *Client) PodLogs(ctx context.Context, namespace, pod string, tail int64) (string, error) {
	stream, err := c.client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{TailLines: &tail}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Client) RestartPod(ctx context.Context, namespace, pod string) error {
	return c.client.CoreV1().Pods(namespace).Delete(ctx, pod, metav1.DeleteOptions{})
}

func (c *Client) DeploymentInfo(ctx context.Context, namespace, name string) (map[string]any, error) {
	dep, err := c.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	m, err := toMap(dep)
	if err != nil {
		return nil, err
	}
	return DeploymentInfoFromMap(m), nil
}

func (c *Client) DeploymentEvents(ctx context.Context, namespace, name string) (string, error) {
	list, err := c.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name,
	})
	if err != nil {
		return "", err
	}
	sort.SliceStable(list.Items, func(i, j int) bool {
		return eventTime(&list.Items[i]).Before(eventTime(&list.Items[j]))
	})
	var b strings.Builder
	b.WriteString("TIME\tTYPE\tREASON\tMESSAGE\n")
	for i := range list.Items {
		e := &list.Items[i]
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", eventTime(e).Format(time.RFC3339), e.Type, e.Reason, strings.TrimSpace(e.Message))
	}
	return b.String(), nil
}

func (c *Client) CurrentImage(ctx context.Context, namespace, deployment, container string) (string, error) {
	dep, err := c.client.AppsV1().Deployments(namespace).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, ctn := range dep.Spec.Template.Spec.Containers {
		if container == "" || ctn.Name == container {
			return ctn.Image, nil
		}
	}
	return "", fmt.Errorf("容器 %s 不存在", container)
}

func (c *Client) SetImage(ctx context.Context, namespace, deployment, container, image string) error {
	dep, err := c.client.AppsV1().Deployments(namespace).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return err
	}
	found := false
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == container {
			dep.Spec.Template.Spec.Containers[i].Image = image
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("容器 %s 不存在", container)
	}
	_, err = c.client.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func eventTime(e *corev1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.FirstTimestamp.Time
}

func toMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveGVR validates an allowlisted ResourceDef against the target
// cluster. With a live RESTMapper the mapping must exist and agree with
// the registry on resource and scope — a mismatch means the registry
// entry is wrong, which fails loudly rather than silently serving the
// wrong object. A nil mapper (tests that skip discovery) trusts the def.
func (c *Client) resolveGVR(def ResourceDef) (schema.GroupVersionResource, bool, error) {
	if c.dyn == nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("资源客户端未配置")
	}
	if c.mapper == nil {
		return def.GVR(), def.Namespaced, nil
	}
	mapping, err := c.mapper.RESTMapping(def.GVK().GroupKind(), def.Version)
	if meta.IsNoMatchError(err) {
		return schema.GroupVersionResource{}, false, fmt.Errorf("%w: %s", ErrResourceUnsupported, def.GVR())
	}
	if err != nil {
		return schema.GroupVersionResource{}, false, err
	}
	if mapping.Resource != def.GVR() {
		return schema.GroupVersionResource{}, false, fmt.Errorf("%w: 注册表 %s 与集群映射 %s 不一致",
			ErrResourceNotAllowed, def.GVR(), mapping.Resource)
	}
	return mapping.Resource, mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// resourceInterface scopes the dynamic client: namespaced resources honor
// the caller's namespace (empty = all namespaces); cluster-scoped
// resources ignore it entirely.
func (c *Client) resourceInterface(gvr schema.GroupVersionResource, namespaced bool, namespace string) dynamic.ResourceInterface {
	ri := c.dyn.Resource(gvr)
	if namespaced && namespace != "" {
		return ri.Namespace(namespace)
	}
	return ri
}

// ListResources lists an allowlisted resource as unstructured objects.
// Each item is guaranteed to carry the registry's GVK.
func (c *Client) ListResources(ctx context.Context, def ResourceDef, namespace string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return nil, err
	}
	list, err := c.resourceInterface(gvr, namespaced, namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	gvk := def.GVK()
	for i := range list.Items {
		if list.Items[i].GroupVersionKind().Empty() {
			list.Items[i].SetGroupVersionKind(gvk)
		}
	}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: def.Group, Version: def.Version, Kind: def.Kind + "List"})
	return list, nil
}

// GetResource fetches one named object. Kubernetes API errors (404, 403)
// propagate unchanged so handlers can translate them faithfully.
func (c *Client) GetResource(ctx context.Context, def ResourceDef, namespace, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, fmt.Errorf("资源名称不能为空")
	}
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return nil, err
	}
	obj, err := c.resourceInterface(gvr, namespaced, namespace).Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}
	obj.SetGroupVersionKind(def.GVK())
	return obj, nil
}

// GetObjectMetadata reads only PartialObjectMetadata for a named resource.
// Its Accept header deliberately has no full-object fallback: older API
// servers that cannot serve metadata-only responses fail closed instead of
// returning ConfigMap or Secret contents to the platform.
func (c *Client) GetObjectMetadata(ctx context.Context, def ResourceDef, namespace, name string) (*metav1.PartialObjectMetadata, error) {
	if c == nil || c.cfg == nil || c.metadataHTTP == nil {
		return nil, fmt.Errorf("metadata-only Kubernetes client is unavailable")
	}
	if name == "" {
		return nil, fmt.Errorf("resource name is required")
	}
	if def.Version == "" || def.Resource == "" || strings.ContainsAny(def.Version+def.Resource+def.Group, "/?#") {
		return nil, fmt.Errorf("resource definition is invalid")
	}
	namespaced := def.Namespaced
	if namespaced && namespace == "" {
		return nil, fmt.Errorf("namespace is required for %s", def.Resource)
	}
	base, err := url.Parse(c.cfg.Host)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("Kubernetes API server URL is invalid")
	}
	apiPath := "/apis/" + def.Group + "/" + def.Version
	if def.Group == "" {
		apiPath = "/api/" + def.Version
	}
	if namespaced {
		apiPath += "/namespaces/" + url.PathEscape(namespace)
	}
	apiPath += "/" + url.PathEscape(def.Resource) + "/" + url.PathEscape(name)
	base.Path = strings.TrimRight(base.Path, "/") + apiPath
	base.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1")
	response, err := c.metadataHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("metadata-only Kubernetes GET returned HTTP %d", response.StatusCode)
	}
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	metadataMediaType := params["as"] != "" || params["g"] != "" || params["v"] != ""
	if err != nil || mediaType != "application/json" || (metadataMediaType && (params["as"] != "PartialObjectMetadata" || params["g"] != "meta.k8s.io" || params["v"] != "v1")) {
		return nil, fmt.Errorf("Kubernetes API server did not honor metadata-only response negotiation")
	}
	var object metav1.PartialObjectMetadata
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("decode metadata-only Kubernetes response: %w", err)
	}
	if object.Kind != "PartialObjectMetadata" || object.APIVersion != "meta.k8s.io/v1" || object.Name != name || (namespaced && object.Namespace != namespace) || object.UID == "" || object.ResourceVersion == "" {
		return nil, fmt.Errorf("metadata-only Kubernetes response identity or version is incomplete")
	}
	return &object, nil
}

// ListMetrics lists metrics.k8s.io/v1beta1 objects ("nodes" or "pods";
// namespace only applies to pods, empty = all namespaces). The API is
// served by metrics-server, so its absence surfaces as a regular API
// error which callers translate into "metrics unavailable" — the GVR is
// fixed server-side via MetricsResource, never client-supplied.
func (c *Client) ListMetrics(ctx context.Context, resource, namespace string) (*unstructured.UnstructuredList, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("资源客户端未配置")
	}
	gvr, err := MetricsResource(resource)
	if err != nil {
		return nil, err
	}
	nri := c.dyn.Resource(gvr)
	var ri dynamic.ResourceInterface = nri
	if resource == "pods" && namespace != "" {
		ri = nri.Namespace(namespace)
	}
	return ri.List(ctx, metav1.ListOptions{})
}

// PatchResource applies a caller-built patch to an allowlisted resource
// and returns the server-updated object. Patch type and payload are
// chosen by the service layer; conflicts (409) and RBAC denials (403)
// propagate unchanged for faithful HTTP translation.
func (c *Client) PatchResource(ctx context.Context, def ResourceDef, namespace, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, fmt.Errorf("资源名称不能为空")
	}
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return nil, err
	}
	obj, err := c.resourceInterface(gvr, namespaced, namespace).Patch(ctx, name, pt, data, opts)
	if err != nil {
		return nil, err
	}
	obj.SetGroupVersionKind(def.GVK())
	return obj, nil
}

// UpdateResource replaces an allowlisted object wholesale (PUT
// semantics). The caller builds the object — the apiserver enforces the
// embedded resourceVersion atomically, answering 409 on a stale read.
// Conflicts/RBAC errors propagate unchanged for faithful HTTP mapping.
func (c *Client) UpdateResource(ctx context.Context, def ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	if obj.GetName() == "" {
		return nil, fmt.Errorf("资源名称不能为空")
	}
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return nil, err
	}
	out, err := c.resourceInterface(gvr, namespaced, namespace).Update(ctx, obj, opts)
	if err != nil {
		return nil, err
	}
	out.SetGroupVersionKind(def.GVK())
	return out, nil
}

// CreateResource creates one allowlisted object (POST semantics).
// AlreadyExists/RBAC errors propagate unchanged for faithful HTTP
// mapping; dry run rides on opts and never persists.
func (c *Client) CreateResource(ctx context.Context, def ResourceDef, namespace string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	if obj.GetName() == "" {
		return nil, fmt.Errorf("资源名称不能为空")
	}
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return nil, err
	}
	out, err := c.resourceInterface(gvr, namespaced, namespace).Create(ctx, obj, opts)
	if err != nil {
		return nil, err
	}
	out.SetGroupVersionKind(def.GVK())
	return out, nil
}

// DeleteResource deletes one allowlisted object. opts.Preconditions can
// pin the expected resourceVersion so a stale read can't delete a newer
// object — the API server answers 409 in that case.
func (c *Client) DeleteResource(ctx context.Context, def ResourceDef, namespace, name string, opts metav1.DeleteOptions) error {
	if name == "" {
		return fmt.Errorf("资源名称不能为空")
	}
	gvr, namespaced, err := c.resolveGVR(def)
	if err != nil {
		return err
	}
	return c.resourceInterface(gvr, namespaced, namespace).Delete(ctx, name, opts)
}
