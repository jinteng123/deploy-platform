package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
)

const (
	registryIndexMaxManifestBytes = 4 << 20
	registryIndexMaxDescriptors   = 128
	registryIndexMaxDepth         = 4
)

// registryIndexContainsDigest proves that runtimeDigest is a descriptor in
// the immutable manifest graph rooted at expectedImage. It only contacts the
// explicitly configured registry, disables redirects, verifies every fetched
// manifest's bytes against its requested sha256 digest, and never turns lookup
// failures into a positive observation.
func registryIndexContainsDigest(ctx context.Context, cfg config.Config, expectedImage, runtimeDigest string) (bool, error) {
	return registryIndexContainsDigestWithClient(ctx, cfg, expectedImage, runtimeDigest, registryHTTPClient(4*time.Second))
}

func registryIndexContainsDigestWithClient(ctx context.Context, cfg config.Config, expectedImage, runtimeDigest string, client *http.Client) (bool, error) {
	host, repo, rootDigest, scheme, ok := configuredPinnedImage(cfg, expectedImage)
	if !ok {
		return false, fmt.Errorf("镜像不属于配置的 registry，跳过 index 核验")
	}
	target := NormalizeDigest(runtimeDigest)
	if target == "" || rootDigest == "" {
		return false, fmt.Errorf("index 或运行时摘要不是有效的 sha256 digest")
	}
	if target == rootDigest {
		return true, nil
	}

	type pendingIndex struct {
		digest string
		depth  int
	}
	queue := []pendingIndex{{digest: rootDigest}}
	visited := make(map[string]bool)
	descriptorCount := 0
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if visited[item.digest] {
			continue
		}
		visited[item.digest] = true
		if item.depth > registryIndexMaxDepth {
			return false, fmt.Errorf("OCI index 嵌套超过核验上限")
		}
		manifest, err := fetchPinnedManifest(ctx, client, cfg, scheme, host, repo, item.digest)
		if err != nil {
			return false, err
		}
		if !isManifestIndex(manifest.MediaType) {
			if item.depth == 0 {
				return false, fmt.Errorf("发布目标 digest 对应的不是 OCI index 或 Docker manifest list")
			}
			continue
		}
		for _, descriptor := range manifest.Manifests {
			digest := NormalizeDigest(descriptor.Digest)
			if digest == "" {
				return false, fmt.Errorf("OCI index 含有无效的 manifest digest")
			}
			descriptorCount++
			if descriptorCount > registryIndexMaxDescriptors {
				return false, fmt.Errorf("OCI index 描述符数量超过核验上限")
			}
			if digest == target {
				return true, nil
			}
			if isManifestIndex(descriptor.MediaType) {
				queue = append(queue, pendingIndex{digest: digest, depth: item.depth + 1})
			}
		}
	}
	return false, nil
}

type registryManifestIndex struct {
	MediaType string `json:"mediaType"`
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"manifests"`
}

func fetchPinnedManifest(ctx context.Context, client *http.Client, cfg config.Config, scheme, host, repo, digest string) (registryManifestIndex, error) {
	path := "/v2/"
	for i, segment := range strings.Split(repo, "/") {
		if i > 0 {
			path += "/"
		}
		path += url.PathEscape(segment)
	}
	path += "/manifests/" + url.PathEscape(digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+path, nil)
	if err != nil {
		return registryManifestIndex{}, err
	}
	req.Header.Set("Accept", manifestAccept)
	req.Header.Set("Accept-Encoding", "identity")
	if scheme == "https" && cfg.RegistryUser != "" {
		// This request is guaranteed by configuredPinnedImage to target the exact
		// configured registry host, and the client refuses redirects. Never send
		// configured credentials over an explicitly configured plaintext registry.
		req.SetBasicAuth(cfg.RegistryUser, cfg.RegistryPassword)
	}
	resp, err := client.Do(req)
	if err != nil {
		return registryManifestIndex{}, fmt.Errorf("读取 registry manifest 失败: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if !bearerChallengeRe.MatchString(challenge) {
			return registryManifestIndex{}, fmt.Errorf("registry manifest 认证失败 (401)")
		}
		token, tokenErr := fetchRegistryToken(ctx, challenge, cfg, repo, scheme, host, client)
		if tokenErr != nil {
			return registryManifestIndex{}, fmt.Errorf("registry token 获取失败: %w", tokenErr)
		}
		retry := req.Clone(ctx)
		retry.Header.Set("Authorization", "Bearer "+token)
		resp, err = client.Do(retry)
		if err != nil {
			return registryManifestIndex{}, fmt.Errorf("使用 registry token 读取 manifest 失败: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return registryManifestIndex{}, fmt.Errorf("registry manifest 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, registryIndexMaxManifestBytes+1))
	if err != nil {
		return registryManifestIndex{}, fmt.Errorf("读取 registry manifest 正文失败: %w", err)
	}
	if len(body) > registryIndexMaxManifestBytes {
		return registryManifestIndex{}, fmt.Errorf("registry manifest 超过大小上限")
	}
	hash := sha256.Sum256(body)
	actualDigest := "sha256:" + hex.EncodeToString(hash[:])
	if actualDigest != digest {
		return registryManifestIndex{}, fmt.Errorf("registry manifest 正文摘要与请求 digest 不一致")
	}
	if headerDigest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest")); headerDigest != "" && NormalizeDigest(headerDigest) != digest {
		return registryManifestIndex{}, fmt.Errorf("registry 返回的 Docker-Content-Digest 与请求 digest 不一致")
	}
	var manifest registryManifestIndex
	if err := json.Unmarshal(body, &manifest); err != nil {
		return registryManifestIndex{}, fmt.Errorf("registry manifest JSON 无效: %w", err)
	}
	if manifest.MediaType == "" {
		manifest.MediaType = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	}
	return manifest, nil
}

func configuredPinnedImage(cfg config.Config, image string) (host, repo, digest, scheme string, ok bool) {
	image = strings.TrimSpace(image)
	if strings.ContainsAny(image, " \t\r\n?#") || strings.Count(image, "@") != 1 {
		return "", "", "", "", false
	}
	parts := strings.SplitN(image, "@", 2)
	digest = NormalizeDigest(parts[1])
	if digest == "" {
		return "", "", "", "", false
	}
	imageHost, imageRepo, found := strings.Cut(parts[0], "/")
	if !found || imageRepo == "" {
		return "", "", "", "", false
	}
	for _, segment := range strings.Split(imageRepo, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", "", "", false
		}
		for _, char := range segment {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
				return "", "", "", "", false
			}
		}
	}
	var configured string
	scheme, configured, found = configuredRegistryEndpoint(cfg)
	if !found || !strings.EqualFold(configured, imageHost) {
		return "", "", "", "", false
	}
	return configured, imageRepo, digest, scheme, true
}

func configuredRegistryEndpoint(cfg config.Config) (scheme, host string, ok bool) {
	configured := strings.TrimSpace(cfg.RegistryHost)
	scheme = "https"
	if strings.HasPrefix(strings.ToLower(configured), "http://") {
		scheme = "http"
		configured = configured[len("http://"):]
	} else if strings.HasPrefix(strings.ToLower(configured), "https://") {
		configured = configured[len("https://"):]
	}
	if configured == "" || strings.ContainsAny(configured, "/?#@ \t\r\n") {
		return "", "", false
	}
	return scheme, configured, true
}

func isManifestIndex(mediaType string) bool {
	switch strings.TrimSpace(strings.Split(mediaType, ";")[0]) {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		return true
	default:
		return false
	}
}
