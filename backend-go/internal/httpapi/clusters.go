package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/tools/clientcmd"
)

type clusterBody struct {
	Name                  string  `json:"name" binding:"required"`
	EnvCode               string  `json:"env_code" binding:"required"`
	Environment           string  `json:"environment" binding:"required"`
	EnvironmentTier       string  `json:"environment_tier"`
	DeployPolicy          string  `json:"deploy_policy"`
	ConnectionType        string  `json:"connection_type"`
	APIServer             *string `json:"api_server"`
	Credential            *string `json:"credential"`
	CACert                *string `json:"ca_cert"`
	InsecureSkipTLSVerify *bool   `json:"insecure_skip_tls_verify"`
	Enabled               *bool   `json:"enabled"`
	DefaultNamespace      string  `json:"default_namespace"`
	Description           *string `json:"description"`
	// legacy SSH fields, only meaningful for connection_type=legacy_ssh
	K8sMasterHost    string  `json:"k8s_master_host"`
	K8sMasterPort    *int    `json:"k8s_master_port"`
	K8sMasterUser    string  `json:"k8s_master_user"`
	K8sMasterKeyPath *string `json:"k8s_master_key_path"`
}

func (b *clusterBody) normalize() {
	b.Name = strings.TrimSpace(b.Name)
	b.EnvCode = strings.TrimSpace(b.EnvCode)
	b.Environment = strings.TrimSpace(b.Environment)
	b.EnvironmentTier = strings.TrimSpace(b.EnvironmentTier)
	b.DeployPolicy = strings.TrimSpace(b.DeployPolicy)
	b.ConnectionType = strings.TrimSpace(b.ConnectionType)
	b.APIServer = trimPtr(b.APIServer)
	b.Credential = trimPtr(b.Credential)
	b.CACert = trimPtr(b.CACert)
	b.DefaultNamespace = strings.TrimSpace(b.DefaultNamespace)
	b.Description = trimPtr(b.Description)
	b.K8sMasterHost = strings.TrimSpace(b.K8sMasterHost)
	b.K8sMasterUser = strings.TrimSpace(b.K8sMasterUser)
	b.K8sMasterKeyPath = trimPtr(b.K8sMasterKeyPath)
}

// clusterOut is the only shape ever serialized for a cluster — ciphertext
// and plaintext credentials never leave the process.
type clusterOut struct {
	ID                    int     `json:"id"`
	Name                  string  `json:"name"`
	EnvCode               string  `json:"env_code"`
	Environment           string  `json:"environment"`
	EnvironmentTier       string  `json:"environment_tier"`
	DeployPolicy          string  `json:"deploy_policy"`
	ConnectionType        string  `json:"connection_type"`
	APIServer             *string `json:"api_server"`
	CredentialConfigured  bool    `json:"credential_configured"`
	CACert                *string `json:"ca_cert"`
	InsecureSkipTLSVerify bool    `json:"insecure_skip_tls_verify"`
	Enabled               bool    `json:"enabled"`
	K8sMasterHost         string  `json:"k8s_master_host,omitempty"`
	K8sMasterPort         int     `json:"k8s_master_port,omitempty"`
	K8sMasterUser         string  `json:"k8s_master_user,omitempty"`
	DefaultNamespace      string  `json:"default_namespace"`
	Description           *string `json:"description"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
}

func toClusterOut(c service.Cluster) clusterOut {
	return clusterOut{
		ID:                    c.ID,
		Name:                  c.Name,
		EnvCode:               c.EnvCode,
		Environment:           c.Environment,
		EnvironmentTier:       c.EnvironmentTier,
		DeployPolicy:          c.DeployPolicy,
		ConnectionType:        c.ConnectionType,
		APIServer:             c.APIServer,
		CredentialConfigured:  c.CredentialCiphertext != nil && *c.CredentialCiphertext != "",
		CACert:                c.CACert,
		InsecureSkipTLSVerify: c.InsecureSkipTLSVerify,
		Enabled:               c.Enabled,
		K8sMasterHost:         c.K8sMasterHost,
		K8sMasterPort:         c.K8sMasterPort,
		K8sMasterUser:         c.K8sMasterUser,
		DefaultNamespace:      c.DefaultNamespace,
		Description:           c.Description,
		CreatedAt:             fmtTime(c.CreatedAt),
		UpdatedAt:             fmtTime(c.UpdatedAt),
	}
}

// envCodePattern matches the DB-facing env_code contract (VARCHAR(16)).
var envCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$`)

// maxCredentialBytes bounds credential/CA payloads against memory abuse.
const maxCredentialBytes = 1 << 20 // 1 MiB

func limitBody(c *gin.Context, n int64) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
}

// validateKubeconfigCredential accepts only self-contained kubeconfigs:
// embedded token / client-certificate-data / client-key-data /
// certificate-authority-data. Exec plugins, auth providers and references
// to files on the service host (client-certificate, client-key,
// tokenFile, certificate-authority paths) are rejected. Never echoes
// credential content in errors.
func validateKubeconfigCredential(plain string) error {
	parsed, err := clientcmd.Load([]byte(plain))
	if err != nil {
		return errors.New("kubeconfig 解析失败")
	}
	for _, auth := range parsed.AuthInfos {
		if auth.Exec != nil || auth.AuthProvider != nil {
			return errors.New("kubeconfig 不允许 exec 插件或 auth-provider，请使用内嵌凭据")
		}
		if auth.ClientCertificate != "" || auth.ClientKey != "" || auth.TokenFile != "" {
			return errors.New("kubeconfig 不允许引用本地文件凭据，请使用内嵌 data 字段")
		}
	}
	for _, cl := range parsed.Clusters {
		if cl.CertificateAuthority != "" {
			return errors.New("kubeconfig 不允许 certificate-authority 路径，请使用 certificate-authority-data")
		}
	}
	if _, err := clientcmd.RESTConfigFromKubeConfig([]byte(plain)); err != nil {
		return errors.New("kubeconfig 语法校验失败")
	}
	return nil
}

// toCluster validates the body and materializes a Cluster. For updates
// (existing != nil) omitted fields preserve the stored values: enabled,
// insecure_skip_tls_verify, environment_tier, deploy_policy,
// connection_type, api_server, ca_cert, credential and SSH key path. A
// connection_type change always requires a fresh credential — ciphertext
// from another type is never reused.
// testOnly keeps the credential in PlainCredential instead of encrypting it —
// the test-connection endpoint never persists.
func (b *clusterBody) toCluster(id int, existing *service.Cluster, cipher *service.CredentialCipher, testOnly bool) (service.Cluster, string) {
	if b.Name == "" || utf8.RuneCountInString(b.Name) > 64 {
		return service.Cluster{}, "name 必填且不超过 64 字符"
	}
	if !envCodePattern.MatchString(b.EnvCode) {
		return service.Cluster{}, "env_code 必填，须匹配 ^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$"
	}
	if b.Environment == "" || utf8.RuneCountInString(b.Environment) > 64 {
		return service.Cluster{}, "environment 必填且不超过 64 字符"
	}
	if b.Description != nil && utf8.RuneCountInString(*b.Description) > 256 {
		return service.Cluster{}, "description 不超过 256 字符"
	}
	if b.DefaultNamespace != "" && utf8.RuneCountInString(b.DefaultNamespace) > 64 {
		return service.Cluster{}, "default_namespace 不超过 64 字符"
	}
	if b.Credential != nil && len(*b.Credential) > maxCredentialBytes {
		return service.Cluster{}, "credential 不超过 1 MiB"
	}
	if b.CACert != nil && len(*b.CACert) > maxCredentialBytes {
		return service.Cluster{}, "ca_cert 不超过 1 MiB"
	}
	tier := b.EnvironmentTier
	if tier == "" {
		if existing != nil {
			tier = existing.EnvironmentTier
		} else {
			tier = "non_production"
		}
	}
	if tier != "non_production" && tier != "production" {
		return service.Cluster{}, "environment_tier 只允许 non_production|production"
	}
	policy := b.DeployPolicy
	if policy == "" {
		if existing != nil {
			policy = existing.DeployPolicy
		} else {
			policy = "manual"
		}
	}
	if policy != "automatic" && policy != "manual" {
		return service.Cluster{}, "deploy_policy 只允许 automatic|manual"
	}
	if tier == "production" && policy == "automatic" {
		return service.Cluster{}, "生产集群必须 manual 发布策略"
	}
	connType := b.ConnectionType
	if connType == "" {
		if existing != nil {
			connType = existing.ConnectionType
		} else {
			connType = "kubeconfig"
		}
	}
	connChanged := existing != nil && connType != existing.ConnectionType
	insecure := false
	if b.InsecureSkipTLSVerify != nil {
		insecure = *b.InsecureSkipTLSVerify
	} else if existing != nil {
		insecure = existing.InsecureSkipTLSVerify
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}
	ns := b.DefaultNamespace
	if ns == "" {
		if existing != nil && existing.DefaultNamespace != "" {
			ns = existing.DefaultNamespace
		} else {
			ns = "default"
		}
	}
	c := service.Cluster{
		ID:                    id,
		Name:                  b.Name,
		EnvCode:               b.EnvCode,
		Environment:           b.Environment,
		EnvironmentTier:       tier,
		DeployPolicy:          policy,
		ConnectionType:        connType,
		InsecureSkipTLSVerify: insecure,
		Enabled:               enabled,
		DefaultNamespace:      ns,
		Description:           b.Description,
	}

	switch connType {
	case "kubeconfig":
		c.K8sMasterPort = 22 // inert defaults; direct connections ignore them
		if b.Credential == nil {
			if existing != nil && !connChanged {
				c.CredentialCiphertext = existing.CredentialCiphertext
			} else if existing != nil {
				return service.Cluster{}, "切换连接类型必须提供新 credential"
			} else {
				return service.Cluster{}, "kubeconfig 连接必须提供 credential"
			}
		} else {
			if err := validateKubeconfigCredential(*b.Credential); err != nil {
				return service.Cluster{}, err.Error()
			}
			if msg := sealCredential(&c, *b.Credential, cipher, testOnly); msg != "" {
				return service.Cluster{}, msg
			}
		}
	case "token":
		apiServer := b.APIServer
		if (apiServer == nil || *apiServer == "") && existing != nil && !connChanged {
			apiServer = existing.APIServer
		}
		if apiServer == nil || *apiServer == "" {
			return service.Cluster{}, "token 连接必须提供 api_server"
		}
		if utf8.RuneCountInString(*apiServer) > 512 {
			return service.Cluster{}, "api_server 不超过 512 字符"
		}
		u, err := url.Parse(*apiServer)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return service.Cluster{}, "api_server 必须是合法的 http/https URL（不含用户信息/查询/片段）"
		}
		c.APIServer = apiServer
		c.CACert = b.CACert
		if c.CACert == nil && existing != nil && !connChanged {
			c.CACert = existing.CACert
		}
		c.K8sMasterPort = 22
		if b.Credential == nil {
			if existing != nil && !connChanged {
				c.CredentialCiphertext = existing.CredentialCiphertext
			} else if existing != nil {
				return service.Cluster{}, "切换连接类型必须提供新 credential"
			} else {
				return service.Cluster{}, "token 连接必须提供 credential"
			}
		} else {
			if msg := sealCredential(&c, *b.Credential, cipher, testOnly); msg != "" {
				return service.Cluster{}, msg
			}
		}
	case "legacy_ssh":
		host := b.K8sMasterHost
		if host == "" && existing != nil {
			host = existing.K8sMasterHost
		}
		if host == "" {
			return service.Cluster{}, "k8s_master_host 必填"
		}
		if utf8.RuneCountInString(host) > 128 {
			return service.Cluster{}, "k8s_master_host 不超过 128 字符"
		}
		port := 22
		if b.K8sMasterPort != nil {
			if *b.K8sMasterPort < 1 || *b.K8sMasterPort > 65535 {
				return service.Cluster{}, "k8s_master_port 必须在 1-65535 之间"
			}
			port = *b.K8sMasterPort
		} else if existing != nil && existing.K8sMasterPort > 0 {
			port = existing.K8sMasterPort
		}
		user := b.K8sMasterUser
		if user == "" {
			if existing != nil && existing.K8sMasterUser != "" {
				user = existing.K8sMasterUser
			} else {
				user = "root"
			}
		}
		if utf8.RuneCountInString(user) > 64 {
			return service.Cluster{}, "k8s_master_user 不超过 64 字符"
		}
		keyPath := b.K8sMasterKeyPath
		if keyPath == nil && existing != nil {
			keyPath = existing.K8sMasterKeyPath
		}
		if keyPath != nil && utf8.RuneCountInString(*keyPath) > 256 {
			return service.Cluster{}, "k8s_master_key_path 不超过 256 字符"
		}
		c.K8sMasterHost = host
		c.K8sMasterPort = port
		c.K8sMasterUser = user
		c.K8sMasterKeyPath = keyPath
	default:
		return service.Cluster{}, "connection_type 只允许 kubeconfig|token|legacy_ssh"
	}
	// An enabled direct-connection cluster must be decryptable on next boot:
	// refuse to persist one without a configured cipher, even when the
	// credential is merely being preserved.
	if !testOnly && c.Enabled && cipher == nil &&
		(c.ConnectionType == "kubeconfig" || c.ConnectionType == "token") {
		return service.Cluster{}, "未配置 CREDENTIAL_ENCRYPTION_KEY，无法保存启用的直连集群"
	}
	return c, ""
}

func sealCredential(c *service.Cluster, plain string, cipher *service.CredentialCipher, testOnly bool) string {
	if testOnly {
		c.PlainCredential = plain
		return ""
	}
	if cipher == nil {
		return "未配置 CREDENTIAL_ENCRYPTION_KEY，无法保存凭据"
	}
	sealed, err := cipher.Encrypt(plain)
	if err != nil {
		return "凭据加密失败"
	}
	c.CredentialCiphertext = &sealed
	return ""
}

func registerClusterAPI(api *gin.RouterGroup, store *service.Store, k8s *service.K8sService, cipher *service.CredentialCipher) {
	api.GET("/clusters", func(c *gin.Context) {
		out := make([]clusterOut, 0)
		for _, cl := range store.Clusters() {
			out = append(out, toClusterOut(cl))
		}
		c.JSON(http.StatusOK, out)
	})

	api.POST("/clusters", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		limitBody(c, 3<<20)
		var b clusterBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		cluster, msg := b.toCluster(0, nil, cipher, false)
		if msg != "" {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		saved, err := store.SaveCluster(cluster)
		audit(c, store, "cluster_create", saved.ID, "", "clusters", cluster.Name, cluster.ConnectionType, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, toClusterOut(saved))
	})

	api.POST("/clusters/test-connection", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		limitBody(c, 3<<20)
		var b clusterBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		cluster, msg := b.toCluster(0, nil, cipher, true)
		if msg != "" {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		version, err := k8s.TestConnection(c.Request.Context(), cluster)
		// Credential-bearing probe — audited, but detail carries only the
		// connection type; credential material never reaches the record.
		audit(c, store, "cluster_test", cluster.ID, "", "clusters", cluster.Name, cluster.ConnectionType, auditErr(err))
		if err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "version": version})
	})

	api.PUT("/clusters/:id", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		existing, err := store.FindCluster(id)
		if err != nil {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		limitBody(c, 3<<20)
		var b clusterBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		cluster, msg := b.toCluster(id, &existing, cipher, false)
		if msg != "" {
			fail(c, http.StatusBadRequest, msg)
			return
		}
		saved, err := store.UpdateCluster(cluster)
		audit(c, store, "cluster_update", id, "", "clusters", cluster.Name, cluster.ConnectionType, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		// UpdatedAt just bumped → cached clients keyed on the old
		// credential version must go, and live streams die with them.
		k8s.InvalidateCluster(id)
		c.JSON(http.StatusOK, toClusterOut(saved))
	})

	api.DELETE("/clusters/:id", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		cluster, err := store.FindCluster(id)
		if err != nil {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		err = store.DeleteCluster(id)
		audit(c, store, "cluster_delete", id, "", "clusters", cluster.Name, "", auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		k8s.InvalidateCluster(id)
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("集群 %s 已删除", cluster.Name)})
	})
}

// writeStoreErr maps store errors: conflict → 409, not-found → 404,
// validation → 400, else 500.
func writeStoreErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrConflict):
		fail(c, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrNotFound):
		fail(c, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrValidation):
		fail(c, http.StatusBadRequest, err.Error())
	default:
		fail(c, http.StatusInternalServerError, err.Error())
	}
}
