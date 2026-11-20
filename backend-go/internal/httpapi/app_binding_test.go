package httpapi

import (
	"encoding/json"
	"testing"
)

func bindingBody() map[string]any {
	return map[string]any{"cluster_id": 1, "app_name": "order-api", "source_repo_url": "https://git.example.com/team/order.git", "source_path": "services/order-api", "deployment_config_repo_url": "https://git.example.com/team/deploy-config.git", "deployment_config_path": "charts/order-api", "image_repository": "registry.example.com/team/order-api", "namespace": "default", "deployment_name": "mock-deployments-1", "container_name": "app"}
}

func TestBindingPreviewIsReadOnlyAndSaveChecksContainerAgain(t *testing.T) {
	app, engine, _ := newTestApp(t)
	body := bindingBody()
	body["deployment_config_path"] = ""
	w := do(t, engine, "POST", "/api/v1/apps/preview", body)
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		App struct {
			DeploymentConfigPath string `json:"deployment_config_path"`
		} `json:"app"`
		CurrentImage string   `json:"current_image"`
		Warnings     []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.CurrentImage == "" || len(preview.Warnings) != 1 || preview.App.DeploymentConfigPath != "." {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	if len(app.store.Apps(0)) != 0 || len(app.store.Logs(0, "", "", 0)) != 0 || len(app.store.ImageReleases("", 0)) != 0 {
		t.Fatal("preview changed durable state")
	}
	body["deployment_config_path"] = "charts/order-api"
	body["container_name"] = "missing-container"
	if w = do(t, engine, "POST", "/api/v1/apps", body); w.Code != 400 {
		t.Fatalf("phantom container: %d %s", w.Code, w.Body.String())
	}
	body["container_name"] = "app"
	if w = do(t, engine, "POST", "/api/v1/apps", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored := app.store.Apps(1)[0]
	if stored.SourcePath != "services/order-api" || stored.DeploymentConfigRepoURL != body["deployment_config_repo_url"] || stored.DeploymentConfigPath != body["deployment_config_path"] || stored.ImageRepository != body["image_repository"] || stored.MatchKeyword != "order-api" {
		t.Fatalf("lost fields: %+v", stored)
	}
	if len(app.store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("saving binding deployed an image")
	}
}

func TestBindingPreviewPermissionsAndMissingTarget(t *testing.T) {
	app, engine, _ := newTestApp(t)
	body := bindingBody()
	viewer := seedUser(t, app.store, "binding-viewer", "viewer")
	if w := do(t, engine, "POST", "/api/v1/apps/preview", body, viewer); w.Code != 403 {
		t.Fatalf("viewer preview: %d", w.Code)
	}
	body["deployment_name"] = "missing"
	if w := do(t, engine, "POST", "/api/v1/apps/preview", body); w.Code != 404 {
		t.Fatalf("missing Deployment: %d %s", w.Code, w.Body.String())
	}
	body["cluster_id"] = 999
	if w := do(t, engine, "POST", "/api/v1/apps", body); w.Code != 404 {
		t.Fatalf("missing cluster: %d", w.Code)
	}
}
