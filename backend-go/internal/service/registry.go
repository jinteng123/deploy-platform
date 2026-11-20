package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
)

// manifestAccept covers every manifest shape a registry may answer with:
// single-arch v2, OCI image/index, and manifest lists.
const manifestAccept = "application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json"

var bearerChallengeRe = regexp.MustCompile(`(?i)^Bearer\s+`)

// ResolveRegistryDigest asks the registry's v2 API for the content digest
// of a tagged reference ("host/repo/name:tag"). The answer is the
// Docker-Content-Digest header — bytes the cluster will actually pull.
//
// Callers decide policy on error; the pipeline treats "cannot resolve our
// own registry" as a deploy-blocking failure since shipping the tag would
// silently skip byte-identity verification.
func ResolveRegistryDigest(ctx context.Context, cfg config.Config, image string) (string, error) {
	return resolveRegistryDigestWithClient(ctx, cfg, image, registryHTTPClient(15*time.Second))
}

func resolveRegistryDigestWithClient(ctx context.Context, cfg config.Config, image string, client *http.Client) (string, error) {
	host, repoTag, ok := splitImageRef(image)
	if !ok {
		return "", fmt.Errorf("无法解析镜像引用: %s", image)
	}
	scheme, configuredHost, configured := configuredRegistryEndpoint(cfg)
	if !configured || !strings.EqualFold(host, configuredHost) {
		return "", fmt.Errorf("只允许解析配置 registry 的镜像")
	}
	host = configuredHost
	i := strings.LastIndex(repoTag, ":")
	repo, tag := repoTag[:i], repoTag[i+1:]

	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, tag)
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	digest, challenge, err := tryManifestWithClient(hctx, client, url, func(req *http.Request) {
		if scheme == "https" && cfg.RegistryUser != "" {
			req.SetBasicAuth(cfg.RegistryUser, cfg.RegistryPassword)
		}
	})
	if err == nil {
		return digest, nil
	}
	if challenge == "" || !bearerChallengeRe.MatchString(challenge) {
		return "", err
	}
	token, terr := fetchRegistryToken(hctx, challenge, cfg, repo, scheme, host, client)
	if terr != nil {
		return "", fmt.Errorf("registry token 获取失败: %w", terr)
	}
	digest, _, err = tryManifestWithClient(hctx, client, url, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	})
	return digest, err
}

// splitImageRef splits "host/path/name:tag" → host, "path/name:tag". A ref
// without a "." or ":" or "localhost" in its first segment has no registry
// host (docker-hub short form) and is rejected — we only resolve the
// configured registry anyway.
func splitImageRef(image string) (host, repoTag string, ok bool) {
	image = strings.TrimSpace(image)
	if strings.Contains(image, "@") {
		return "", "", false
	}
	i := strings.Index(image, "/")
	if i <= 0 {
		return "", "", false
	}
	host = image[:i]
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "", "", false
	}
	repoTag = image[i+1:]
	last := repoTag[strings.LastIndex(repoTag, "/")+1:]
	if !strings.Contains(last, ":") {
		return "", "", false
	}
	return host, repoTag, true
}

// ImageRegistryHost returns the registry host portion of an image ref, or
// "" for docker-hub short form / digest refs.
func ImageRegistryHost(image string) string {
	host, _, ok := splitImageRef(image)
	if !ok {
		return ""
	}
	host = strings.TrimPrefix(host, "http://")
	return strings.TrimPrefix(host, "https://")
}

func tryManifest(ctx context.Context, url string, setAuth func(*http.Request)) (digest, challenge string, err error) {
	return tryManifestWithClient(ctx, registryHTTPClient(15*time.Second), url, setAuth)
}

func tryManifestWithClient(ctx context.Context, client *http.Client, url string, setAuth func(*http.Request)) (digest, challenge string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", manifestAccept)
	if setAuth != nil {
		setAuth(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		d := resp.Header.Get("Docker-Content-Digest")
		if d == "" {
			return "", "", fmt.Errorf("registry 未返回 Docker-Content-Digest")
		}
		return d, "", nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", resp.Header.Get("WWW-Authenticate"), fmt.Errorf("registry 认证失败 (401)")
	}
	return "", "", fmt.Errorf("registry 查询镜像失败: HTTP %d", resp.StatusCode)
}

// fetchRegistryToken completes the v2 Bearer token dance:
// WWW-Authenticate: Bearer realm="…",service="…",scope="repository:repo:pull"
func fetchRegistryToken(ctx context.Context, challenge string, cfg config.Config, repo, registryScheme, registryHost string, client *http.Client) (string, error) {
	params := map[string]string{}
	for _, kv := range strings.Split(bearerChallengeRe.ReplaceAllString(challenge, ""), ",") {
		kv = strings.TrimSpace(kv)
		if i := strings.Index(kv, "="); i > 0 {
			params[kv[:i]] = strings.Trim(kv[i+1:], `"`)
		}
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("Bearer challenge 缺少 realm")
	}
	if registryScheme != "https" {
		return "", fmt.Errorf("明文 HTTP registry 不支持 Bearer token 核验")
	}
	realmURL, err := url.Parse(realm)
	if err != nil || realmURL.Scheme != "https" || realmURL.Host == "" || realmURL.User != nil || realmURL.Fragment != "" {
		return "", fmt.Errorf("Bearer token realm 必须是无凭据的 HTTPS URL")
	}
	if !registryAuthHostAllowed(cfg, registryHost, realmURL.Host) {
		return "", fmt.Errorf("Bearer token realm 主机未加入 REGISTRY_AUTH_HOSTS 信任列表")
	}
	if client == nil {
		client = registryHTTPClient(15 * time.Second)
	}
	// Always request the least privilege needed by image resolution. Do not
	// accept a challenge scope that expands access beyond repository pull.
	scope := "repository:" + repo + ":pull"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realmURL.String(), nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", scope)
	req.URL.RawQuery = q.Encode()
	if cfg.RegistryUser != "" {
		req.SetBasicAuth(cfg.RegistryUser, cfg.RegistryPassword)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token 服务返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("token 服务响应超过大小上限")
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("token 服务未返回 token")
}

func registryHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func registryAuthHostAllowed(cfg config.Config, registryHost, realmHost string) bool {
	if strings.EqualFold(registryHost, realmHost) {
		return true
	}
	for _, allowed := range cfg.RegistryAuthHosts {
		allowed = strings.TrimSpace(allowed)
		if strings.HasPrefix(strings.ToLower(allowed), "https://") {
			allowed = allowed[len("https://"):]
		}
		if allowed != "" && !strings.ContainsAny(allowed, "/?#@") && strings.EqualFold(allowed, realmHost) {
			return true
		}
	}
	return false
}
