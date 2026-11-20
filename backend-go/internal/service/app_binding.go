package service

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

var repositorySegment = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var registryName = regexp.MustCompile(`^(?:localhost|[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)(?::[0-9]{1,5})?$`)

// ImageRepository removes a tag/digest but preserves the exact registry and
// repository. No suffix/keyword matching, URL fetching or short-name expansion.
func ImageRepository(image string) string {
	if _, err := ImageVersion(image); err != nil {
		return ""
	}
	repo := strings.SplitN(image, "@", 2)[0]
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	if !ValidImageRepository(repo) {
		return ""
	}
	return repo
}

func ValidImageRepository(repo string) bool {
	if len(repo) == 0 || len(repo) > 512 || strings.ContainsAny(repo, "@?# \\") {
		return false
	}
	parts := strings.Split(repo, "/")
	if len(parts) < 2 || !registryName.MatchString(parts[0]) || (!strings.ContainsAny(parts[0], ".:") && parts[0] != "localhost") {
		return false
	}
	for _, p := range parts[1:] {
		if !repositorySegment.MatchString(p) {
			return false
		}
	}
	return true
}

// ValidateAppSources checks metadata only; it never clones or contacts the code
// repository. Empty source fields retain the existing mapping contract.
func ValidateAppSources(a App) error {
	a = NormalizeAppSources(a)
	if a.SourceKind != "repository" && a.SourceKind != "third_party" && a.SourceKind != "unknown" {
		return NewValidationError("来源类型必须是代码仓库、第三方镜像或待确认")
	}
	if a.SourceKind == "repository" && a.SourceRepoURL == "" {
		return NewValidationError("选择代码仓库来源时必须填写仓库地址")
	}
	if a.SourceKind == "third_party" && (a.SourceRepoURL != "" || a.SourcePath != "") {
		return NewValidationError("第三方镜像来源不能填写代码仓库或仓库目录")
	}
	if a.SourceKind == "unknown" && a.SourceRepoURL != "" {
		return NewValidationError("请确认该组件使用代码仓库还是第三方镜像")
	}
	if a.ImageRepository != "" && !ValidImageRepository(a.ImageRepository) {
		return NewValidationError("镜像仓库应为完整 registry/项目/镜像名称，不包含协议、tag 或 digest")
	}
	if a.SourceRepoURL != "" {
		u, err := url.Parse(a.SourceRepoURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" || len(a.SourceRepoURL) > 1024 {
			return NewValidationError("代码仓库请填写不含凭据和查询参数的 HTTPS 仓库地址")
		}
		if a.ImageRepository == "" {
			return NewValidationError("登记代码仓库时需要同时绑定镜像仓库")
		}
	}
	if a.SourcePath != "" {
		if a.SourceRepoURL == "" || len(a.SourcePath) > 512 || strings.HasPrefix(a.SourcePath, "/") || strings.Contains(a.SourcePath, "\\") || path.Clean(a.SourcePath) != a.SourcePath {
			return NewValidationError("构建目录应为仓库内相对路径，例如 . 或 services/order-api")
		}
		for _, p := range strings.Split(a.SourcePath, "/") {
			if p == ".." {
				return NewValidationError("构建目录不能越出代码仓库")
			}
		}
	}
	if _, _, err := normalizeDeploymentConfigSource(a.DeploymentConfigRepoURL, a.DeploymentConfigPath); err != nil {
		return err
	}
	return nil
}

func NormalizeAppSources(a App) App {
	if a.SourceKind == "" {
		if a.SourceRepoURL != "" {
			a.SourceKind = "repository"
		} else {
			a.SourceKind = "unknown"
		}
	}
	a.DeploymentConfigRepoURL = strings.TrimSpace(a.DeploymentConfigRepoURL)
	a.DeploymentConfigPath = strings.TrimSpace(a.DeploymentConfigPath)
	if a.DeploymentConfigRepoURL != "" && a.DeploymentConfigPath == "" {
		a.DeploymentConfigPath = "."
	}
	return a
}

func normalizeDeploymentConfigSource(repository, repositoryPath string) (string, string, error) {
	repository = strings.TrimSpace(repository)
	repositoryPath = strings.TrimSpace(repositoryPath)
	if repository == "" {
		if repositoryPath != "" {
			return "", "", NewValidationError("填写部署配置目录前，请先填写配置仓库地址")
		}
		return "", "", nil
	}
	u, err := url.Parse(repository)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" || len(repository) > 1024 {
		return "", "", NewValidationError("部署配置仓库请填写不含凭据和查询参数的 HTTPS 仓库地址")
	}
	if repositoryPath == "" {
		repositoryPath = "."
	}
	if len(repositoryPath) > 512 || strings.HasPrefix(repositoryPath, "/") || strings.Contains(repositoryPath, "\\") || strings.ContainsAny(repositoryPath, "\x00\r\n") {
		return "", "", NewValidationError("部署配置目录必须是仓库内相对路径，不能越出仓库")
	}
	if repositoryPath != "." {
		for _, segment := range strings.Split(repositoryPath, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return "", "", NewValidationError("部署配置目录不能包含空段、`.` 或 `..` 路径段")
			}
		}
	}
	return u.String(), repositoryPath, nil
}

func CheckAppImage(a App, image string) error {
	if a.ImageRepository != "" && ImageRepository(image) != a.ImageRepository {
		return NewValidationError("组件 %s 绑定镜像仓库 %s，收到的镜像不属于该仓库", a.AppName, a.ImageRepository)
	}
	return nil
}
