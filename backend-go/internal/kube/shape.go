package kube

import "fmt"

// PodSummary converts one item of `kubectl get pods -o json` into the
// response shape produced by the Python backend.
func PodSummary(item map[string]any) map[string]any {
	meta := asMap(item["metadata"])
	status := asMap(item["status"])
	spec := asMap(item["spec"])
	containers := asSlice(status["containerStatuses"])
	specContainers := asSlice(spec["containers"])

	image := ""
	if len(specContainers) > 0 {
		image = asString(asMap(specContainers[0])["image"])
	}
	ready := 0
	restarts := 0
	for _, c := range containers {
		m := asMap(c)
		if b, _ := m["ready"].(bool); b {
			ready++
		}
		restarts += intValue(m["restartCount"])
	}
	// kubectl's RESTARTS column also counts restartable init containers
	// (native sidecars with restartPolicy: Always).
	sidecars := map[string]bool{}
	for _, ic := range asSlice(spec["initContainers"]) {
		m := asMap(ic)
		if asString(m["restartPolicy"]) == "Always" {
			sidecars[asString(m["name"])] = true
		}
	}
	for _, c := range asSlice(status["initContainerStatuses"]) {
		m := asMap(c)
		if sidecars[asString(m["name"])] {
			restarts += intValue(m["restartCount"])
		}
	}
	total := len(containers)
	if total == 0 {
		total = len(specContainers)
	}
	phase := asString(status["phase"])
	if phase == "" {
		phase = "Unknown"
	}
	return map[string]any{
		"name":      asString(meta["name"]),
		"status":    phase,
		"ready":     fmt.Sprintf("%d/%d", ready, total),
		"restarts":  restarts,
		"image":     image,
		"app_label": asString(asMap(meta["labels"])["app"]),
	}
}

// DeploymentInfoFromMap converts `kubectl get deployment -o json` output into
// the response shape produced by the Python backend.
func DeploymentInfoFromMap(d map[string]any) map[string]any {
	meta := asMap(d["metadata"])
	spec := asMap(d["spec"])
	status := asMap(d["status"])
	tmplSpec := asMap(asMap(spec["template"])["spec"])

	container := map[string]any{}
	if cs := asSlice(tmplSpec["containers"]); len(cs) > 0 {
		container = asMap(cs[0])
	}
	configMaps := []any{}
	for _, ef := range asSlice(container["envFrom"]) {
		if ref, ok := asMap(ef)["configMapRef"]; ok {
			configMaps = append(configMaps, asMap(ref)["name"])
		}
	}
	return map[string]any{
		"name":        asString(meta["name"]),
		"namespace":   asString(meta["namespace"]),
		"replicas":    fmt.Sprintf("%d/%d", intValue(status["readyReplicas"]), intValue(spec["replicas"])),
		"image":       asString(container["image"]),
		"strategy":    asString(asMap(spec["strategy"])["type"]),
		"env":         orEmptySlice(container["env"]),
		"config_maps": configMaps,
		"ports":       orEmptySlice(container["ports"]),
		"resources":   orEmptyMap(container["resources"]),
		"conditions":  orEmptySlice(status["conditions"]),
	}
}

// ContainerImage returns the image of the named container in a deployment's
// pod template. An empty container name selects the first container.
func ContainerImage(d map[string]any, containerName string) (string, error) {
	containers := asSlice(asMap(asMap(asMap(d["spec"])["template"])["spec"])["containers"])
	for _, c := range containers {
		m := asMap(c)
		if containerName == "" || asString(m["name"]) == containerName {
			return asString(m["image"]), nil
		}
	}
	return "", fmt.Errorf("容器 %s 不存在", containerName)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	}
	return 0
}

func orEmptySlice(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

func orEmptyMap(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
