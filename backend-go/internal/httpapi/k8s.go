package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"deploy-platform/backend-go/internal/kube"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// resolveCluster resolves cluster_id (required) and the per-cluster runtime
// shared by all K8s handlers. A disabled cluster is a platform-level
// rejection → 403, not a backend failure.
func resolveCluster(c *gin.Context, store *service.Store, k8s *service.K8sService) (service.KubernetesRuntime, service.Cluster, bool) {
	raw := c.Query("cluster_id")
	if raw == "" {
		fail(c, http.StatusBadRequest, "cluster_id 为必填参数")
		return nil, service.Cluster{}, false
	}
	clusterID, err := strconv.Atoi(raw)
	if err != nil || clusterID <= 0 {
		fail(c, http.StatusBadRequest, "非法参数 cluster_id")
		return nil, service.Cluster{}, false
	}
	cluster, err := store.FindCluster(clusterID)
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return nil, service.Cluster{}, false
	}
	rt, err := k8s.ForUser(cluster, currentUser(c))
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, service.ErrClusterDisabled) {
			code = http.StatusForbidden
		}
		fail(c, code, err.Error())
		return nil, cluster, false
	}
	return rt, cluster, true
}

// k8sContext resolves cluster_id (required) and namespace for K8s handlers.
func k8sContext(c *gin.Context, store *service.Store, k8s *service.K8sService) (service.KubernetesRuntime, string, bool) {
	rt, cluster, ok := resolveCluster(c, store, k8s)
	if !ok {
		return nil, "", false
	}
	ns := c.Query("namespace")
	if !nonEmpty(ns) {
		ns = cluster.DefaultNamespace
	}
	return rt, ns, true
}

func registerK8sAPI(api *gin.RouterGroup, store *service.Store, k8s *service.K8sService) {
	api.GET("/k8s/pods", func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		pods, err := rt.Pods(c.Request.Context(), ns)
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		if pods == nil {
			pods = []map[string]any{}
		}
		c.JSON(http.StatusOK, pods)
	})

	api.GET("/k8s/pods/:pod/logs", func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		tail, ok := queryIntRange(c, "tail", 200, 10, 5000)
		if !ok {
			return
		}
		pod := c.Param("pod")
		logs, err := rt.PodLogs(c.Request.Context(), ns, pod, int64(tail))
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"pod": pod, "logs": logs})
	})

	api.POST("/k8s/pods/:pod/restart", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		pod := c.Param("pod")
		if err := rt.RestartPod(c.Request.Context(), ns, pod); err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"pod": pod, "message": fmt.Sprintf("pod \"%s\" deleted", pod)})
	})

	api.GET("/k8s/deployments/:name", func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		info, err := rt.DeploymentInfo(c.Request.Context(), ns, c.Param("name"))
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, info)
	})

	api.GET("/k8s/deployments/:name/events", func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		name := c.Param("name")
		events, err := rt.DeploymentEvents(c.Request.Context(), ns, name)
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"deployment": name, "events": events})
	})

	// GET /k8s/overview composes the cluster dashboard: server version,
	// node health/capacity, namespace + workload counts, recent Warning
	// events and metrics.k8s.io usage. A missing metrics-server only
	// flips metrics.available=false — never fails the request.
	// Capabilities answers "what may THIS user do here" — one
	// SelfSubjectRulesReview (or kubectl can-i --list) mapped onto the
	// server-side allowlist. The frontend hides what the apiserver would
	// deny; enforcement itself stays impersonation-side.
	api.GET("/k8s/capabilities", func(c *gin.Context) {
		rt, ns, ok := k8sContext(c, store, k8s)
		if !ok {
			return
		}
		caps, err := service.AccessCapabilities(c.Request.Context(), rt, ns)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, caps)
	})

	api.GET("/k8s/overview", func(c *gin.Context) {
		rt, _, ok := resolveCluster(c, store, k8s)
		if !ok {
			return
		}
		ov, err := service.BuildClusterOverview(c.Request.Context(), rt)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, ov)
	})

	// GET /k8s/resource-types exposes the server-side GVR allowlist so the
	// console can render resource menus without duplicating the registry.
	api.GET("/k8s/resource-types", func(c *gin.Context) {
		defs := kube.AllowedResources()
		out := make([]gin.H, 0, len(defs))
		for _, d := range defs {
			out = append(out, gin.H{
				"group":         d.Group,
				"version":       d.Version,
				"resource":      d.Resource,
				"kind":          d.Kind,
				"namespaced":    d.Namespaced,
				"scalable":      d.Scalable,
				"imageable":     d.Imageable,
				"restartable":   d.Restartable,
				"deletable":     d.Deletable,
				"editable":      d.Editable,
				"creatable":     d.Creatable,
				"data_editable": d.DataEditable,
			})
		}
		c.JSON(http.StatusOK, out)
	})

	// GET /k8s/resources lists one allowlisted resource kind. namespace
	// defaults to the cluster's default namespace; all_namespaces=true lists
	// across every namespace (namespaced resources only). Cluster-scoped
	// resources ignore namespace entirely.
	api.GET("/k8s/resources", func(c *gin.Context) {
		rt, cluster, ok := resolveCluster(c, store, k8s)
		if !ok {
			return
		}
		def, ok := resourceDef(c)
		if !ok {
			return
		}
		ns, ok := resourceNamespace(c, cluster, def, true)
		if !ok {
			return
		}
		list, err := rt.ListResources(c.Request.Context(), def, ns, metav1.ListOptions{
			LabelSelector: strings.TrimSpace(c.Query("label_selector")),
			FieldSelector: strings.TrimSpace(c.Query("field_selector")),
		})
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		if list.Items == nil {
			list.Items = []unstructured.Unstructured{}
		}
		// Secrets never leak values: data/stringData are masked on every
		// read path before the object leaves the API.
		if kube.IsSecretDef(def) {
			for i := range list.Items {
				kube.MaskSecretData(&list.Items[i])
			}
		}
		c.JSON(http.StatusOK, list)
	})

	// GET /k8s/resources/:name fetches a single object. Namespaced resources
	// always need a concrete namespace — all_namespaces is rejected here.
	api.GET("/k8s/resources/:name", func(c *gin.Context) {
		rt, cluster, ok := resolveCluster(c, store, k8s)
		if !ok {
			return
		}
		def, ok := resourceDef(c)
		if !ok {
			return
		}
		ns, ok := resourceNamespace(c, cluster, def, false)
		if !ok {
			return
		}
		obj, err := rt.GetResource(c.Request.Context(), def, ns, c.Param("name"), metav1.GetOptions{})
		// Explicit Secret reads are audited — masked output or not, "who
		// looked at which Secret" must be answerable. Detail stays empty:
		// nothing about the Secret's content belongs in the record.
		if kube.IsSecretDef(def) {
			auditK8s(c, store, "secret_view", def, ns, c.Param("name"), "", err)
		}
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		// Reveal is opt-in on an authenticated, audited detail read. List and
		// ordinary detail responses keep their masking contract.
		if kube.IsSecretDef(def) && c.Query("reveal_secret") != "true" {
			kube.MaskSecretData(obj)
		}
		if kube.IsSecretDef(def) {
			c.Header("Cache-Control", "no-store")
		}
		c.JSON(http.StatusOK, obj)
	})

	// ---- Controlled write operations (step 6) ----
	// Every write resolves the GVR through the allowlist exactly like the
	// reads, then additionally checks the per-resource capability flag.
	// Patches embed metadata.resourceVersion when the caller supplies it,
	// so a stale read answers 409 Conflict instead of silently clobbering.

	// POST /k8s/resources/:name/scale {replicas, resource_version?}
	api.POST("/k8s/resources/:name/scale", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		if c.Request.Method != http.MethodPut || c.Query("dry_run") != "true" {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, c.Param("name")); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, c.Param("name"), "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		if !def.Scalable {
			deny(c, store, "scale", def, ns, c.Param("name"), "", fmt.Sprintf("资源 %s 不支持调整副本数", def.Resource))
			return
		}
		var body struct {
			Replicas        *int32 `json:"replicas"`
			ResourceVersion string `json:"resource_version"`
		}
		if !bindJSON(c, &body) {
			return
		}
		if body.Replicas == nil || *body.Replicas < 0 || *body.Replicas > 50 {
			deny(c, store, "scale", def, ns, c.Param("name"), "", "replicas 为必填且须在 0-50 之间")
			return
		}
		patch, err := service.ScalePatch(*body.Replicas, body.ResourceVersion)
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		obj, err := rt.PatchResource(c.Request.Context(), def, ns, c.Param("name"), types.MergePatchType, patch, metav1.PatchOptions{})
		auditK8s(c, store, "scale", def, ns, c.Param("name"), fmt.Sprintf("replicas=%d", *body.Replicas), err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, obj)
	})

	// POST /k8s/resources/:name/image {container, image, resource_version?}
	api.POST("/k8s/resources/:name/image", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		if c.Request.Method != http.MethodPut || c.Query("dry_run") != "true" {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, c.Param("name")); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, c.Param("name"), "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		if !def.Imageable {
			deny(c, store, "image", def, ns, c.Param("name"), "", fmt.Sprintf("资源 %s 不支持更新镜像", def.Resource))
			return
		}
		var body struct {
			Container       string `json:"container"`
			Image           string `json:"image"`
			ResourceVersion string `json:"resource_version"`
		}
		if !bindJSON(c, &body) {
			return
		}
		body.Container = strings.TrimSpace(body.Container)
		body.Image = strings.TrimSpace(body.Image)
		if !nonEmpty(body.Container) || !nonEmpty(body.Image) {
			deny(c, store, "image", def, ns, c.Param("name"), "", "container 和 image 为必填参数")
			return
		}
		name := c.Param("name")
		// The container must exist in the pod template — otherwise a typo
		// would inject a phantom container via the name-keyed merge.
		obj, err := rt.GetResource(c.Request.Context(), def, ns, name, metav1.GetOptions{})
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		if !service.ContainerInPodTemplate(obj, body.Container) {
			deny(c, store, "image", def, ns, name, fmt.Sprintf("container=%s", body.Container), fmt.Sprintf("容器 %s 不存在于 %s/%s", body.Container, def.Resource, name))
			return
		}
		patch, err := service.ImagePatch(body.Container, body.Image, body.ResourceVersion)
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		obj, err = rt.PatchResource(c.Request.Context(), def, ns, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		auditK8s(c, store, "image", def, ns, name, fmt.Sprintf("%s=%s", body.Container, body.Image), err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, obj)
	})

	// POST /k8s/resources/:name/restart — rollout restart via the
	// restartedAt pod-template annotation (no body).
	api.POST("/k8s/resources/:name/restart", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		if c.Request.Method != http.MethodPut || c.Query("dry_run") != "true" {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, c.Param("name")); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, c.Param("name"), "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		if !def.Restartable {
			deny(c, store, "restart", def, ns, c.Param("name"), "", fmt.Sprintf("资源 %s 不支持滚动重启", def.Resource))
			return
		}
		var body struct {
			ResourceVersion string `json:"resource_version"`
		}
		// Body is optional — an empty body restarts unconditionally.
		_ = c.ShouldBindJSON(&body)
		patch, err := service.RestartPatch(strings.TrimSpace(body.ResourceVersion))
		if err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		obj, err := rt.PatchResource(c.Request.Context(), def, ns, c.Param("name"), types.MergePatchType, patch, metav1.PatchOptions{})
		auditK8s(c, store, "restart", def, ns, c.Param("name"), "", err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, obj)
	})

	// DELETE /k8s/resources/:name — separate delete allowlist
	// (def.Deletable). ?resource_version pins the expected version as a
	// delete precondition (409 on a stale read).
	api.DELETE("/k8s/resources/:name", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		if c.Request.Method != http.MethodPut || c.Query("dry_run") != "true" {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, c.Param("name")); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, c.Param("name"), "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		if !def.Deletable {
			deny(c, store, "delete", def, ns, c.Param("name"), "", fmt.Sprintf("资源 %s 不允许删除", def.Resource))
			return
		}
		opts := metav1.DeleteOptions{}
		if rv := strings.TrimSpace(c.Query("resource_version")); rv != "" {
			opts.Preconditions = &metav1.Preconditions{ResourceVersion: &rv}
		}
		name := c.Param("name")
		err := rt.DeleteResource(c.Request.Context(), def, ns, name, opts)
		auditK8s(c, store, "delete", def, ns, name, "", err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"detail": fmt.Sprintf("%s %q 已删除", def.Resource, name)})
	})

	// PUT /k8s/resources/:name replaces a whole object — the YAML
	// editor's save path. The manifest must match the allowlisted GVK,
	// the path name, and the resolved namespace, and carry a
	// resourceVersion (the apiserver requires it for update — that is
	// also the optimistic-concurrency precondition, stale → 409).
	// ?dry_run=true runs the same pipeline with UpdateOptions.DryRun —
	// apiserver-side validation without persistence (Kuboard's
	// "校验后 Apply" flow), audited as update_dryrun so validation
	// traffic stays distinguishable from real writes.
	api.PUT("/k8s/resources/:name", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		if c.Request.Method != http.MethodPut || c.Query("dry_run") != "true" {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, c.Param("name")); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, c.Param("name"), "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		dryRun := c.Query("dry_run") == "true"
		action := "update"
		if dryRun {
			action = "update_dryrun"
		}
		if !def.Editable {
			deny(c, store, action, def, ns, c.Param("name"), "", fmt.Sprintf("资源 %s 不允许编辑", def.Resource))
			return
		}
		var body struct {
			Manifest string `json:"manifest"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "请求体必须是 JSON: "+err.Error())
			return
		}
		obj, err := parseManifest(body.Manifest, def, ns, c.Param("name"))
		if err != nil {
			deny(c, store, action, def, ns, c.Param("name"), "invalid manifest", err.Error())
			return
		}
		opts := metav1.UpdateOptions{}
		if dryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		out, err := rt.UpdateResource(c.Request.Context(), def, ns, obj, opts)
		auditK8s(c, store, action, def, ns, c.Param("name"), "manifest", err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		if kube.IsSecretDef(def) {
			kube.MaskSecretData(out)
		}
		c.JSON(http.StatusOK, out)
	})

	// POST /k8s/resources creates one allowlisted object — the create
	// dialog's submit path. Unlike PUT there is no :name param; the name
	// comes from the manifest and must be a DNS-1123 subdomain, while
	// metadata.resourceVersion must be absent (a create can never carry
	// an optimistic-concurrency precondition). ?dry_run=true runs the
	// same pipeline with CreateOptions.DryRun, audited as
	// create_dryrun.
	api.POST("/k8s/resources", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		dryRun := c.Query("dry_run") == "true"
		action := "create"
		if dryRun {
			action = "create_dryrun"
		}
		if !def.Creatable {
			deny(c, store, action, def, ns, "", "", fmt.Sprintf("资源 %s 不允许创建", def.Resource))
			return
		}
		var body struct {
			Manifest string `json:"manifest"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "请求体必须是 JSON: "+err.Error())
			return
		}
		obj, err := parseCreateManifest(body.Manifest, def, ns)
		if err != nil {
			deny(c, store, action, def, ns, "", "invalid manifest", err.Error())
			return
		}
		if !dryRun {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, obj.GetName()); err != nil {
				writeCDErr(c, err)
				return
			}
		}
		opts := metav1.CreateOptions{}
		if dryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		out, err := rt.CreateResource(c.Request.Context(), def, ns, obj, opts)
		auditK8s(c, store, action, def, ns, obj.GetName(), "manifest", err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		if kube.IsSecretDef(def) {
			kube.MaskSecretData(out)
		}
		if dryRun {
			c.JSON(http.StatusOK, out)
			return
		}
		c.JSON(http.StatusCreated, out)
	})

	// POST /k8s/resources/:name/data — the structured data editor
	// (DataEditable kinds: configmaps, secrets). Unlike PUT this is a
	// server-side MERGE, not a replace: the client sends only the keys
	// it wants to set or remove. A Secret's real values are fetched
	// inside this handler; only explicitly changed keys are transmitted.
	// resource_version is mandatory: it is compared
	// against the live object so a stale read answers 409 on BOTH
	// runtimes (the merged object is then submitted with the live rv,
	// letting the apiserver enforce atomicity end-to-end).
	api.POST("/k8s/resources/:name/data", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rt, def, ns, ok := writeContext(c, store, k8s)
		if !ok {
			return
		}
		dryRun := c.Query("dry_run") == "true"
		action := "update"
		if dryRun {
			action = "update_dryrun"
		}
		name := c.Param("name")
		if !dryRun {
			clusterID, _ := strconv.Atoi(c.Query("cluster_id"))
			if err := store.CheckManagedWrite(clusterID, def.Resource, ns, name); err != nil {
				audit(c, store, "managed_write_denied", clusterID, ns, def.Resource, name, "", service.AuditDenied)
				writeCDErr(c, err)
				return
			}
		}
		if !def.DataEditable {
			deny(c, store, action, def, ns, name, "", fmt.Sprintf("资源 %s 不支持数据编辑", def.Resource))
			return
		}
		var body struct {
			ResourceVersion string            `json:"resource_version"`
			Type            string            `json:"type"`
			Set             map[string]string `json:"set"`
			DeleteKeys      []string          `json:"delete_keys"`
			Immutable       *bool             `json:"immutable"`
			Metadata        *struct {
				Labels      *map[string]string `json:"labels"`
				Annotations *map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "请求体必须是 JSON: "+err.Error())
			return
		}
		detail := dataEditDetail(body.Set, body.DeleteKeys, body.Type)
		if strings.TrimSpace(body.ResourceVersion) == "" {
			deny(c, store, action, def, ns, name, detail, "resource_version 缺失：请基于最新对象编辑后提交")
			return
		}
		if len(body.Set) == 0 && len(body.DeleteKeys) == 0 && strings.TrimSpace(body.Type) == "" && body.Immutable == nil && body.Metadata == nil {
			deny(c, store, action, def, ns, name, "", "无变更")
			return
		}
		for k := range body.Set {
			if errs := validation.IsConfigMapKey(k); len(errs) > 0 {
				deny(c, store, action, def, ns, name, detail, fmt.Sprintf("非法数据键 %q: %s", k, strings.Join(errs, "; ")))
				return
			}
		}
		for _, k := range body.DeleteKeys {
			if errs := validation.IsConfigMapKey(k); len(errs) > 0 {
				deny(c, store, action, def, ns, name, detail, fmt.Sprintf("非法数据键 %q: %s", k, strings.Join(errs, "; ")))
				return
			}
		}
		if strings.TrimSpace(body.Type) != "" && !kube.IsSecretDef(def) {
			deny(c, store, action, def, ns, name, detail, "只有 secrets 支持修改 type")
			return
		}
		obj, err := rt.GetResource(c.Request.Context(), def, ns, name, metav1.GetOptions{})
		if err != nil {
			auditK8s(c, store, action, def, ns, name, detail, err)
			writeResourceErr(c, err)
			return
		}
		if obj.GetResourceVersion() != body.ResourceVersion {
			err := apierrors.NewConflict(def.GVR().GroupResource(), name, fmt.Errorf("resourceVersion %q 与当前 %q 不一致", body.ResourceVersion, obj.GetResourceVersion()))
			auditK8s(c, store, action, def, ns, name, detail, err)
			writeResourceErr(c, err)
			return
		}
		if immutable, _, _ := unstructured.NestedBool(obj.Object, "immutable"); immutable &&
			(len(body.Set) > 0 || len(body.DeleteKeys) > 0) {
			deny(c, store, action, def, ns, name, detail, "该资源已标记 immutable，数据不可修改")
			return
		}
		if body.Immutable != nil {
			if immutable, _, _ := unstructured.NestedBool(obj.Object, "immutable"); immutable && !*body.Immutable {
				deny(c, store, action, def, ns, name, detail, "不可取消 immutable，请删除后重建")
				return
			}
			obj.Object["immutable"] = *body.Immutable
		}
		if body.Metadata != nil {
			if body.Metadata.Labels != nil {
				obj.SetLabels(*body.Metadata.Labels)
			}
			if body.Metadata.Annotations != nil {
				obj.SetAnnotations(*body.Metadata.Annotations)
			}
		}
		mergeResourceData(obj, def, body.Set, body.DeleteKeys, strings.TrimSpace(body.Type))
		opts := metav1.UpdateOptions{}
		if dryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		out, err := rt.UpdateResource(c.Request.Context(), def, ns, obj, opts)
		auditK8s(c, store, action, def, ns, name, detail, err)
		if err != nil {
			writeResourceErr(c, err)
			return
		}
		if kube.IsSecretDef(def) {
			kube.MaskSecretData(out)
		}
		c.JSON(http.StatusOK, out)
	})
}

// auditK8s records one K8s resource operation — the sanitized tuple
// (who, what, where, outcome). Manifests, Secret data and credentials
// never reach this path; detail is bounded operator-supplied metadata.
func auditK8s(c *gin.Context, store *service.Store, action string, def kube.ResourceDef, ns, name, detail string, err error) {
	auditK8sResult(c, store, action, def, ns, name, detail, auditErr(err))
}

func auditK8sResult(c *gin.Context, store *service.Store, action string, def kube.ResourceDef, ns, name, detail, result string) {
	cid, _ := strconv.Atoi(c.Query("cluster_id"))
	gvr := def.Group + "/" + def.Version + "/" + def.Resource
	audit(c, store, action, cid, ns, gvr, name, detail, result)
}

// deny audits a platform-level write rejection (unsupported verb, invalid
// target) before answering 400 — every write ATTEMPT leaves a record, not
// only operations that reached the apiserver.
func deny(c *gin.Context, store *service.Store, action string, def kube.ResourceDef, ns, name, detail, msg string) {
	auditK8sResult(c, store, action, def, ns, name, detail, service.AuditDenied)
	fail(c, http.StatusBadRequest, msg)
}

// parseManifestObject holds the manifest normalization shared by update
// and create: YAML/JSON text → JSON-normalized Unstructured, GVK must
// equal the allowlisted def (the client can never redirect the manifest
// at a different kind), metadata.name must be non-empty, namespace must
// match the resolved one, and server-owned metadata fields are stripped.
func parseManifestObject(manifest string, def kube.ResourceDef, namespace string) (*unstructured.Unstructured, error) {
	if strings.TrimSpace(manifest) == "" {
		return nil, fmt.Errorf("manifest 不能为空")
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &m); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("manifest 不能为空")
	}
	// YAML produces non-JSON scalar types (uint64, time.Time...) that
	// unstructured can't deep-copy — a JSON round-trip normalizes every
	// value to float64/string/bool/map/slice.
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("manifest 序列化失败: %w", err)
	}
	m = nil
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest 解析失败: %w", err)
	}
	obj := &unstructured.Unstructured{Object: m}
	if obj.GetKind() != def.Kind || obj.GetAPIVersion() != def.APIVersion() {
		return nil, fmt.Errorf("manifest 类型 %s/%s 与目标资源 %s/%s 不符",
			obj.GetAPIVersion(), obj.GetKind(), def.APIVersion(), def.Kind)
	}
	if obj.GetName() == "" {
		return nil, fmt.Errorf("metadata.name 不能为空")
	}
	if def.Namespaced {
		if obj.GetNamespace() != "" && obj.GetNamespace() != namespace {
			return nil, fmt.Errorf("metadata.namespace %q 与目标命名空间 %q 不一致", obj.GetNamespace(), namespace)
		}
		obj.SetNamespace(namespace)
	}
	// Fields the apiserver owns or that a client must not dictate.
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	obj.SetUID("")
	obj.SetSelfLink("")
	return obj, nil
}

// parseManifest turns the submitted YAML/JSON text into an
// Unstructured and validates it against the resolved target: the GVK
// must equal the allowlisted def (the client can never redirect the
// manifest at a different kind), metadata.name must equal the path
// parameter, namespace must match the resolved one, and
// resourceVersion is mandatory — it is the update precondition the
// apiserver enforces atomically.
func parseManifest(manifest string, def kube.ResourceDef, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := parseManifestObject(manifest, def, namespace)
	if err != nil {
		return nil, err
	}
	if obj.GetName() != name {
		return nil, fmt.Errorf("metadata.name %q 与路径资源名 %q 不一致", obj.GetName(), name)
	}
	if obj.GetResourceVersion() == "" {
		return nil, fmt.Errorf("metadata.resourceVersion 缺失：请基于最新对象编辑后提交")
	}
	obj.SetGeneration(0)
	obj.SetCreationTimestamp(metav1.Time{})
	return obj, nil
}

// parseCreateManifest validates a manifest for create: everything
// parseManifestObject guarantees, plus the name must be a DNS-1123
// subdomain and the manifest must not carry server-owned state — a
// resourceVersion would be meaningless (and silently dropped) on create,
// and status is never client-supplied.
func parseCreateManifest(manifest string, def kube.ResourceDef, namespace string) (*unstructured.Unstructured, error) {
	obj, err := parseManifestObject(manifest, def, namespace)
	if err != nil {
		return nil, err
	}
	if errs := validation.IsDNS1123Subdomain(obj.GetName()); len(errs) > 0 {
		return nil, fmt.Errorf("非法 metadata.name %q: %s", obj.GetName(), strings.Join(errs, "; "))
	}
	if obj.GetResourceVersion() != "" {
		return nil, fmt.Errorf("新建资源不能携带 metadata.resourceVersion")
	}
	obj.SetGeneration(0)
	obj.SetCreationTimestamp(metav1.Time{})
	unstructured.RemoveNestedField(obj.Object, "status")
	return obj, nil
}

// mergeResourceData applies the data-editor diff to the live object in
// place: deleteKeys are removed, set entries overwrite — for secrets the
// plaintext values are base64-encoded into `data` (the stringData
// semantics the apiserver applies on create), for configmaps they stay
// verbatim strings. Values present in the live object but untouched by
// the diff keep their real content — that is the whole point of doing
// the merge server-side.
func mergeResourceData(obj *unstructured.Unstructured, def kube.ResourceDef, set map[string]string, deleteKeys []string, typ string) {
	data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
	if data == nil {
		data = map[string]string{}
	}
	for _, k := range deleteKeys {
		delete(data, k)
	}
	isSecret := kube.IsSecretDef(def)
	for k, v := range set {
		if isSecret {
			data[k] = base64.StdEncoding.EncodeToString([]byte(v))
		} else {
			data[k] = v
		}
	}
	// unstructured requires map[string]any — assigning the string-typed
	// map back would panic DeepCopyJSON inside the runtime layer.
	merged := make(map[string]any, len(data))
	for k, v := range data {
		merged[k] = v
	}
	obj.Object["data"] = merged
	if typ != "" {
		obj.Object["type"] = typ
	}
	// The object came straight from a GET: strip server-owned noise the
	// replace payload must not dictate (uid/rv stay — uid pins identity,
	// rv carries the optimistic-concurrency precondition).
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	obj.SetSelfLink("")
}

// dataEditDetail renders the audit line for a data edit: key NAMES only.
// Values never appear — the audit record must not become a side channel
// for secret material.
func dataEditDetail(set map[string]string, deleteKeys []string, typ string) string {
	setKeys := make([]string, 0, len(set))
	for k := range set {
		setKeys = append(setKeys, k)
	}
	sort.Strings(setKeys)
	del := append([]string(nil), deleteKeys...)
	sort.Strings(del)
	parts := make([]string, 0, 3)
	if len(setKeys) > 0 {
		parts = append(parts, "keys +"+strings.Join(setKeys, ","))
	}
	if len(del) > 0 {
		parts = append(parts, "keys -"+strings.Join(del, ","))
	}
	if typ != "" {
		parts = append(parts, "type="+typ)
	}
	return strings.Join(parts, " ")
}

// resourceDef resolves the group/version/resource triple strictly through
// the server-side allowlist — client input never becomes an arbitrary GVR.
func resourceDef(c *gin.Context) (kube.ResourceDef, bool) {
	version := strings.TrimSpace(c.Query("version"))
	resource := strings.TrimSpace(c.Query("resource"))
	if version == "" || resource == "" {
		fail(c, http.StatusBadRequest, "version 和 resource 为必填参数")
		return kube.ResourceDef{}, false
	}
	def, err := kube.LookupResource(strings.TrimSpace(c.Query("group")), version, resource)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return kube.ResourceDef{}, false
	}
	return def, true
}

// writeContext bundles the three validations every write endpoint
// shares: cluster+runtime, allowlist GVR, concrete namespace.
func writeContext(c *gin.Context, store *service.Store, k8s *service.K8sService) (service.KubernetesRuntime, kube.ResourceDef, string, bool) {
	rt, cluster, ok := resolveCluster(c, store, k8s)
	if !ok {
		return nil, kube.ResourceDef{}, "", false
	}
	def, ok := resourceDef(c)
	if !ok {
		return nil, kube.ResourceDef{}, "", false
	}
	ns, ok := resourceNamespace(c, cluster, def, false)
	if !ok {
		return nil, kube.ResourceDef{}, "", false
	}
	return rt, def, ns, true
}

// resourceNamespace resolves the effective namespace for a resource def.
// Cluster-scoped defs ignore namespace. For namespaced defs an explicit
// namespace must validate as DNS-1123; allowAll permits all_namespaces=true
// (list only) which resolves to "".
func resourceNamespace(c *gin.Context, cluster service.Cluster, def kube.ResourceDef, allowAll bool) (string, bool) {
	if !def.Namespaced {
		return "", true
	}
	if raw := strings.TrimSpace(c.Query("all_namespaces")); raw != "" {
		all, err := strconv.ParseBool(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法参数 all_namespaces")
			return "", false
		}
		if all {
			if !allowAll {
				fail(c, http.StatusBadRequest, "namespace 为必填参数")
				return "", false
			}
			return "", true
		}
	}
	ns := strings.TrimSpace(c.Query("namespace"))
	if ns == "" {
		ns = cluster.DefaultNamespace
	}
	if ns == "" {
		fail(c, http.StatusBadRequest, "namespace 为必填参数")
		return "", false
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		fail(c, http.StatusBadRequest, fmt.Sprintf("非法 namespace: %s", ns))
		return "", false
	}
	return ns, true
}

// writeResourceErr translates runtime errors into stable HTTP semantics:
// cluster-unsupported allowlisted types and Kubernetes NotFound → 404,
// Kubernetes Forbidden → 403, Kubernetes BadRequest (e.g. malformed
// selectors) → 400, everything else → 500.
func writeResourceErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, kube.ErrResourceUnsupported), apierrors.IsNotFound(err):
		fail(c, http.StatusNotFound, err.Error())
	case apierrors.IsForbidden(err):
		fail(c, http.StatusForbidden, err.Error())
	case apierrors.IsAlreadyExists(err):
		fail(c, http.StatusConflict, fmt.Sprintf("同名资源已存在: %s", err.Error()))
	case apierrors.IsConflict(err):
		fail(c, http.StatusConflict, fmt.Sprintf("资源已被修改，请刷新后重试: %s", err.Error()))
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		fail(c, http.StatusBadRequest, err.Error())
	default:
		fail(c, http.StatusInternalServerError, err.Error())
	}
}
