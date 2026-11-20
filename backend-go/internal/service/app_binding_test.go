package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExplicitRepositoryCannotMatchBySuffixOrKeyword(t *testing.T) {
	good := "registry.example.com:5443/team/order-api"
	a := App{AppName: "order-api", ImageRepository: good}
	for _, ref := range []string{good + ":v2", good + "@sha256:" + strings.Repeat("ab", 32), good + ":v2@sha256:" + strings.Repeat("ab", 32)} {
		if ImageRepository(ref) != good || CheckAppImage(a, ref) != nil {
			t.Fatalf("rejected %s", ref)
		}
	}
	for _, ref := range []string{"other.example.com:5443/team/order-api:v2", "registry.example.com:5443/team/order-api-extra:v2", "registry.example.com:5443/other/order-api:v2", "https://" + good + ":v2", good + "@sha256:short", good + ":", good + ":v2?token=secret"} {
		if CheckAppImage(a, ref) == nil {
			t.Fatalf("accepted wrong repository %s", ref)
		}
	}
}

func TestSourceMetadataValidationDoesNotAcceptCredentialsOrPathTraversal(t *testing.T) {
	a := App{SourceRepoURL: "https://git.example.com/team/order.git", SourcePath: "services/order", ImageRepository: "registry.example.com/team/order"}
	if err := ValidateAppSources(a); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"https://token@git.example.com/team/order.git", "https://git.example.com/team/order.git?token=secret", "http://git.example.com/team/order.git", "https://git.example.com"} {
		b := a
		b.SourceRepoURL = source
		if ValidateAppSources(b) == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	for _, source := range []string{"../outside", "services/../../outside", "/services/order", "services\\order"} {
		b := a
		b.SourcePath = source
		if ValidateAppSources(b) == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	if err := ValidateAppSources(App{}); err != nil {
		t.Fatalf("legacy mapping: %v", err)
	}
}

func TestDeploymentConfigSourceValidationAndDefaultPath(t *testing.T) {
	a := App{
		DeploymentConfigRepoURL: " https://git.example.com/team/deploy-config.git ",
	}
	normalized := NormalizeAppSources(a)
	if normalized.DeploymentConfigPath != "." {
		t.Fatalf("empty configuration path did not default to repository root: %+v", normalized)
	}
	if err := ValidateAppSources(a); err != nil {
		t.Fatalf("valid configuration repository rejected: %v", err)
	}
	for _, source := range []string{
		"https://token@git.example.com/team/deploy-config.git",
		"https://git.example.com/team/deploy-config.git?token=secret",
		"http://git.example.com/team/deploy-config.git",
		"https://git.example.com",
	} {
		if err := ValidateAppSources(App{DeploymentConfigRepoURL: source}); err == nil {
			t.Fatalf("accepted unsafe configuration repository %q", source)
		}
	}
	for _, configPath := range []string{"../outside", "charts/../../prod", "/charts/api", "charts//api", "charts/./api", "charts\\api"} {
		if err := ValidateAppSources(App{DeploymentConfigRepoURL: a.DeploymentConfigRepoURL, DeploymentConfigPath: configPath}); err == nil {
			t.Fatalf("accepted unsafe configuration path %q", configPath)
		}
	}
	if err := ValidateAppSources(App{DeploymentConfigPath: "charts/api"}); err == nil {
		t.Fatal("accepted a configuration path without a repository")
	}
}

func TestWrongRepositoryCannotCreateReleaseAndBindingIsRecorded(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	a, _ := store.FindAppByName(1, "app")
	a.SourceRepoURL = "https://git.example.com/team/app.git"
	a.SourcePath = "services/api"
	a.ImageRepository = "reg.io/Demo/app"
	if _, err := store.UpdateApp(a); err != nil {
		t.Fatal(err)
	}
	ev := cdEvent()
	ev.ImageFull = "evil.io/Demo/app:v1"
	if _, _, _, err := cd.Receive(context.Background(), ev); err == nil {
		t.Fatal("wrong repository accepted")
	}
	if len(store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("wrong repository queued a task")
	}
	r, l, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	waitLog(t, store, l.ID)
	if r.Metadata.Binding == nil || r.Metadata.Binding.SourceRepoURL != a.SourceRepoURL || r.Metadata.Binding.DeploymentName != a.DeploymentName {
		t.Fatalf("binding not captured: %+v", r.Metadata)
	}
	a.SourcePath = "services/renamed"
	if _, err := store.UpdateApp(a); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.FindImageRelease(r.ID)
	if stored.Metadata.Binding.SourcePath != "services/api" {
		t.Fatal("historical source changed with mapping")
	}
	attestServiceRelease(t, store, r.ID)
	prod, _ := store.FindAppByName(2, "app")
	prod.ImageRepository = "other.io/Demo/app"
	if _, err := store.UpdateApp(prod); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.RequestPromotion(r.ID, 2, "alice"); err == nil {
		t.Fatal("production repository mismatch accepted")
	}
}

func TestLogicalComponentRequiresOneDeploymentConfigSourceAcrossEnvironments(t *testing.T) {
	_, store, _, _, _, _ := cdFixture(t)
	model := deliveryModelForPlan("rolling")
	model.Bindings = nil
	store.LoadDeliveryModel(model)
	testApp, err := store.FindAppByName(1, "app")
	if err != nil {
		t.Fatal(err)
	}
	prodApp, err := store.FindAppByName(2, "app")
	if err != nil {
		t.Fatal(err)
	}
	testApp.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
	testApp.DeploymentConfigPath = "charts/orders"
	prodApp.DeploymentConfigRepoURL = "https://git.example.com/orders/other-config.git"
	prodApp.DeploymentConfigPath = "charts/orders"
	if _, err := store.UpdateApp(testApp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateApp(prodApp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignDeliveryBinding(testApp.ID, 21, 31, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignDeliveryBinding(prodApp.ID, 21, 31, 12, ""); err == nil || !strings.Contains(err.Error(), "部署配置来源") {
		t.Fatalf("same logical component accepted different deployment configuration sources: %v", err)
	}
	prodApp.DeploymentConfigRepoURL = testApp.DeploymentConfigRepoURL
	prodApp.DeploymentConfigPath = testApp.DeploymentConfigPath
	if _, err := store.UpdateApp(prodApp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignDeliveryBinding(prodApp.ID, 21, 31, 12, ""); err != nil {
		t.Fatalf("same component source across environments rejected: %v", err)
	}
	testApp.DeploymentConfigPath = "charts/next"
	if _, err := store.UpdateApp(testApp); err == nil || !strings.Contains(err.Error(), "部署配置来源") {
		t.Fatalf("component mapping changed for one linked environment only: %v", err)
	}
}

func TestDeploymentPipelineBindingIsSeparateValidatedAndClearable(t *testing.T) {
	_, store, _, _, _, _ := cdFixture(t)
	model := deliveryModelForPlan("rolling")
	model.Bindings = nil
	store.LoadDeliveryModel(model)
	app, err := store.FindAppByName(1, "app")
	if err != nil {
		t.Fatal(err)
	}
	app.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
	app.DeploymentConfigPath = "charts/orders"
	if _, err := store.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	store.LoadYunxiaoConnections([]YunxiaoConnection{{ID: 9, Name: "production"}})

	pipeline := &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"}
	expectedPipeline := *pipeline
	binding, err := store.AssignDeliveryBindingWithDeploymentPipeline(app.ID, 21, 31, 11, "", "", pipeline)
	if err != nil || binding.DeploymentPipeline == nil || *binding.DeploymentPipeline != *pipeline {
		t.Fatalf("deployment pipeline binding=%+v err=%v", binding, err)
	}
	pipeline.PipelineID = "external-mutation"
	view := store.DeliveryModel()
	view.Bindings[0].DeploymentPipeline.PipelineID = "view-mutation"
	if stored := store.DeliveryModel().Bindings[0].DeploymentPipeline; stored == nil || stored.PipelineID != "123456" {
		t.Fatalf("deployment pipeline binding leaked mutable state: %+v", stored)
	}

	// Legacy binding updates that omit the optional field retain its existing
	// value; an explicit empty object removes it.
	legacyUpdate, err := store.AssignDeliveryBinding(app.ID, 21, 31, 11, "")
	if err != nil || legacyUpdate.DeploymentPipeline == nil || *legacyUpdate.DeploymentPipeline != expectedPipeline {
		t.Fatalf("legacy update erased deployment pipeline: binding=%+v err=%v", legacyUpdate, err)
	}
	cleared, err := store.AssignDeliveryBindingWithDeploymentPipeline(app.ID, 21, 31, 11, "", "", &YunxiaoDeploymentPipelineBinding{})
	if err != nil || cleared.DeploymentPipeline != nil {
		t.Fatalf("explicit clear failed: binding=%+v err=%v", cleared, err)
	}

	for name, invalid := range map[string]*YunxiaoDeploymentPipelineBinding{
		"missing connection": {PipelineID: "123456"},
		"invalid ID":         {ConnectionID: 9, PipelineID: "not-a-number"},
		"unknown connection": {ConnectionID: 10, PipelineID: "123456"},
	} {
		if _, err := store.AssignDeliveryBindingWithDeploymentPipeline(app.ID, 21, 31, 11, "", "", invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}

	app.DeploymentConfigRepoURL = ""
	app.DeploymentConfigPath = ""
	if _, err := store.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignDeliveryBindingWithDeploymentPipeline(app.ID, 21, 31, 11, "", "", pipeline); !errors.Is(err, ErrValidation) {
		t.Fatalf("pipeline without registered config repository should fail: %v", err)
	}
}
