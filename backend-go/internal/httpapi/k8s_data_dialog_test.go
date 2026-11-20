package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/service"
)

func TestSecretRevealDetailContract(t *testing.T) {
	app, e, _ := newTestApp(t)
	path := "/api/v1/k8s/resources/s1?cluster_id=1&version=v1&resource=secrets&namespace=prod"
	for _, reveal := range []bool{false, true} {
		url := path
		if reveal {
			url += "&reveal_secret=true"
		}
		w := do(t, e, "GET", url, nil)
		if w.Code != 200 {
			t.Fatalf("read → %d %s", w.Code, w.Body.String())
		}
		var obj map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		want := "***"
		if reveal {
			want = "djE="
		}
		if got := obj["data"].(map[string]any)["keep"]; got != want {
			t.Fatalf("reveal=%v got=%v want=%v", reveal, got, want)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Secret detail must not be cached by HTTP intermediaries")
		}
	}
	if !hasAudit(app.store, "secret_view", "s1", service.AuditSuccess) {
		t.Fatal("reveal read must be audited")
	}
	for _, a := range app.store.Audits(10) {
		if strings.Contains(a.Detail, "djE=") {
			t.Fatal("reveal data must not enter audit detail")
		}
	}
	// Reveal applies only to an authenticated detail request, never the list.
	w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=secrets&namespace=prod&reveal_secret=true", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "djE=") {
		t.Fatalf("list must stay masked: %s", w.Body.String())
	}
	w = do(t, e, "GET", path+"&reveal_secret=true", nil, "")
	if w.Code != 401 {
		t.Fatalf("anonymous reveal → %d want 401", w.Code)
	}
}

func TestSecretDialogMetadataAndEmptyValue(t *testing.T) {
	_, e, _ := newTestApp(t)
	path := "/api/v1/k8s/resources/s1/data?cluster_id=1&version=v1&resource=secrets&namespace=prod"
	for _, dryRun := range []bool{true, false} {
		url := path
		if dryRun {
			url += "&dry_run=true"
		}
		w := do(t, e, "POST", url, map[string]any{
			"resource_version": "1", "set": map[string]string{"keep": ""}, "immutable": true,
			"metadata": map[string]any{"labels": map[string]string{"app": "web"}, "annotations": map[string]string{"team/owner": "infra"}},
		})
		if w.Code != 200 {
			t.Fatalf("save → %d %s", w.Code, w.Body.String())
		}
		var obj map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		if obj["immutable"] != true {
			t.Fatal("immutable was not applied")
		}
		meta := obj["metadata"].(map[string]any)
		if meta["labels"].(map[string]any)["app"] != "web" || meta["annotations"].(map[string]any)["team/owner"] != "infra" {
			t.Fatal("metadata was not applied")
		}
		if obj["data"].(map[string]any)["keep"] != "***" {
			t.Fatal("write response must remain masked")
		}
	}
	// Metadata maps can be cleared without transmitting any Secret values.
	w := do(t, e, "POST", path, map[string]any{"resource_version": "1", "metadata": map[string]any{"labels": map[string]string{}, "annotations": map[string]string{}}})
	if w.Code != 200 {
		t.Fatalf("clear metadata → %d %s", w.Code, w.Body.String())
	}
}

func TestImmutableSecretDialogUpdates(t *testing.T) {
	secret := `{"apiVersion":"v1","kind":"Secret","type":"Opaque","immutable":true,"metadata":{"name":"s1","namespace":"prod","resourceVersion":"7"},"data":{"keep":"djE="}}`
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"metadata allowed", map[string]any{"resource_version": "7", "metadata": map[string]any{"labels": map[string]string{"app": "updated"}}}, 200},
		{"cannot unfreeze", map[string]any{"resource_version": "7", "immutable": false}, 400},
		{"cannot edit data", map[string]any{"resource_version": "7", "set": map[string]string{"keep": "new"}}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &scriptRunner{fn: func(args []string) (string, string, error) { return secret, "", nil }}
			e := newSSHEngine(t, runner)
			w := do(t, e, "POST", "/api/v1/k8s/resources/s1/data?cluster_id=1&version=v1&resource=secrets&namespace=prod", tc.body)
			if w.Code != tc.want {
				t.Fatalf("update → %d want %d %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 200 {
				var obj map[string]any
				if err := json.Unmarshal(runner.stdin, &obj); err != nil {
					t.Fatal(err)
				}
				if obj["data"].(map[string]any)["keep"] != "djE=" {
					t.Fatal("metadata update changed Secret data")
				}
			} else if len(runner.recorded()) != 1 {
				t.Fatal("rejected update must not execute replace")
			}
		})
	}
}
