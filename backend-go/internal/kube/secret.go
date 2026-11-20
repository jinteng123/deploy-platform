package kube

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// MaskedValue replaces every entry under a Secret's data/stringData so
// keys stay visible (they describe the secret's shape) while values
// stay masked on lists, ordinary detail reads and write responses.
// "***" is not valid base64, which also makes an
// accidental write-back of masked content fail loudly.
const MaskedValue = "***"

// IsSecretDef reports whether the allowlist entry is the core Secret
// kind — the only resource whose payload is masked at the API layer.
func IsSecretDef(def ResourceDef) bool {
	return def.Group == "" && def.Resource == "secrets"
}

// MaskSecretData scrubs data/stringData values on a Secret object in
// place. Detail reads can opt in to an audited reveal; ordinary reads
// and writes use this mask. Whole-manifest Secret writes stay disabled.
func MaskSecretData(obj *unstructured.Unstructured) {
	if obj == nil {
		return
	}
	for _, field := range []string{"data", "stringData"} {
		raw, found, _ := unstructured.NestedMap(obj.Object, field)
		if !found {
			continue
		}
		masked := make(map[string]any, len(raw))
		for k := range raw {
			masked[k] = MaskedValue
		}
		obj.Object[field] = masked
	}
}
