package service

import (
	"fmt"
	"net/url"
	"strings"
)

// sourceRepositoryIdentity returns a credential-free display value and a
// comparison key for common HTTPS, SSH, git, and scp-style Git remote URLs.
// Protocol and the conventional .git suffix do not change repository identity.
func sourceRepositoryIdentity(raw string) (display, identity string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 || strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == '\\' }) >= 0 {
		return "", "", fmt.Errorf("invalid repository URL")
	}
	var host, repositoryPath string
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		remote := strings.TrimPrefix(raw, "git@")
		var found bool
		host, repositoryPath, found = strings.Cut(remote, ":")
		if !found {
			return "", "", fmt.Errorf("invalid repository URL")
		}
	} else {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u == nil {
			return "", "", fmt.Errorf("invalid repository URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http", "ssh", "git":
		default:
			return "", "", fmt.Errorf("unsupported repository URL")
		}
		if u.User != nil {
			if strings.ToLower(u.Scheme) != "ssh" || u.User.Username() != "git" {
				return "", "", fmt.Errorf("repository URL must not contain credentials")
			}
			if _, hasPassword := u.User.Password(); hasPassword {
				return "", "", fmt.Errorf("repository URL must not contain credentials")
			}
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return "", "", fmt.Errorf("repository URL must not contain query or fragment")
		}
		host = u.Hostname()
		port := u.Port()
		if port != "" && !((strings.EqualFold(u.Scheme, "https") && port == "443") || (strings.EqualFold(u.Scheme, "http") && port == "80") || (strings.EqualFold(u.Scheme, "ssh") && port == "22")) {
			host += ":" + port
		}
		repositoryPath, parseErr = url.PathUnescape(u.EscapedPath())
		if parseErr != nil {
			return "", "", fmt.Errorf("invalid repository path")
		}
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	repositoryPath = strings.Trim(repositoryPath, "/")
	if host == "" || repositoryPath == "" || strings.ContainsAny(repositoryPath, "?#") || strings.IndexFunc(repositoryPath, func(r rune) bool { return r <= ' ' || r == '\\' }) >= 0 {
		return "", "", fmt.Errorf("repository URL must include host and path")
	}
	for _, segment := range strings.Split(repositoryPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", fmt.Errorf("invalid repository path")
		}
	}
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	if repositoryPath == "" || strings.HasSuffix(repositoryPath, "/") {
		return "", "", fmt.Errorf("repository URL must include repository name")
	}
	identity = host + "/" + repositoryPath
	return identity, identity, nil
}

func normalizeSourceRepositories(repositories []string) ([]string, map[string]struct{}, error) {
	if len(repositories) > 32 {
		return nil, nil, NewValidationError("构建事件包含过多代码仓库")
	}
	cleaned := make([]string, 0, len(repositories))
	identities := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		if strings.TrimSpace(repository) == "" {
			continue
		}
		display, identity, err := sourceRepositoryIdentity(repository)
		if err != nil {
			return nil, nil, NewValidationError("构建事件包含无效或带凭据的代码仓库地址")
		}
		if _, exists := identities[identity]; exists {
			continue
		}
		identities[identity] = struct{}{}
		cleaned = append(cleaned, display)
	}
	return cleaned, identities, nil
}
