package repository

import (
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestComponentSourcesPersistAcrossSaveUpdateAndReload(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	a := service.App{ClusterID: 1, AppName: "order-api", MatchKeyword: "order-api", Namespace: "orders", DeploymentName: "api", ContainerName: "api", HarborProject: "orders", SourceKind: "repository", SourceRepoURL: "https://git.example.com/orders.git", SourcePath: "services/api", ImageRepository: "reg.io/orders/api", DeploymentConfigRepoURL: "https://git.example.com/orders/deploy-config.git", DeploymentConfigPath: "charts/orders"}
	mock.ExpectExec("INSERT INTO apps").WithArgs(a.ClusterID, a.AppName, a.MatchKeyword, a.DeploymentName, a.ContainerName, a.Namespace, a.HarborProject, a.Description, a.SourceRepoURL, a.SourcePath, a.ImageRepository, a.SourceKind, a.DeploymentConfigRepoURL, a.DeploymentConfigPath).WillReturnResult(sqlmock.NewResult(5, 1))
	a, err = repo.SaveApp(a)
	if err != nil {
		t.Fatal(err)
	}
	a.SourcePath = "services/order-api"
	mock.ExpectExec("UPDATE apps SET").WithArgs(a.ClusterID, a.AppName, a.MatchKeyword, a.DeploymentName, a.ContainerName, a.Namespace, a.HarborProject, a.Description, a.SourceRepoURL, a.SourcePath, a.ImageRepository, a.SourceKind, a.DeploymentConfigRepoURL, a.DeploymentConfigPath, 5).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateApp(a); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT .* FROM apps WHERE cluster_id=\\? ORDER BY id").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"id", "cluster_id", "app_name", "match_keyword", "deployment_name", "container_name", "namespace", "harbor_project", "description", "created_at", "source_repo_url", "source_path", "image_repository", "source_kind", "deployment_config_repo_url", "deployment_config_path"}).AddRow(5, 1, a.AppName, a.MatchKeyword, a.DeploymentName, a.ContainerName, a.Namespace, a.HarborProject, nil, time.Now(), a.SourceRepoURL, a.SourcePath, a.ImageRepository, a.SourceKind, a.DeploymentConfigRepoURL, a.DeploymentConfigPath))
	loaded, err := repo.Apps(1)
	if err != nil || len(loaded) != 1 || loaded[0].SourceKind != a.SourceKind || loaded[0].SourcePath != a.SourcePath || loaded[0].SourceRepoURL != a.SourceRepoURL || loaded[0].ImageRepository != a.ImageRepository || loaded[0].DeploymentConfigRepoURL != a.DeploymentConfigRepoURL || loaded[0].DeploymentConfigPath != a.DeploymentConfigPath {
		t.Fatalf("reload=%+v %v", loaded, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
