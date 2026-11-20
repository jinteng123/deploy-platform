// Package e2e runs the playbook's end-to-end sequence against the REAL
// platform binary and the local Kind cluster — MOCK_RUNTIME=false, never
// a mock stand-in. It is opt-in: `KB_E2E=1 go test ./e2e/ -v`.
//
// Environment contract:
//   - Docker and a local Kind context; MySQL runs in a disposable tmpfs container
//     (user deploy/deploy-platform); each run creates a random database and
//     drops only that database, so existing dev data is never reset.
//   - kubectl's current cluster must resolve to a local Kind cluster.
//   - All K8s objects live in a random per-run namespace, deleted on cleanup;
//     nothing outside that namespace is created or removed.
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
)

const e2ePassword = "e2e-admin-pass"

var (
	e2eNS                string
	e2eDB                string
	e2eMySQLRootPassword = "deploy-platform"
	e2eMySQLContainer    = "deploy-platform-mysql"
	e2eMySQLAddress      = "127.0.0.1:3306"
)

// e2eAddr is bound at runtime to a free port — a fixed port can collide
// with a leftover port-forward or another dev process, and a colliding
// server that happens to answer /health would silently pass as ours.
var e2eAddr string

var httpClient *http.Client

func TestMain(m *testing.M) {
	jar, _ := cookiejar.New(nil)
	httpClient = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	os.Exit(m.Run())
}

func skipUnlessE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("KB_E2E") != "1" {
		t.Skip("KB_E2E != 1 — skipping real-cluster E2E")
	}
}

func e2eRunID(t *testing.T) string {
	t.Helper()
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate isolated e2e namespace: %v", err)
	}
	return hex.EncodeToString(raw[:])
}

func skipUnlessLocalKindContext(t *testing.T) {
	t.Helper()
	clusterOut, err := exec.Command("kubectl", "config", "view", "--minify", "-o", "jsonpath={.contexts[0].context.cluster}").CombinedOutput()
	if err != nil {
		t.Skipf("kubectl current cluster unavailable: %v\n%s", err, clusterOut)
	}
	clusterRef := strings.TrimSpace(string(clusterOut))
	if !strings.HasPrefix(clusterRef, "kind-") {
		t.Skipf("current Kubernetes cluster %q is not a Kind cluster", clusterRef)
	}
	want := strings.TrimPrefix(clusterRef, "kind-")
	kindOut, err := exec.Command("kind", "get", "clusters").CombinedOutput()
	if err != nil {
		t.Skipf("local Kind cluster list unavailable: %v\n%s", err, kindOut)
	}
	for _, name := range strings.Fields(string(kindOut)) {
		if name == want {
			return
		}
	}
	t.Skipf("current Kubernetes cluster %q does not resolve to a local Kind cluster", clusterRef)
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// api performs one JSON HTTP call and returns (status, parsed-body).
func api(t *testing.T, method, path string, payload any) (int, map[string]any) {
	return apiWithBearer(t, method, path, payload, "")
}

func apiWithBearer(t *testing.T, method, path string, payload any, token string) (int, map[string]any) {
	t.Helper()
	code, out, err := apiRequest(method, path, payload, token)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return code, out
}

func apiRequest(method, path string, payload any, token string) (int, map[string]any, error) {
	var rd io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e2eAddr+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, nil
}

// apiList fetches a resource list endpoint ({items:[...]}).
func apiList(t *testing.T, path string) []any {
	t.Helper()
	resp, err := httpClient.Get(e2eAddr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out struct {
		Items []any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Items
}

func apiArray(t *testing.T, path string) []any {
	t.Helper()
	resp, err := httpClient.Get(e2eAddr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out []any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode GET %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d", path, resp.StatusCode)
	}
	return out
}

// applySchema creates a unique e2e database. The replay itself goes through
// scripts/apply-schema.sh, the single implementation of "schema.sql +
// migrations/*.sql with the USE line rewritten". Cleanup drops only the
// database created by this test run.
func applySchema(t *testing.T) {
	t.Helper()
	create := exec.Command("docker", "exec", "-i", e2eMySQLContainer,
		"mysql", "-uroot", "-p"+e2eMySQLRootPassword)
	create.Stdin = strings.NewReader("CREATE DATABASE IF NOT EXISTS " + e2eDB + " CHARACTER SET utf8mb4; CREATE USER IF NOT EXISTS 'deploy'@'%' IDENTIFIED BY 'deploy-platform';")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create e2e db: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		revoke := exec.Command("docker", "exec", "-i", e2eMySQLContainer,
			"mysql", "-uroot", "-p"+e2eMySQLRootPassword)
		revoke.Stdin = strings.NewReader("REVOKE ALL PRIVILEGES ON `" + e2eDB + "`.* FROM 'deploy'@'%';")
		if out, err := revoke.CombinedOutput(); err != nil {
			t.Errorf("revoke e2e db grant %s: %v\n%s", e2eDB, err, out)
		}
		drop := exec.Command("docker", "exec", "-i", e2eMySQLContainer,
			"mysql", "-uroot", "-p"+e2eMySQLRootPassword)
		drop.Stdin = strings.NewReader("DROP DATABASE IF EXISTS `" + e2eDB + "`;")
		if out, err := drop.CombinedOutput(); err != nil {
			t.Errorf("drop e2e db %s: %v\n%s", e2eDB, err, out)
		}
	})
	grant := exec.Command("docker", "exec", "-i", e2eMySQLContainer,
		"mysql", "-uroot", "-p"+e2eMySQLRootPassword)
	grant.Stdin = strings.NewReader("GRANT ALL PRIVILEGES ON `" + e2eDB + "`.* TO 'deploy'@'%';")
	if out, err := grant.CombinedOutput(); err != nil {
		t.Fatalf("grant e2e db access: %v\n%s", err, out)
	}
	script := filepath.Join(backendRoot(t), "..", "scripts", "apply-schema.sh")
	command := exec.Command("bash", script, e2eDB)
	command.Env = append(os.Environ(), "MYSQL_CMD=docker exec -i "+e2eMySQLContainer+" mysql -uroot -p"+e2eMySQLRootPassword)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("apply schema: %v\n%s", err, out)
	}
}

// verifyResourceLockContention checks the database mutex through two
// independent repository pools. This proves lock ownership is shared beyond
// one in-memory Store while keeping all rows inside this run's disposable DB.
func verifyResourceLockContention(t *testing.T) {
	t.Helper()
	dsn := "deploy:deploy-platform@tcp(" + e2eMySQLAddress + ")/" + e2eDB + "?parseTime=true&charset=utf8mb4"
	first, err := repository.Open(dsn)
	if err != nil {
		t.Fatalf("open first resource-lock repository: %v", err)
	}
	second, err := repository.Open(dsn)
	if err != nil {
		_ = first.Close()
		t.Fatalf("open second resource-lock repository: %v", err)
	}
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	name := "e2e-race-" + e2eRunID(t)
	testTier := "non_production"
	lock := service.ResourceLock{ClusterID: 1, Namespace: "e2e-lock-test", Kind: "deployment", Name: name}
	type result struct {
		log service.DeployLog
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, repo := range []*repository.MySQL{first, second} {
		repo := repo
		go func() {
			<-start
			log, err := repo.CreateDeployLog(service.DeployLog{
				ClusterID: 1, AppName: name, DeploymentName: name,
				ContainerName: "app", Namespace: lock.Namespace,
				Version: "e2e", FileName: "cross-connection-lock-test",
				DeployType: "image_deploy", Status: "pending", EnvironmentTierSnapshot: &testTier,
			}, []service.ResourceLock{lock})
			results <- result{log: log, err: err}
		}()
	}
	close(start)

	var winner service.DeployLog
	successes, conflicts := 0, 0
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			successes++
			winner = got.log
		case errors.Is(got.err, service.ErrConflict):
			conflicts++
		default:
			t.Fatalf("resource-lock race returned unexpected error: %v", got.err)
		}
	}
	if successes != 1 || conflicts != 1 || winner.ID == 0 {
		t.Fatalf("resource-lock race successes=%d conflicts=%d winner=%+v; want exactly one owner", successes, conflicts, winner)
	}
	logs, err := first.Logs()
	if err != nil {
		t.Fatalf("read isolated resource-lock test rows: %v", err)
	}
	if len(logs) != 1 || logs[0].ID != winner.ID {
		t.Fatalf("losing lock transaction left an unexpected deploy log: got %d rows, winner=%d", len(logs), winner.ID)
	}
	finished := time.Now()
	winner.Status, winner.FinishedAt = "failed", &finished
	if err := first.UpdateLogExecution(winner); err != nil {
		t.Fatalf("release isolated resource-lock test row: %v", err)
	}
}

func backendRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd) // e2e/ sits directly under backend-go/
}

type e2eServer struct {
	bin     string
	root    string
	logPath string
	env     []string
	cmd     *exec.Cmd
	logFile *os.File
	mysql   *e2eMySQLFaultProxy
	kubeAPI *e2eKubeAPIProxy
}

type e2eKubeAPIProxy struct {
	server       *httptest.Server
	proxy        *httputil.ReverseProxy
	mu           sync.Mutex
	targetMethod string
	targetPath   string
	armed        bool
	writeApplied chan struct{}
}

// newE2EKubeAPIProxy keeps normal Kubernetes API requests flowing to the
// current local Kind control plane and can drop one selected write response
// after the API server has applied the write.
func newE2EKubeAPIProxy(t *testing.T, rawKubeconfig string) (*e2eKubeAPIProxy, string) {
	t.Helper()
	cfg, err := clientcmd.Load([]byte(rawKubeconfig))
	if err != nil {
		t.Fatalf("parse Kind kubeconfig for scoped API fault proxy: %v", err)
	}
	ctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		t.Fatalf("current Kind kubeconfig context %q is missing", cfg.CurrentContext)
	}
	cluster, ok := cfg.Clusters[ctx.Cluster]
	if !ok {
		t.Fatalf("Kind kubeconfig cluster %q is missing", ctx.Cluster)
	}
	upstreamURL, err := url.Parse(cluster.Server)
	if err != nil || upstreamURL.Scheme != "https" || upstreamURL.Host == "" {
		t.Fatalf("unexpected Kind API server URL %q", cluster.Server)
	}
	caData := cluster.CertificateAuthorityData
	if len(caData) == 0 && cluster.CertificateAuthority != "" {
		caData, err = os.ReadFile(cluster.CertificateAuthority)
		if err != nil {
			t.Fatalf("read Kind API CA: %v", err)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caData) {
		t.Fatal("Kind kubeconfig contains no usable API server CA")
	}
	var clientCertificates []tls.Certificate
	if auth, ok := cfg.AuthInfos[ctx.AuthInfo]; ok {
		certData, keyData := auth.ClientCertificateData, auth.ClientKeyData
		if len(certData) == 0 && auth.ClientCertificate != "" {
			certData, err = os.ReadFile(auth.ClientCertificate)
			if err != nil {
				t.Fatalf("read Kind client certificate: %v", err)
			}
		}
		if len(keyData) == 0 && auth.ClientKey != "" {
			keyData, err = os.ReadFile(auth.ClientKey)
			if err != nil {
				t.Fatalf("read Kind client key: %v", err)
			}
		}
		if len(certData) > 0 || len(keyData) > 0 {
			cert, certErr := tls.X509KeyPair(certData, keyData)
			if certErr != nil {
				t.Fatalf("load Kind client credentials for API proxy: %v", certErr)
			}
			clientCertificates = append(clientCertificates, cert)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: clientCertificates}
	reverse := httputil.NewSingleHostReverseProxy(upstreamURL)
	reverse.Transport = transport
	p := &e2eKubeAPIProxy{proxy: reverse, writeApplied: make(chan struct{}, 1)}
	reverse.ModifyResponse = func(response *http.Response) error {
		request := response.Request
		if request == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil
		}
		p.mu.Lock()
		shouldDrop := p.armed && request.Method == p.targetMethod && request.URL.Path == p.targetPath
		if shouldDrop {
			p.armed = false
		}
		p.mu.Unlock()
		if !shouldDrop {
			return nil
		}
		p.writeApplied <- struct{}{}
		<-request.Context().Done()
		return fmt.Errorf("E2E dropped successful Kubernetes %s response after process interruption", request.Method)
	}
	p.server = httptest.NewTLSServer(reverse)

	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.server.Certificate().Raw})
	cluster.Server = p.server.URL
	cluster.CertificateAuthority = ""
	cluster.CertificateAuthorityData = proxyCA
	cluster.InsecureSkipTLSVerify = false
	cfg.Clusters[ctx.Cluster] = cluster
	proxied, err := clientcmd.Write(*cfg)
	if err != nil {
		p.Close()
		t.Fatalf("serialize fault-proxied Kind kubeconfig: %v", err)
	}
	return p, string(proxied)
}

func (p *e2eKubeAPIProxy) ArmServicePatch(namespace, service string) {
	p.arm(http.MethodPatch, "/api/v1/namespaces/"+namespace+"/services/"+service)
}

func (p *e2eKubeAPIProxy) ArmStableDeploymentPatch(namespace, deployment string) {
	p.arm(http.MethodPatch, "/apis/apps/v1/namespaces/"+namespace+"/deployments/"+deployment)
}

func (p *e2eKubeAPIProxy) ArmCandidateDeploymentCreate(namespace string) {
	p.arm(http.MethodPost, "/apis/apps/v1/namespaces/"+namespace+"/deployments")
}

func (p *e2eKubeAPIProxy) ArmCandidateServiceCreate(namespace string) {
	p.arm(http.MethodPost, "/api/v1/namespaces/"+namespace+"/services")
}

func (p *e2eKubeAPIProxy) ArmCandidateServiceDelete(namespace, service string) {
	p.arm(http.MethodDelete, "/api/v1/namespaces/"+namespace+"/services/"+service)
}

func (p *e2eKubeAPIProxy) arm(method, path string) {
	p.mu.Lock()
	p.targetMethod = method
	p.targetPath = path
	p.armed = true
	p.mu.Unlock()
}

func (p *e2eKubeAPIProxy) Close() {
	if p.server != nil {
		p.server.CloseClientConnections()
		p.server.Close()
	}
}

// e2eMySQLFaultProxy scopes connection faults to this test's backend process;
// it never pauses or changes the shared MySQL container.
type e2eMySQLFaultProxy struct {
	listener            net.Listener
	upstream            string
	mu                  sync.Mutex
	disabled            bool
	closed              bool
	armedCommitAckLoss  bool
	commitAckTriggerSQL string
	commitAckLost       chan struct{}
	active              map[net.Conn]struct{}
}

type e2eMySQLCommitAckLoss struct {
	lost chan struct{}
}

func newE2EMySQLFaultProxy(t *testing.T, upstream string) *e2eMySQLFaultProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &e2eMySQLFaultProxy{listener: listener, upstream: upstream, active: map[net.Conn]struct{}{}}
	go p.accept()
	return p
}

func (p *e2eMySQLFaultProxy) Addr() string {
	return p.listener.Addr().String()
}

func (p *e2eMySQLFaultProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.forward(client)
	}
}

func (p *e2eMySQLFaultProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disabled || p.closed {
		_ = conn.Close()
		return false
	}
	p.active[conn] = struct{}{}
	return true
}

func (p *e2eMySQLFaultProxy) untrack(conn net.Conn) {
	p.mu.Lock()
	delete(p.active, conn)
	p.mu.Unlock()
}

func (p *e2eMySQLFaultProxy) forward(client net.Conn) {
	if !p.track(client) {
		return
	}
	defer func() {
		p.untrack(client)
		_ = client.Close()
	}()

	upstream, err := net.DialTimeout("tcp", p.upstream, 5*time.Second)
	if err != nil || !p.track(upstream) {
		if upstream != nil {
			_ = upstream.Close()
		}
		return
	}
	defer func() {
		p.untrack(upstream)
		_ = upstream.Close()
	}()

	done := make(chan struct{}, 2)
	commitAckRequests := make(chan e2eMySQLCommitAckLoss, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		var pending []byte
		for {
			n, err := client.Read(buf)
			if n > 0 {
				if p.commitAckLossArmed() {
					pending = append(pending, buf[:n]...)
					ack, rest, found := p.findCommitAckLoss(pending)
					pending = rest
					if found {
						commitAckRequests <- e2eMySQLCommitAckLoss{lost: ack}
					}
				}
				if _, writeErr := upstream.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		var pending []byte
		var lost chan struct{}
		for {
			select {
			case attempt := <-commitAckRequests:
				lost = attempt.lost
			default:
			}
			n, err := upstream.Read(buf)
			if n > 0 {
				if lost == nil {
					select {
					case attempt := <-commitAckRequests:
						lost = attempt.lost
					default:
					}
				}
				if lost != nil {
					pending = append(pending, buf[:n]...)
					if mysqlPacketComplete(pending) {
						payloadLength := int(pending[0]) | int(pending[1])<<8 | int(pending[2])<<16
						if payloadLength > 0 && pending[4] == 0x00 {
							close(lost)
							return // the server committed; deliberately discard its OK packet
						}
						if _, writeErr := client.Write(pending); writeErr != nil {
							return
						}
						pending = nil
						lost = nil
					}
				} else if _, writeErr := client.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func (p *e2eMySQLFaultProxy) commitAckLossArmed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.armedCommitAckLoss || p.commitAckTriggerSQL != ""
}

func (p *e2eMySQLFaultProxy) findCommitAckLoss(stream []byte) (chan struct{}, []byte, bool) {
	for len(stream) >= 4 {
		payloadLength := int(stream[0]) | int(stream[1])<<8 | int(stream[2])<<16
		packetLength := payloadLength + 4
		if len(stream) < packetLength {
			break
		}
		payload := stream[4:packetLength]
		if len(payload) > 1 && (payload[0] == 0x03 || payload[0] == 0x16) {
			// Track both COM_QUERY and COM_STMT_PREPARE because database/sql may
			// send parameterized repository statements through the prepared
			// statement protocol. Arming on the target UPDATE lets the test drop
			// the COMMIT for workload promotion rather than an earlier journal
			// transaction in the same confirmation action.
			query := strings.TrimSpace(string(payload[1:]))
			query = strings.TrimSuffix(query, ";")
			p.mu.Lock()
			if p.commitAckTriggerSQL != "" && strings.Contains(strings.ToLower(query), strings.ToLower(p.commitAckTriggerSQL)) {
				p.armedCommitAckLoss = true
				p.commitAckTriggerSQL = ""
			}
			if payload[0] == 0x03 && strings.EqualFold(query, "COMMIT") && p.armedCommitAckLoss {
				p.armedCommitAckLoss = false
				lost := p.commitAckLost
				p.mu.Unlock()
				return lost, stream[packetLength:], true
			}
			p.mu.Unlock()
		}
		stream = stream[packetLength:]
	}
	return nil, stream, false
}

func mysqlPacketComplete(stream []byte) bool {
	if len(stream) < 4 {
		return false
	}
	payloadLength := int(stream[0]) | int(stream[1])<<8 | int(stream[2])<<16
	return len(stream) >= payloadLength+4
}

func (p *e2eMySQLFaultProxy) ArmCommitAckLoss() <-chan struct{} {
	return p.armCommitAckLoss("")
}

func (p *e2eMySQLFaultProxy) ArmCommitAckLossAfterQuery(query string) <-chan struct{} {
	query = strings.TrimSpace(query)
	if query == "" {
		panic("commit acknowledgement query trigger cannot be empty")
	}
	return p.armCommitAckLoss(query)
}

func (p *e2eMySQLFaultProxy) armCommitAckLoss(query string) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armedCommitAckLoss = true
	p.commitAckTriggerSQL = query
	if query != "" {
		p.armedCommitAckLoss = false
	}
	p.commitAckLost = make(chan struct{})
	return p.commitAckLost
}

func (p *e2eMySQLFaultProxy) SetAvailable(available bool) {
	p.mu.Lock()
	p.disabled = !available
	var connections []net.Conn
	if !available {
		connections = make([]net.Conn, 0, len(p.active))
		for conn := range p.active {
			connections = append(connections, conn)
		}
	}
	p.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (p *e2eMySQLFaultProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	connections := make([]net.Conn, 0, len(p.active))
	for conn := range p.active {
		connections = append(connections, conn)
	}
	p.mu.Unlock()
	_ = p.listener.Close()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

// startServer builds and launches the real binary against the e2e DB on
// an OS-assigned free port. The returned handle can restart the same process
// against the same isolated database for recovery drills.
func startServer(t *testing.T) *e2eServer {
	return startServerWithOverrides(t, nil)
}

func startServerWithOverrides(t *testing.T, overrides map[string]string) *e2eServer {
	t.Helper()
	root := backendRoot(t)
	bin := filepath.Join(t.TempDir(), "server")
	build := exec.Command("go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	e2eAddr = fmt.Sprintf("http://127.0.0.1:%d", port)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	// Environment: inherit the dev shell but let the test's settings win.
	// os.Getenv returns the FIRST envp entry, so appended duplicates lose
	// to a developer's exported vars — build a map and override instead.
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	// 只绑 loopback——绑 0.0.0.0 会把带固定 admin 密码的实例短暂暴露
	// 到局域网。
	env["HTTP_ADDR"] = fmt.Sprintf("127.0.0.1:%d", port)
	env["ENV"] = "dev"
	env["MOCK_RUNTIME"] = "false"
	env["NOTIFICATION_WEBHOOK_URL"] = ""
	env["NOTIFICATION_WEBHOOK_SECRET"] = ""
	var mysqlProxy *e2eMySQLFaultProxy
	if dsn := strings.TrimSpace(overrides["DATABASE_DSN"]); dsn != "" {
		env["DATABASE_DSN"] = dsn
	} else {
		mysqlProxy = newE2EMySQLFaultProxy(t, e2eMySQLAddress)
		env["DATABASE_DSN"] = "deploy:deploy-platform@tcp(" + mysqlProxy.Addr() + ")/" + e2eDB + "?parseTime=true&charset=utf8mb4"
	}
	env["CREDENTIAL_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString(key)
	env["ADMIN_INITIAL_PASSWORD"] = e2ePassword
	for name, value := range overrides {
		env[name] = value
	}
	server := &e2eServer{
		bin: bin, root: root, logPath: filepath.Join(t.TempDir(), "server.log"),
		env: make([]string, 0, len(env)), mysql: mysqlProxy,
	}
	for k, v := range env {
		server.env = append(server.env, k+"="+v)
	}
	t.Cleanup(server.stop)
	server.start(t)
	return server
}

func (s *e2eServer) start(t *testing.T) {
	t.Helper()
	logFile, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(s.bin)
	cmd.Dir = s.root
	cmd.Env = s.env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	s.cmd, s.logFile = cmd, logFile
	deadline := time.Now().Add(30 * time.Second)
	// 短超时 client：裸 http.Get 无超时，端口若无响应进程占用会把单次
	// 调用挂住、越过 deadline（deadline 只在迭代间检查）。
	healthClient := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := healthClient.Get(e2eAddr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	raw, _ := os.ReadFile(s.logPath)
	tail := raw
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	t.Fatalf("server never became healthy; server.log tail:\n%s", tail)
}

func (s *e2eServer) stopProcess() {
	if s.cmd != nil {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
		if s.logFile != nil {
			_ = s.logFile.Close()
		}
		s.cmd, s.logFile = nil, nil
	}
}

func (s *e2eServer) stop() {
	s.stopProcess()
	if s.mysql != nil {
		s.mysql.Close()
	}
	if s.kubeAPI != nil {
		s.kubeAPI.Close()
	}
}

func (s *e2eServer) restart(t *testing.T) {
	t.Helper()
	s.stopProcess()
	s.start(t)
}

// connectCluster uploads the current kubeconfig as the platform's cluster
// credential. Repeatable: updates the existing e2e-local record in place.
func connectCluster(t *testing.T) int {
	t.Helper()
	kubeconfig := run(t, "kubectl", "config", "view", "--minify", "--raw")
	existingID := 0
	for _, it := range apiList(t, "/api/v1/clusters") {
		if m, ok := it.(map[string]any); ok && m["env_code"] == "e2e-local" {
			existingID = int(m["id"].(float64))
		}
	}
	body := map[string]any{
		"name": "E2E Kind", "env_code": "e2e-local", "environment": "e2e",
		"environment_tier": "non_production", "deploy_policy": "automatic",
		"connection_type": "kubeconfig", "credential": kubeconfig,
		"enabled": true, "default_namespace": "default",
	}
	var code int
	if existingID > 0 {
		code, _ = api(t, "PUT", fmt.Sprintf("/api/v1/clusters/%d", existingID), body)
	} else {
		var out map[string]any
		code, out = api(t, "POST", "/api/v1/clusters", body)
		if f, ok := out["id"].(float64); ok {
			existingID = int(f)
		}
	}
	if code != 200 {
		t.Fatalf("connect cluster → %d", code)
	}
	return existingID
}

// TestKindE2E runs the playbook sequence end to end:
// start platform → connect Kind → browse → create Deployment →
// scale/image → logs → exec → edit YAML → delete → no residue.
func TestKindE2E(t *testing.T) {
	skipUnlessE2E(t)
	skipUnlessLocalKindContext(t)

	// Use a disposable MySQL container so this E2E never creates or drops
	// schemas in the developer's persistent database volume.
	if out, err := exec.Command("kubectl", "get", "nodes", "-o", "name").CombinedOutput(); err != nil {
		t.Skipf("kubectl context unreachable: %v\n%s", err, out)
	}
	mysql := startMigrationE2EMySQL(t)
	previousContainer, previousPassword, previousAddress := e2eMySQLContainer, e2eMySQLRootPassword, e2eMySQLAddress
	e2eMySQLContainer, e2eMySQLRootPassword, e2eMySQLAddress = mysql.container, mysql.rootPassword, mysql.address
	t.Cleanup(func() {
		e2eMySQLContainer, e2eMySQLRootPassword, e2eMySQLAddress = previousContainer, previousPassword, previousAddress
	})
	runID := e2eRunID(t)
	e2eNS = "kbtest-e2e-" + runID
	e2eDB = mysql.database

	applySchema(t)
	verifyResourceLockContention(t)
	server := startServer(t)

	code, out := api(t, "POST", "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": e2ePassword,
	})
	if code != 200 {
		t.Fatalf("login → %d %v", code, out)
	}

	clusterID := connectCluster(t)
	q := fmt.Sprintf("cluster_id=%d&namespace=%s", clusterID, e2eNS)

	kubectlApply(t, fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", e2eNS))
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "delete", "namespace", e2eNS, "--wait=false").Run()
		// Self-verify zero residue rather than relying on the next run's
		// preamble to absorb a still-terminating namespace.
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := exec.Command("kubectl", "get", "ns", e2eNS).CombinedOutput()
			if strings.Contains(string(out), "NotFound") {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Errorf("namespace %s still terminating after cleanup — residue possible", e2eNS)
	})

	// --- create test Deployment ---
	kubectlApply(t, fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kbtest-web
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kbtest-web
  template:
    metadata:
      labels:
        app: kbtest-web
    spec:
      containers:
      - name: web
        image: docker.io/library/busybox:1.36
        command: ["sh", "-c", "i=0; while true; do i=$((i+1)); echo tick-$i; sleep 2; done"]
`, e2eNS))
	waitForPods(t, "app=kbtest-web", 90*time.Second)

	// --- browse ---
	found := false
	for _, it := range apiList(t, "/api/v1/k8s/resources?"+q+"&group=apps&version=v1&resource=deployments") {
		if m := it.(map[string]any); m["metadata"].(map[string]any)["name"] == "kbtest-web" {
			found = true
		}
	}
	if !found {
		t.Fatal("kbtest-web not visible in resource list")
	}

	// --- scale ---
	code, _ = api(t, "POST", "/api/v1/k8s/resources/kbtest-web/scale?"+q+"&group=apps&version=v1&resource=deployments",
		map[string]any{"replicas": 2})
	if code != 200 {
		t.Fatalf("scale → %d", code)
	}
	waitForJSONPath(t, "deploy", "kbtest-web", "{.spec.replicas}", "2", 30*time.Second)

	// --- image update ---
	code, _ = api(t, "POST", "/api/v1/k8s/resources/kbtest-web/image?"+q+"&group=apps&version=v1&resource=deployments",
		map[string]any{"container": "web", "image": "m.daocloud.io/docker.io/library/busybox:1.36"})
	if code != 200 {
		t.Fatalf("image update → %d", code)
	}
	waitForJSONPath(t, "deploy", "kbtest-web", "{.spec.template.spec.containers[0].image}",
		"m.daocloud.io/docker.io/library/busybox:1.36", 30*time.Second)

	// --- logs (snapshot endpoint) ---
	time.Sleep(3 * time.Second)
	podName := run(t, "kubectl", "get", "pods", "-n", e2eNS, "-l", "app=kbtest-web",
		"-o", "jsonpath={.items[0].metadata.name}")
	code, logsOut := api(t, "GET", fmt.Sprintf("/api/v1/k8s/pods/%s/logs?cluster_id=%d&namespace=%s&tail=10",
		podName, clusterID, e2eNS), nil)
	if code != 200 {
		t.Fatalf("logs → %d %v", code, logsOut)
	}
	if !strings.Contains(fmt.Sprintf("%v", logsOut), "tick") {
		t.Fatalf("log snapshot has no tick output: %v", logsOut)
	}

	// --- exec via WS terminal ---
	execEcho(t, clusterID, podName)

	// --- edit YAML (inject env var via PUT manifest) ---
	code, obj := api(t, "GET", "/api/v1/k8s/resources/kbtest-web?"+q+"&group=apps&version=v1&resource=deployments", nil)
	if code != 200 {
		t.Fatalf("get deployment → %d", code)
	}
	containers := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	c0 := containers[0].(map[string]any)
	c0["env"] = []any{map[string]any{"name": "E2E_MARK", "value": "on"}}
	code, _ = api(t, "PUT", "/api/v1/k8s/resources/kbtest-web?"+q+"&group=apps&version=v1&resource=deployments",
		map[string]any{"manifest": toJSON(t, obj)})
	if code != 200 {
		t.Fatalf("yaml update → %d", code)
	}
	waitForJSONPath(t, "deploy", "kbtest-web", "{.spec.template.spec.containers[0].env[0].name}",
		"E2E_MARK", 30*time.Second)

	// --- create PVC via POST manifest (dry-run first, then real, then
	// duplicate → 409). The PVC stays Pending — kind's `standard`
	// StorageClass is WaitForFirstConsumer, so existence is the check,
	// never Bound.
	pvcQ := fmt.Sprintf("cluster_id=%d&version=v1&resource=persistentvolumeclaims&namespace=%s", clusterID, e2eNS)
	pvcManifest := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kbtest-data
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 1Gi}}
`
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+pvcQ+"&dry_run=true",
		map[string]any{"manifest": pvcManifest})
	if code != 200 {
		t.Fatalf("pvc dry-run create → %d", code)
	}
	if out, _ := exec.Command("kubectl", "get", "pvc", "kbtest-data", "-n", e2eNS).CombinedOutput(); !strings.Contains(string(out), "NotFound") {
		t.Fatalf("dry-run persisted the PVC: %s", out)
	}
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+pvcQ,
		map[string]any{"manifest": pvcManifest})
	if code != 201 {
		t.Fatalf("pvc create → %d", code)
	}
	run(t, "kubectl", "get", "pvc", "kbtest-data", "-n", e2eNS)
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+pvcQ,
		map[string]any{"manifest": pvcManifest})
	if code != 409 {
		t.Fatalf("duplicate pvc create → %d, want 409", code)
	}
	code, _ = api(t, "DELETE", "/api/v1/k8s/resources/kbtest-data?"+pvcQ, nil)
	if code != 200 {
		t.Fatalf("pvc delete → %d", code)
	}

	// --- create ConfigMap via POST manifest (dry-run → real → 409) ---
	cmQ := fmt.Sprintf("cluster_id=%d&version=v1&resource=configmaps&namespace=%s", clusterID, e2eNS)
	cmManifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: kbtest-config
data:
  app.ini: |
    [server]
    port = 8080
`
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+cmQ+"&dry_run=true",
		map[string]any{"manifest": cmManifest})
	if code != 200 {
		t.Fatalf("configmap dry-run create → %d", code)
	}
	if out, _ := exec.Command("kubectl", "get", "configmap", "kbtest-config", "-n", e2eNS).CombinedOutput(); !strings.Contains(string(out), "NotFound") {
		t.Fatalf("dry-run persisted the ConfigMap: %s", out)
	}
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+cmQ,
		map[string]any{"manifest": cmManifest})
	if code != 201 {
		t.Fatalf("configmap create → %d", code)
	}
	run(t, "kubectl", "get", "configmap", "kbtest-config", "-n", e2eNS)
	code, _ = api(t, "POST", "/api/v1/k8s/resources?"+cmQ,
		map[string]any{"manifest": cmManifest})
	if code != 409 {
		t.Fatalf("duplicate configmap create → %d, want 409", code)
	}

	// --- create Secret via POST manifest: plaintext goes in through
	// stringData, and the create response obeys the same no-leak rule as
	// reads — every data value comes back masked as "***".
	secQ := fmt.Sprintf("cluster_id=%d&version=v1&resource=secrets&namespace=%s", clusterID, e2eNS)
	secManifest := `apiVersion: v1
kind: Secret
metadata:
  name: kbtest-secret
type: Opaque
stringData:
  username: admin
  password: s3cret
`
	code, obj = api(t, "POST", "/api/v1/k8s/resources?"+secQ,
		map[string]any{"manifest": secManifest})
	if code != 201 {
		t.Fatalf("secret create → %d", code)
	}
	for k, v := range obj["data"].(map[string]any) {
		if v != "***" {
			t.Fatalf("secret create response leaks data[%s]=%v", k, v)
		}
	}
	// The subsequent detail GET is masked too, while kubectl sees the
	// real base64 — the mask is an API-layer view, not stored content.
	code, obj = api(t, "GET", "/api/v1/k8s/resources/kbtest-secret?"+secQ, nil)
	if code != 200 {
		t.Fatalf("get secret → %d", code)
	}
	for k, v := range obj["data"].(map[string]any) {
		if v != "***" {
			t.Fatalf("secret detail data[%s]=%v, want masked", k, v)
		}
	}
	if v := run(t, "kubectl", "get", "secret", "kbtest-secret", "-n", e2eNS,
		"-o", "jsonpath={.data.password}"); v != "czNjcmV0" {
		t.Fatalf("kubectl secret password = %q, want base64 of s3cret", v)
	}

	// --- structured data edit (the kv dialog path): server-side merge
	// keyed on resource_version — set writes, delete removes, untouched
	// keys keep their real values, stale rv → 409.
	code, obj = api(t, "GET", "/api/v1/k8s/resources/kbtest-config?"+cmQ, nil)
	if code != 200 {
		t.Fatalf("get configmap → %d", code)
	}
	cmRv := obj["metadata"].(map[string]any)["resourceVersion"].(string)
	code, obj = api(t, "POST", "/api/v1/k8s/resources/kbtest-config/data?"+cmQ,
		map[string]any{
			"resource_version": cmRv,
			"set":              map[string]string{"extra": "on"},
		})
	if code != 200 {
		t.Fatalf("configmap data edit → %d", code)
	}
	if v := run(t, "kubectl", "get", "configmap", "kbtest-config", "-n", e2eNS,
		"-o", "jsonpath={.data.extra}"); v != "on" {
		t.Fatalf("configmap extra = %q, want on", v)
	}
	if v := run(t, "kubectl", "get", "configmap", "kbtest-config", "-n", e2eNS,
		`-o`, `jsonpath={.data.app\.ini}`); !strings.Contains(v, "port = 8080") {
		t.Fatalf("configmap app.ini must survive the merge: %q", v)
	}
	// Replaying the same rv must conflict — the second reader's version
	// no longer matches the live object.
	code, _ = api(t, "POST", "/api/v1/k8s/resources/kbtest-config/data?"+cmQ,
		map[string]any{
			"resource_version": cmRv,
			"set":              map[string]string{"extra": "again"},
		})
	if code != 409 {
		t.Fatalf("stale-rv data edit → %d, want 409", code)
	}

	// Secret edit: replace one key, delete one, leave nothing readable.
	code, obj = api(t, "GET", "/api/v1/k8s/resources/kbtest-secret?"+secQ, nil)
	if code != 200 {
		t.Fatalf("get secret for edit → %d", code)
	}
	secRv := obj["metadata"].(map[string]any)["resourceVersion"].(string)
	code, obj = api(t, "POST", "/api/v1/k8s/resources/kbtest-secret/data?"+secQ,
		map[string]any{
			"resource_version": secRv,
			"set":              map[string]string{"password": "n3wpass"},
			"delete_keys":      []string{"username"},
		})
	if code != 200 {
		t.Fatalf("secret data edit → %d", code)
	}
	for k, v := range obj["data"].(map[string]any) {
		if v != "***" {
			t.Fatalf("secret edit response leaks data[%s]=%v", k, v)
		}
	}
	if v := run(t, "kubectl", "get", "secret", "kbtest-secret", "-n", e2eNS,
		"-o", "jsonpath={.data.password}"); v != "bjN3cGFzcw==" {
		t.Fatalf("kubectl secret password = %q, want base64 of n3wpass", v)
	}
	if v := run(t, "kubectl", "get", "secret", "kbtest-secret", "-n", e2eNS,
		"-o", "jsonpath={.data.username}"); v != "" {
		t.Fatalf("deleted key username survived: %q", v)
	}

	// --- cleanup: the newly created objects go out through the API ---
	code, _ = api(t, "DELETE", "/api/v1/k8s/resources/kbtest-config?"+cmQ, nil)
	if code != 200 {
		t.Fatalf("configmap delete → %d", code)
	}
	code, _ = api(t, "DELETE", "/api/v1/k8s/resources/kbtest-secret?"+secQ, nil)
	if code != 200 {
		t.Fatalf("secret delete → %d", code)
	}

	// --- delete test resource through the API ---
	code, _ = api(t, "DELETE", "/api/v1/k8s/resources/kbtest-web?"+q+"&group=apps&version=v1&resource=deployments", nil)
	if code != 200 {
		t.Fatalf("delete deployment → %d", code)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := exec.Command("kubectl", "get", "deploy", "kbtest-web", "-n", e2eNS).CombinedOutput()
		if strings.Contains(string(out), "NotFound") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deployment still exists after API delete")
		}
		time.Sleep(time.Second)
	}

	// Exercise the native blue-green flow against real Deployments, Service,
	// EndpointSlices and the real approval/runner APIs. All objects remain in
	// this run's isolated namespace and database.
	runBlueGreenDeliveryE2E(t, server, clusterID, e2eNS)
	loginE2EUser(t, "admin", e2ePassword)
	code, deliveryMetrics := api(t, "GET", "/api/v1/delivery/metrics?window_days=365", nil)
	if code != 200 {
		t.Fatalf("read isolated delivery metrics → %d %v", code, deliveryMetrics)
	}
	if deliveryMetrics["production_attempts"].(float64) < 1 || deliveryMetrics["unknown_tier_samples"].(float64) != 0 {
		t.Fatalf("real MySQL delivery metrics did not load immutable target tiers: %v", deliveryMetrics)
	}
}

const e2eBusyboxImage = "docker.m.daocloud.io/library/busybox:1.36"

func runBlueGreenDeliveryE2E(t *testing.T, server *e2eServer, testClusterID int, namespace string) {
	t.Helper()
	image, digest := e2eBusyboxArtifact(t)
	appName := "kbtest-bluegreen"
	testDeployment := "kbtest-bg-test"
	prodDeployment := "kbtest-bg-prod"
	entryService := "kbtest-bg-entry"

	kubectlApply(t, fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s-test
  template:
    metadata:
      labels:
        app: %s-test
    spec:
      containers:
      - name: web
        image: %s
        command: ["sh", "-c", "while true; do echo test; sleep 2; done"]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s-prod
  template:
    metadata:
      labels:
        app: %s-prod
    spec:
      containers:
      - name: web
        image: %s
        command: ["sh", "-c", "while true; do echo stable; sleep 2; done"]
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s-prod
  ports:
  - name: http
    port: 80
    targetPort: 8080
`, testDeployment, namespace, appName, appName, image, prodDeployment, namespace, appName, appName, image, entryService, namespace, appName))
	run(t, "kubectl", "rollout", "status", "deployment/"+testDeployment, "-n", namespace, "--timeout=90s")
	run(t, "kubectl", "rollout", "status", "deployment/"+prodDeployment, "-n", namespace, "--timeout=90s")

	// A second cluster record models the production environment while still
	// pointing at the same local Kind cluster. Its app and all Kubernetes
	// resources stay fenced to this test namespace.
	rawKubeconfig := run(t, "kubectl", "config", "view", "--minify", "--raw")
	kubeAPIProxy, kubeconfig := newE2EKubeAPIProxy(t, rawKubeconfig)
	server.kubeAPI = kubeAPIProxy
	code, prodCluster := api(t, "POST", "/api/v1/clusters", map[string]any{
		"name": "E2E Kind Production", "env_code": "e2e-prod", "environment": "e2e",
		"environment_tier": "production", "deploy_policy": "manual",
		"connection_type": "kubeconfig", "credential": kubeconfig,
		"enabled": true, "default_namespace": namespace,
	})
	if code != 200 {
		t.Fatalf("create production cluster → %d %v", code, prodCluster)
	}
	prodClusterID := int(responseID(t, prodCluster, "id"))

	code, testEnv := api(t, "POST", "/api/v1/environments", map[string]any{
		"name": "E2E 测试环境 " + namespace, "environment_tier": "non_production",
	})
	if code != 201 {
		t.Fatalf("create test environment → %d %v", code, testEnv)
	}
	code, prodEnv := api(t, "POST", "/api/v1/environments", map[string]any{
		"name": "E2E 生产环境 " + namespace, "environment_tier": "production",
	})
	if code != 201 {
		t.Fatalf("create production environment → %d %v", code, prodEnv)
	}

	operatorName := "e2e-operator-" + strings.TrimPrefix(namespace, "kbtest-e2e-")
	operatorPassword := "e2e-operator-pass"
	code, operator := api(t, "POST", "/api/v1/users", map[string]any{
		"username": operatorName, "display_name": "E2E Operator", "role": "operator", "password": operatorPassword,
	})
	if code != 201 {
		t.Fatalf("create independent operator → %d %v", code, operator)
	}
	operatorID := responseID(t, operator, "id")
	prodEnvID := responseID(t, prodEnv, "id")
	code, evidencePolicy := api(t, "PUT", fmt.Sprintf("/api/v1/environments/%d/evidence-policy", prodEnvID), map[string]any{
		"expected_version": 0, "required_verification_kinds": []string{"manual"},
		"max_evidence_age_minutes": 1440, "trusted_submitters": map[string][]string{"manual": {operatorName}},
	})
	if code != 200 || evidencePolicy["version"] != float64(1) {
		t.Fatalf("configure isolated production evidence policy → %d %v", code, evidencePolicy)
	}

	code, businessApp := api(t, "POST", "/api/v1/applications", map[string]any{"name": "E2E Blue Green " + namespace})
	if code != 201 {
		t.Fatalf("create business application → %d %v", code, businessApp)
	}
	businessAppID := responseID(t, businessApp, "id")
	code, component := api(t, "POST", fmt.Sprintf("/api/v1/applications/%d/components", businessAppID), map[string]any{"name": "busybox web"})
	if code != 201 {
		t.Fatalf("create delivery component → %d %v", code, component)
	}
	componentID := responseID(t, component, "id")

	testAppID := createE2EApp(t, testClusterID, appName, testDeployment, image, namespace)
	prodAppID := createE2EApp(t, prodClusterID, appName, prodDeployment, image, namespace)
	testEnvID := responseID(t, testEnv, "id")
	code, binding := api(t, "PUT", fmt.Sprintf("/api/v1/apps/%d/delivery-binding", testAppID), map[string]any{
		"application_id": businessAppID, "component_id": componentID, "environment_id": testEnvID,
	})
	if code != 200 {
		t.Fatalf("bind test component → %d %v", code, binding)
	}
	code, binding = api(t, "PUT", fmt.Sprintf("/api/v1/apps/%d/delivery-binding", prodAppID), map[string]any{
		"application_id": businessAppID, "component_id": componentID, "environment_id": prodEnvID,
		"strategy_draft": "blue_green", "service_name": entryService,
		"expected_policy_version": 0, "strategy": "blue_green", "candidate_retention_hours": 48,
	})
	if code != 200 {
		t.Fatalf("atomically bind production component and blue-green policy → %d %v", code, binding)
	}
	code, policyHistory := api(t, "GET", fmt.Sprintf("/api/v1/apps/%d/delivery-policy", prodAppID), nil)
	if code != 200 || policyHistory["current_version"] != float64(1) {
		t.Fatalf("read atomically saved binding policy history → %d %v", code, policyHistory)
	}
	versions, ok := policyHistory["versions"].([]any)
	if !ok || len(versions) != 1 {
		t.Fatalf("policy history versions=%v, want one atomic initial revision", policyHistory["versions"])
	}
	policy, ok := versions[0].(map[string]any)
	if !ok || policy["version"] != float64(1) || policy["candidate_retention_hours"] != float64(48) {
		t.Fatalf("atomically saved blue-green policy=%v", versions[0])
	}
	code, capabilityPreview := api(t, "POST", "/api/v1/delivery-preview", map[string]any{
		"cluster_id": prodClusterID, "namespace": namespace, "deployment_name": prodDeployment,
	})
	if code != 200 {
		t.Fatalf("read-only delivery capability preview → %d %v", code, capabilityPreview)
	}
	matchingServices, ok := capabilityPreview["matching_services"].([]any)
	if !ok || len(matchingServices) != 1 || matchingServices[0] != entryService {
		t.Fatalf("capability preview matching services=%v, want only %q", capabilityPreview["matching_services"], entryService)
	}
	previewChecks, ok := capabilityPreview["checks"].([]any)
	if !ok {
		t.Fatalf("capability preview checks missing: %v", capabilityPreview)
	}
	previewStatuses := map[string]string{}
	for _, item := range previewChecks {
		check, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := check["key"].(string)
		status, _ := check["status"].(string)
		previewStatuses[key] = status
	}
	if previewStatuses["deployment"] != "passed" || previewStatuses["service-entry"] != "passed" || previewStatuses["autoscaling"] != "passed" {
		t.Fatalf("capability preview statuses=%v", previewStatuses)
	}

	code, runner := api(t, "POST", "/api/v1/runner/bindings", map[string]any{
		"app_id": testAppID, "pipeline_id": "781236", "private_cluster_id": "e2e-kind-runner",
		"platform_url": e2eAddr,
	})
	if code != 200 {
		t.Fatalf("configure Runner binding → %d %v", code, runner)
	}
	runnerBinding := runner["binding"].(map[string]any)
	runnerID := responseID(t, runnerBinding, "id")
	runnerKey, ok := runner["key"].(string)
	if !ok || runnerKey == "" {
		t.Fatalf("Runner credential missing from one-time response: %v", runner)
	}
	code, event := apiWithBearer(t, "POST", fmt.Sprintf("/openapi/v1/runner/bindings/%d/events", runnerID), map[string]any{
		"pipeline_id": "781236", "build_number": "1", "image_full": image,
		"image_digest": digest, "commit_id": "e2e-blue-green",
	}, runnerKey)
	if code != 202 {
		t.Fatalf("Runner build event → %d %v", code, event)
	}
	releaseID := responseID(t, event, "release_id")
	waitForRunnerRelease(t, runnerID, releaseID, runnerKey, "success", 3*time.Minute)
	waitForJSONPath(t, "deploy", testDeployment, "{.spec.template.spec.containers[0].image}", strings.Split(image, ":")[0]+"@"+digest, 30*time.Second)

	loginE2EUser(t, operatorName, operatorPassword)
	code, evidence := api(t, "POST", fmt.Sprintf("/api/v1/releases/%d/verification", releaseID), map[string]any{
		"digest": digest, "status": "passed", "kind": "manual",
		"summary":     "Kind E2E 测试环境镜像已就绪，人工验收通过。",
		"valid_until": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	})
	if code != 200 {
		t.Fatalf("record manual test verification → %d %v", code, evidence)
	}

	loginE2EUser(t, "admin", e2ePassword)
	code, approvalGrant := api(t, "PUT", fmt.Sprintf("/api/v1/delivery/approval-grants/users/%d", operatorID), map[string]any{
		"scopes": []map[string]int64{{"application_id": businessAppID, "environment_id": prodEnvID}},
	})
	if code != 200 {
		t.Fatalf("grant operator production approval scope → %d %v", code, approvalGrant)
	}
	code, plan := api(t, "POST", "/api/v1/delivery-plans", map[string]any{
		"release_id": releaseID, "cluster_id": prodClusterID,
	})
	if code != 201 && code != 200 {
		t.Fatalf("create blue-green plan → %d %v", code, plan)
	}
	if plan["strategy"] != "blue_green" {
		t.Fatalf("plan strategy = %v, want blue_green", plan["strategy"])
	}
	snapshot, ok := plan["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("plan snapshot is missing: %v", plan["snapshot"])
	}
	policySnapshot, ok := snapshot["policy"].(map[string]any)
	if !ok || policySnapshot["version"] != float64(1) || policySnapshot["source"] != "versioned_policy" || snapshot["candidate_retention_hours"] != float64(48) {
		t.Fatalf("plan did not pin the current policy version: %v", snapshot["policy"])
	}
	planID := responseID(t, plan, "id")
	candidateSuffix := "-bg-" + strconv.FormatInt(planID, 10)
	candidateDeployment := prodDeployment + candidateSuffix
	candidatePreviewService := entryService + candidateSuffix
	code, approval := api(t, "POST", fmt.Sprintf("/api/v1/delivery-plans/%d/request-approval", planID), map[string]any{
		"manual_review_acknowledged": true,
	})
	if code != 202 {
		t.Fatalf("request blue-green approval → %d %v", code, approval)
	}
	requestID := responseID(t, approval, "request_id")
	loginE2EUser(t, operatorName, operatorPassword)
	planTarget := snapshot["target"].(map[string]any)
	stableLabelKey := planTarget["stable_label_key"].(string)
	if got := deploymentTemplateLabel(t, namespace, prodDeployment, stableLabelKey); got != "" {
		t.Fatalf("stable Deployment unexpectedly already has the platform track label %q=%q", stableLabelKey, got)
	}
	// Interrupt the first blue-green write after Kind persists the stable-track
	// Pod-template label but before the backend can journal its response.
	kubeAPIProxy.ArmStableDeploymentPatch(namespace, prodDeployment)
	code, approved := api(t, "POST", fmt.Sprintf("/api/v1/promotion-requests/%d/approve", requestID), nil)
	if code != 202 {
		t.Fatalf("approve blue-green plan → %d %v", code, approved)
	}
	prodLogID := responseID(t, approved, "prod_log_id")
	select {
	case <-kubeAPIProxy.writeApplied:
	case <-time.After(4 * time.Minute):
		t.Fatal("Kind did not apply the stable Deployment label patch while the response fault was armed")
	}
	server.stopProcess()
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	stableLabelAttention := waitForDeliveryAttention(t, prodLogID, 30*time.Second)
	stableLabelRun := stableLabelAttention["run"].(map[string]any)
	stableLabelRunID := responseID(t, stableLabelRun, "id")
	stableLabelLocks := countDeliveryLocks(t, prodLogID)
	if stableLabelLocks == 0 {
		t.Fatal("stable-label interruption released production resource locks")
	}
	if got := deploymentTemplateLabel(t, namespace, prodDeployment, stableLabelKey); got != "stable" {
		t.Fatalf("Kind did not persist stable track label before dropping the response: got=%q", got)
	}

	// The first recovery can still lose the stable-entry selector PATCH response.
	// Restart once more, verify the live Service and resume from its persisted
	// action intent before testing candidate Deployment creation.
	kubeAPIProxy.ArmServicePatch(namespace, entryService)
	type stableServiceResult struct {
		status int
		body   map[string]any
		err    error
	}
	stableServicePath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", stableLabelRunID)
	stableServiceVersion := responseID(t, stableLabelRun, "version")
	stableServiceDone := make(chan stableServiceResult, 1)
	go func() {
		status, body, err := apiRequest("POST", stableServicePath, map[string]any{
			"action": "reconcile", "expected_version": stableServiceVersion,
			"idempotency_key": fmt.Sprintf("e2e-stable-service-reconcile-%d", stableLabelRunID),
		}, "")
		stableServiceDone <- stableServiceResult{status: status, body: body, err: err}
	}()
	select {
	case <-kubeAPIProxy.writeApplied:
	case result := <-stableServiceDone:
		t.Fatalf("stable-label reconcile returned before stable Service selector PATCH: status=%d body=%v err=%v server log=%s", result.status, result.body, result.err, tailLog(server.logPath))
	case <-time.After(45 * time.Second):
		t.Fatalf("Kind did not apply the stable Service selector PATCH; server log=%s", tailLog(server.logPath))
	}
	server.stopProcess()
	select {
	case result := <-stableServiceDone:
		if result.err == nil {
			t.Fatalf("stable Service selector PATCH unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stable Service selector PATCH request did not close after backend termination")
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	stableServiceAttention := waitForDeliveryAttention(t, prodLogID, 30*time.Second)
	stableServiceRun := stableServiceAttention["run"].(map[string]any)
	stableServiceRunID := responseID(t, stableServiceRun, "id")
	if got := countDeliveryLocks(t, prodLogID); got != stableLabelLocks {
		t.Fatalf("stable Service selector interruption changed resource locks: before=%d after=%d", stableLabelLocks, got)
	}
	waitForServiceSelector(t, namespace, entryService, stableLabelKey, "stable", 30*time.Second)

	// Reconcile the persisted stable Service intent and interrupt candidate
	// Deployment creation in the same execution.
	kubeAPIProxy.ArmCandidateDeploymentCreate(namespace)
	type candidateDeploymentResult struct {
		status int
		body   map[string]any
		err    error
	}
	stableServiceRunVersion := responseID(t, stableServiceRun, "version")
	reconcileCandidatePath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", stableServiceRunID)
	reconcileCandidateDone := make(chan candidateDeploymentResult, 1)
	go func() {
		status, body, err := apiRequest("POST", reconcileCandidatePath, map[string]any{
			"action": "reconcile", "expected_version": stableServiceRunVersion,
			"idempotency_key": fmt.Sprintf("e2e-stable-label-reconcile-%d", stableServiceRunID),
		}, "")
		reconcileCandidateDone <- candidateDeploymentResult{status: status, body: body, err: err}
	}()
	select {
	case <-kubeAPIProxy.writeApplied:
	case result := <-reconcileCandidateDone:
		t.Fatalf("stable-Service reconciliation returned before candidate Deployment create: status=%d body=%v err=%v server log=%s", result.status, result.body, result.err, tailLog(server.logPath))
	case <-time.After(45 * time.Second):
		t.Fatalf("Kind did not apply the candidate Deployment create during stable-Service reconciliation; server log=%s", tailLog(server.logPath))
	}
	server.stopProcess()
	select {
	case result := <-reconcileCandidateDone:
		if result.err == nil {
			t.Fatalf("candidate Deployment create unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("candidate Deployment request did not close after backend termination")
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	partialCandidateView := waitForDeliveryAttention(t, prodLogID, 30*time.Second)
	partialCandidateRun := partialCandidateView["run"].(map[string]any)
	partialCandidateRunID := responseID(t, partialCandidateRun, "id")
	partialLocks := countDeliveryLocks(t, prodLogID)
	if partialLocks == 0 || partialLocks != stableLabelLocks {
		t.Fatalf("candidate-create interruption changed production resource locks: before=%d after=%d", stableLabelLocks, partialLocks)
	}
	partialTarget := partialCandidateView["plan"].(map[string]any)["snapshot"].(map[string]any)["target"].(map[string]any)
	if partialTarget["stable_label_key"] != stableLabelKey {
		t.Fatalf("stable label key changed across recovery: before=%q after=%v", stableLabelKey, partialTarget["stable_label_key"])
	}
	waitForServiceSelector(t, namespace, entryService, stableLabelKey, "stable", 30*time.Second)
	candidateDeploymentUID := run(t, "kubectl", "get", "deployment", candidateDeployment, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	if preview := run(t, "kubectl", "get", "service", candidatePreviewService, "-n", namespace, "--ignore-not-found", "-o", "name"); preview != "" {
		t.Fatalf("candidate preview Service unexpectedly exists before reconcile: %s", preview)
	}
	// The recovery adopts the existing Deployment and creates its preview
	// Service. Drop that successful create response and restart again so the
	// next reconcile must discover and verify the Service by identity/config.
	kubeAPIProxy.ArmCandidateServiceCreate(namespace)
	type candidateServiceResult struct {
		status int
		body   map[string]any
		err    error
	}
	candidateServiceDone := make(chan candidateServiceResult, 1)
	partialReconcilePath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", partialCandidateRunID)
	partialReconcileVersion := responseID(t, partialCandidateRun, "version")
	go func() {
		status, body, err := apiRequest("POST", partialReconcilePath, map[string]any{
			"action": "reconcile", "expected_version": partialReconcileVersion,
			"idempotency_key": fmt.Sprintf("e2e-candidate-service-crash-%d", partialCandidateRunID),
		}, "")
		candidateServiceDone <- candidateServiceResult{status: status, body: body, err: err}
	}()
	select {
	case <-kubeAPIProxy.writeApplied:
	case <-time.After(45 * time.Second):
		t.Fatal("Kind did not apply the candidate preview Service create while the response fault was armed")
	}
	server.stopProcess()
	select {
	case result := <-candidateServiceDone:
		if result.err == nil {
			t.Fatalf("candidate preview Service create unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("candidate preview Service request did not close after backend termination")
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	serviceCreateAttention := waitForDeliveryAttention(t, prodLogID, 30*time.Second)
	if countDeliveryLocks(t, prodLogID) != partialLocks {
		t.Fatal("candidate preview Service create interruption released production resource locks")
	}
	if got := run(t, "kubectl", "get", "service", candidatePreviewService, "-n", namespace, "-o", "name"); got != "service/"+candidatePreviewService {
		t.Fatalf("Kind did not persist candidate preview Service before dropping the response: %q", got)
	}
	code, resumedCandidate := applyDeliveryRunAction(t, partialCandidateRunID, serviceCreateAttention, "reconcile")
	if code != 200 {
		t.Fatalf("resume candidate preview Service after process restart → %d %v", code, resumedCandidate)
	}
	if resumedCandidate["run"].(map[string]any)["current_stage"] != "awaiting_candidate_review" {
		t.Fatalf("partial candidate reconcile did not reach review: %v", resumedCandidate["run"])
	}
	deployLog, ok := resumedCandidate["deploy_log"].(map[string]any)
	if !ok {
		t.Fatalf("reconciled candidate view has no deploy log: %v", resumedCandidate)
	}
	stepDetail, ok := deployLog["step_detail"].(string)
	if !ok || stepDetail == "" {
		t.Fatalf("candidate runtime image observation was not persisted to step_detail: %v", deployLog["step_detail"])
	}
	var deploySteps []service.StepResult
	if err := json.Unmarshal([]byte(stepDetail), &deploySteps); err != nil {
		t.Fatalf("decode candidate runtime image observation: %v", err)
	}
	var candidateImageObservation *service.StepResult
	for i := range deploySteps {
		if deploySteps[i].Step == "verify_candidate_pod_image" {
			candidateImageObservation = &deploySteps[i]
			break
		}
	}
	if candidateImageObservation == nil || candidateImageObservation.ObservationStatus != "verified" && candidateImageObservation.ObservationStatus != "unknown" {
		t.Fatalf("candidate runtime image observation missing a known status: %+v", deploySteps)
	}
	if got := run(t, "kubectl", "get", "deployment", candidateDeployment, "-n", namespace, "-o", "jsonpath={.metadata.uid}"); got != candidateDeploymentUID {
		t.Fatalf("reconcile replaced the already-created candidate Deployment UID: before=%s after=%s", candidateDeploymentUID, got)
	}
	if got := run(t, "kubectl", "get", "service", candidatePreviewService, "-n", namespace, "-o", "name"); got != "service/"+candidatePreviewService {
		t.Fatalf("reconcile did not create the missing candidate preview Service: %q", got)
	}
	if countDeliveryLocks(t, prodLogID) != partialLocks {
		t.Fatal("candidate reconcile changed production resource lock ownership")
	}
	for _, raw := range resumedCandidate["actions"].([]any) {
		action := raw.(map[string]any)
		if action["action_key"] == "blue-green:candidate-deployment" && action["status"] != "applied" {
			t.Fatalf("reconcile did not confirm candidate Deployment action: %v", action)
		}
		if action["action_key"] == "blue-green:candidate-service" && action["status"] != "applied" {
			t.Fatalf("reconcile did not confirm candidate preview Service action: %v", action)
		}
	}
	runView := resumedCandidate
	runID := responseID(t, runView["run"].(map[string]any), "id")
	preRestartRun := runView["run"].(map[string]any)
	preRestartEvents := runView["events"].([]any)
	deployLogID := responseID(t, preRestartRun, "deploy_log_id")
	locksBeforeRestart := countDeliveryLocks(t, deployLogID)
	if locksBeforeRestart == 0 {
		t.Fatal("blue-green operator barrier has no durable resource locks before restart")
	}
	server.restart(t)
	loginE2EUser(t, operatorName, operatorPassword)
	code, afterRestart := api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
	if code != 200 {
		t.Fatalf("read blue-green operator barrier after process restart → %d %v", code, afterRestart)
	}
	afterRestartRun := afterRestart["run"].(map[string]any)
	afterRestartEvents := afterRestart["events"].([]any)
	locksAfterRestart := countDeliveryLocks(t, deployLogID)
	if afterRestartRun["status"] != preRestartRun["status"] || afterRestartRun["current_stage"] != preRestartRun["current_stage"] || afterRestartRun["version"] != preRestartRun["version"] || len(afterRestartEvents) != len(preRestartEvents) || locksAfterRestart != locksBeforeRestart {
		t.Fatalf("restart changed durable operator barrier: before run=%v events=%d locks=%d, after run=%v events=%d locks=%d", preRestartRun, len(preRestartEvents), locksBeforeRestart, afterRestartRun, len(afterRestartEvents), locksAfterRestart)
	}
	server.mysql.SetAvailable(false)
	code, _ = api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
	if code >= 200 && code < 300 {
		t.Fatal("delivery run read succeeded while the E2E MySQL connection proxy was unavailable")
	}
	server.mysql.SetAvailable(true)
	reconnected := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		code, afterDBRecovery := api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
		if code == 200 {
			afterRestart = afterDBRecovery
			reconnected = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !reconnected {
		t.Fatal("backend did not reconnect to the E2E MySQL database after the isolated outage ended")
	}
	if afterRestart["run"].(map[string]any)["version"] != preRestartRun["version"] || len(afterRestart["events"].([]any)) != len(preRestartEvents) || countDeliveryLocks(t, deployLogID) != locksBeforeRestart {
		t.Fatalf("database outage changed delivery state or lock ownership: run=%v events=%d", afterRestart["run"], len(afterRestart["events"].([]any)))
	}
	runView = afterRestart

	// Drop the successful MySQL COMMIT response for a real operator action.
	// The first HTTP request must report an uncertain outcome; retrying the
	// same idempotency key must read the committed transition back into the
	// Store cache without appending a second event or releasing the lock.
	ackLost := server.mysql.ArmCommitAckLoss()
	beforeAckLossRun := runView["run"].(map[string]any)
	beforeAckLossEvents := runView["events"].([]any)
	ackLossVersion := responseID(t, beforeAckLossRun, "version")
	if got := responseID(t, beforeAckLossRun, "deploy_log_id"); got != deployLogID {
		t.Fatalf("delivery run log identity changed before MySQL acknowledgement fault: run=%d previous=%d", got, deployLogID)
	}
	if got := responseID(t, runView["deploy_log"].(map[string]any), "id"); got != deployLogID {
		t.Fatalf("delivery run view contains a different deploy log before MySQL acknowledgement fault: got=%d want=%d", got, deployLogID)
	}
	ackLossKey := fmt.Sprintf("e2e-mysql-commit-ack-%d", runID)
	ackLossAction := map[string]any{
		"action": "pause", "expected_version": ackLossVersion,
		"idempotency_key": ackLossKey,
	}
	firstStatus, firstBody, firstErr := apiRequest("POST", fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID), ackLossAction, "")
	if firstStatus == http.StatusNotFound {
		query := fmt.Sprintf("SELECT CONCAT(id, ':', deploy_log_id, ':', version, ':', status) FROM delivery_runs WHERE deploy_log_id = %d", deployLogID)
		rows, queryErr := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword, "--database", e2eDB, "-Nse", query).CombinedOutput()
		t.Fatalf("delivery action returned not-found before MySQL COMMIT (run=%v db rows=%q queryErr=%v body=%v); run likely missing from persistent transaction path", beforeAckLossRun, rows, queryErr, firstBody)
	}
	select {
	case <-ackLost:
	case <-time.After(30 * time.Second):
		t.Fatalf("MySQL proxy did not observe and drop a successful COMMIT acknowledgement; first response status=%d body=%v err=%v", firstStatus, firstBody, firstErr)
	}
	if firstErr != nil || (firstStatus >= 200 && firstStatus < 300) {
		t.Fatalf("first action should report an unknown commit result after its MySQL acknowledgement was dropped: status=%d body=%v err=%v", firstStatus, firstBody, firstErr)
	}
	code, recoveredAck := api(t, "POST", fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID), ackLossAction)
	if code != 200 {
		t.Fatalf("retry same action after lost MySQL acknowledgement → %d %v", code, recoveredAck)
	}
	recoveredRun := recoveredAck["run"].(map[string]any)
	recoveredEvents := recoveredAck["events"].([]any)
	if recoveredRun["status"] != "paused" || recoveredRun["version"] != float64(ackLossVersion+1) || len(recoveredEvents) != len(beforeAckLossEvents)+1 {
		t.Fatalf("idempotent MySQL acknowledgement recovery produced duplicate or stale state: run=%v events=%d (before=%d)", recoveredRun, len(recoveredEvents), len(beforeAckLossEvents))
	}
	if countDeliveryLocks(t, deployLogID) != locksBeforeRestart {
		t.Fatal("recovering a paused run after a lost MySQL acknowledgement changed its resource locks")
	}
	code, runView = applyDeliveryRunAction(t, runID, recoveredAck, "continue")
	if code != 200 || runView["run"].(map[string]any)["status"] != "running" || runView["run"].(map[string]any)["current_stage"] != "awaiting_candidate_review" {
		t.Fatalf("continue blue-green run after acknowledgement recovery → %d run=%v", code, runView["run"])
	}
	preCutoverEvents := runView["events"].([]any)

	// Wait until Kind has applied the Service selector patch, then terminate
	// the backend before it can receive or persist the Kubernetes response.
	kubeAPIProxy.ArmServicePatch(namespace, entryService)
	type cutoverResult struct {
		status int
		body   map[string]any
		err    error
	}
	cutoverDone := make(chan cutoverResult, 1)
	cutoverPath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID)
	cutoverVersion := responseID(t, runView["run"].(map[string]any), "version")
	go func() {
		status, body, err := apiRequest("POST", cutoverPath, map[string]any{
			"action": "confirm_cutover", "expected_version": cutoverVersion,
			"idempotency_key": fmt.Sprintf("e2e-cutover-crash-%d", runID),
		}, "")
		cutoverDone <- cutoverResult{status: status, body: body, err: err}
	}()
	select {
	case <-kubeAPIProxy.writeApplied:
	case <-time.After(45 * time.Second):
		t.Fatal("Kind did not apply the production Service cutover while the response fault was armed")
	}
	server.stopProcess()
	select {
	case result := <-cutoverDone:
		if result.err == nil {
			t.Fatalf("cutover HTTP request unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cutover HTTP request did not close after backend termination")
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	attentionView := waitForDeliveryStage(t, prodLogID, "needs_attention", 30*time.Second)
	attentionRun := attentionView["run"].(map[string]any)
	attentionEvents := attentionView["events"].([]any)
	locksAfterWriteCrash := countDeliveryLocks(t, deployLogID)
	if len(attentionEvents) != len(preCutoverEvents)+1 || locksAfterWriteCrash != locksBeforeRestart {
		t.Fatalf("write-interruption recovery did not preserve audit/locks: events=%d (before=%d) locks=%d (before=%d)", len(attentionEvents), len(preCutoverEvents), locksAfterWriteCrash, locksBeforeRestart)
	}
	planData := attentionView["plan"].(map[string]any)
	target := planData["snapshot"].(map[string]any)["target"].(map[string]any)
	expectedCandidate := target["candidate_label_value"].(string)
	selectorKey := target["candidate_label_key"].(string)
	waitForServiceSelector(t, namespace, entryService, selectorKey, expectedCandidate, 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)

	code, control := applyDeliveryRunAction(t, responseID(t, attentionRun, "id"), attentionView, "reconcile")
	if code != 200 {
		t.Fatalf("reconcile Service selector after lost cutover response → %d %v", code, control)
	}
	if control["run"].(map[string]any)["current_stage"] != "awaiting_live_verification" {
		t.Fatalf("reconcile did not advance to live verification: %v", control["run"])
	}
	actionResolved := false
	for _, raw := range control["actions"].([]any) {
		if action, ok := raw.(map[string]any); ok && action["action_key"] == "blue-green:entry:candidate" && action["status"] == "applied" {
			actionResolved = true
			break
		}
	}
	if !actionResolved {
		t.Fatalf("reconcile did not resolve persisted cutover intent: %v", control["actions"])
	}

	// Target the COMMIT that atomically promotes apps.deployment_name and the
	// active workload UID together with the terminal run event. Earlier commits
	// in this action only journal preview-Service cleanup and must pass through.
	confirmVersion := responseID(t, control["run"].(map[string]any), "version")
	confirmEventsBefore := control["events"].([]any)
	confirmKey := fmt.Sprintf("e2e-live-confirm-commit-ack-%d", runID)
	confirmAction := map[string]any{
		"action": "confirm_live_verification", "expected_version": confirmVersion,
		"idempotency_key": confirmKey,
	}
	confirmAckLost := server.mysql.ArmCommitAckLossAfterQuery("UPDATE delivery_bindings SET active_workload_cluster_id=")
	confirmPath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID)
	firstConfirmStatus, firstConfirmBody, firstConfirmErr := apiRequest("POST", confirmPath, confirmAction, "")
	select {
	case <-confirmAckLost:
	case <-time.After(45 * time.Second):
		t.Fatalf("MySQL proxy did not drop the workload-promotion COMMIT acknowledgement: status=%d body=%v err=%v", firstConfirmStatus, firstConfirmBody, firstConfirmErr)
	}
	if firstConfirmErr == nil && firstConfirmStatus >= 200 && firstConfirmStatus < 300 {
		t.Fatalf("live confirmation unexpectedly succeeded after its MySQL COMMIT acknowledgement was dropped: status=%d body=%v", firstConfirmStatus, firstConfirmBody)
	}
	query := fmt.Sprintf("SELECT CONCAT_WS('|',a.deployment_name,b.active_workload_name,b.active_workload_uid,r.status,r.current_stage,r.version,COUNT(e.id)) FROM apps a JOIN delivery_bindings b ON b.app_id=a.id JOIN delivery_runs r ON r.id=%d LEFT JOIN delivery_run_events e ON e.run_id=r.id WHERE a.id=%d GROUP BY a.id,b.active_workload_name,b.active_workload_uid,r.id", runID, prodAppID)
	committedState, queryErr := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword, "--database", e2eDB, "-Nse", query).CombinedOutput()
	if queryErr != nil {
		t.Fatalf("read committed live-promotion state after lost acknowledgement: %v\n%s", queryErr, committedState)
	}
	stateOutput := strings.Split(strings.TrimSpace(string(committedState)), "\n")
	fields := strings.Split(strings.TrimSpace(stateOutput[len(stateOutput)-1]), "|")
	if len(fields) != 7 {
		t.Fatalf("parse committed live-promotion state %q: expected 7 fields", committedState)
	}
	committedVersion, versionErr := strconv.ParseInt(fields[5], 10, 64)
	committedEvents, eventsErr := strconv.Atoi(fields[6])
	if versionErr != nil || eventsErr != nil || fields[0] != candidateDeployment || fields[1] != candidateDeployment || fields[2] != candidateDeploymentUID || fields[3] != "success" || fields[4] != "success" || committedVersion != confirmVersion+1 || committedEvents != len(confirmEventsBefore)+1 {
		t.Fatalf("workload promotion did not commit atomically after ACK loss: row=%q versionErr=%v eventsErr=%v", committedState, versionErr, eventsErr)
	}
	code, control = api(t, "POST", confirmPath, confirmAction)
	if code != 200 {
		t.Fatalf("retry same live confirmation after lost MySQL COMMIT acknowledgement → %d %v", code, control)
	}
	confirmedRun := control["run"].(map[string]any)
	confirmedEvents := control["events"].([]any)
	if confirmedRun["status"] != "success" || confirmedRun["current_stage"] != "success" || confirmedRun["version"] != float64(confirmVersion+1) || len(confirmedEvents) != len(confirmEventsBefore)+1 {
		t.Fatalf("idempotent live-confirmation recovery duplicated or missed the terminal event: run=%v events=%d (before=%d)", confirmedRun, len(confirmedEvents), len(confirmEventsBefore))
	}
	if countDeliveryLocks(t, deployLogID) != 0 {
		t.Fatal("successful live-confirmation retry did not refresh terminal resource-lock state")
	}
	waitForDeliveryStatus(t, prodLogID, "success", 90*time.Second)
	if apps := apiArray(t, fmt.Sprintf("/api/v1/apps?cluster_id=%d", prodClusterID)); len(apps) != 1 {
		t.Fatalf("production app binding count = %d, want one", len(apps))
	} else if app := apps[0].(map[string]any); app["deployment_name"] == prodDeployment {
		t.Fatal("production app still points at the pre-release Deployment after successful blue-green promotion")
	}
	if got := run(t, "kubectl", "get", "deployment", prodDeployment, "-n", namespace, "-o", "name"); got == "" {
		t.Fatal("stable Deployment was not retained after blue-green promotion")
	}
	runBlueGreenRecoveryE2E(t, server, namespace, appName, testDeployment, entryService, prodClusterID, runnerID, runnerKey, operatorName, operatorPassword)
}

func deploymentTemplateLabel(t *testing.T, namespace, deployment, key string) string {
	t.Helper()
	raw := run(t, "kubectl", "get", "deployment", deployment, "-n", namespace, "-o", "json")
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatalf("decode Deployment %s/%s while reading template label: %v", namespace, deployment, err)
	}
	labels, found, err := unstructured.NestedStringMap(object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		t.Fatalf("read Deployment %s/%s Pod template labels: found=%t err=%v", namespace, deployment, found, err)
	}
	return labels[key]
}

func countDeliveryLocks(t *testing.T, deployLogID int64) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM delivery_resource_locks WHERE deploy_log_id = " + strconv.FormatInt(deployLogID, 10)
	out, err := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword,
		"--database", e2eDB, "-Nse", query).CombinedOutput()
	if err != nil {
		t.Fatalf("read E2E delivery locks for log %d: %v\n%s", deployLogID, err, out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		t.Fatalf("parse E2E delivery lock count %q: no result", out)
	}
	count, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("parse E2E delivery lock count %q: %v", out, err)
	}
	return count
}

func e2eBusyboxArtifact(t *testing.T) (string, string) {
	return e2eImageArtifact(t, e2eBusyboxImage)
}

func e2eImageArtifact(t *testing.T, image string) (string, string) {
	t.Helper()
	if _, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		run(t, "docker", "pull", image)
	}
	raw := run(t, "docker", "image", "inspect", "--format={{json .RepoDigests}}", image)
	var refs []string
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		t.Fatalf("parse local repository digests for %s: %v (%s)", image, err, raw)
	}
	for _, ref := range refs {
		if repo, digest, ok := strings.Cut(ref, "@"); ok && repo == strings.Split(image, ":")[0] && strings.HasPrefix(digest, "sha256:") {
			return image, digest
		}
	}
	t.Fatalf("no repository digest found for %s: %v", image, refs)
	return "", ""
}

func runBlueGreenRecoveryE2E(t *testing.T, server *e2eServer, namespace, appName, testDeployment, entryService string, prodClusterID int, runnerID int64, runnerKey, operatorName, operatorPassword string) {
	t.Helper()
	image, digest := e2eImageArtifact(t, "docker.m.daocloud.io/library/busybox:1.37")
	code, event := apiWithBearer(t, "POST", fmt.Sprintf("/openapi/v1/runner/bindings/%d/events", runnerID), map[string]any{
		"pipeline_id": "781236", "build_number": "2", "image_full": image,
		"image_digest": digest, "commit_id": "e2e-blue-green-recovery",
	}, runnerKey)
	if code != 202 {
		t.Fatalf("Runner recovery build event → %d %v", code, event)
	}
	releaseID := responseID(t, event, "release_id")
	waitForRunnerRelease(t, runnerID, releaseID, runnerKey, "success", 3*time.Minute)
	waitForJSONPath(t, "deploy", testDeployment, "{.spec.template.spec.containers[0].image}", strings.Split(image, ":")[0]+"@"+digest, 30*time.Second)

	loginE2EUser(t, operatorName, operatorPassword)
	code, evidence := api(t, "POST", fmt.Sprintf("/api/v1/releases/%d/verification", releaseID), map[string]any{
		"digest": digest, "status": "passed", "kind": "manual",
		"summary":     "第二版测试镜像已就绪，开始验证切流后的稳定版本恢复。",
		"valid_until": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	})
	if code != 200 {
		t.Fatalf("record recovery release verification → %d %v", code, evidence)
	}

	stableDeployment := productionAppDeployment(t, prodClusterID, appName)
	loginE2EUser(t, "admin", e2ePassword)
	code, plan := api(t, "POST", "/api/v1/delivery-plans", map[string]any{
		"release_id": releaseID, "cluster_id": prodClusterID,
	})
	if code != 201 && code != 200 {
		t.Fatalf("create blue-green recovery plan → %d %v", code, plan)
	}
	planID := responseID(t, plan, "id")
	code, approval := api(t, "POST", fmt.Sprintf("/api/v1/delivery-plans/%d/request-approval", planID), map[string]any{
		"manual_review_acknowledged": true,
	})
	if code != 202 {
		t.Fatalf("request recovery plan approval → %d %v", code, approval)
	}
	requestID := responseID(t, approval, "request_id")
	loginE2EUser(t, operatorName, operatorPassword)
	code, approved := api(t, "POST", fmt.Sprintf("/api/v1/promotion-requests/%d/approve", requestID), nil)
	if code != 202 {
		t.Fatalf("approve recovery plan → %d %v", code, approved)
	}
	prodLogID := responseID(t, approved, "prod_log_id")
	runView := waitForDeliveryStage(t, prodLogID, "awaiting_candidate_review", 4*time.Minute)
	runID := responseID(t, runView["run"].(map[string]any), "id")
	code, control := applyDeliveryRunAction(t, runID, runView, "confirm_cutover")
	if code != 200 {
		t.Fatalf("cut over recovery candidate → %d %v", code, control)
	}
	planData := control["plan"].(map[string]any)
	target := planData["snapshot"].(map[string]any)["target"].(map[string]any)
	selectorKey := target["candidate_label_key"].(string)
	selectorValue := target["candidate_label_value"].(string)
	waitForServiceSelector(t, namespace, entryService, selectorKey, selectorValue, 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)

	// Let Kind apply the restore selector patch, then kill the backend before
	// its response/readback is journaled. Startup must keep the run fenced
	// until an operator re-reads the stable selector and resolves the intent.
	server.kubeAPI.ArmServicePatch(namespace, entryService)
	type restoreResult struct {
		status int
		body   map[string]any
		err    error
	}
	restoreDone := make(chan restoreResult, 1)
	restorePath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID)
	restoreVersion := responseID(t, control["run"].(map[string]any), "version")
	go func() {
		status, body, err := apiRequest("POST", restorePath, map[string]any{
			"action": "restore_stable", "expected_version": restoreVersion,
			"idempotency_key":                     fmt.Sprintf("e2e-restore-stable-crash-%d", runID),
			"recovery_compatibility_acknowledged": true,
		}, "")
		restoreDone <- restoreResult{status: status, body: body, err: err}
	}()
	select {
	case <-server.kubeAPI.writeApplied:
	case <-time.After(45 * time.Second):
		t.Fatal("Kind did not apply the stable Service restore while the response fault was armed")
	}
	server.stopProcess()
	select {
	case result := <-restoreDone:
		if result.err == nil {
			t.Fatalf("restore HTTP request unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restore HTTP request did not close after backend termination")
	}
	stableLabelKey := target["stable_label_key"].(string)
	waitForServiceSelector(t, namespace, entryService, stableLabelKey, "stable", 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	restoreAttention := waitForDeliveryAttention(t, prodLogID, 30*time.Second)
	if countDeliveryLocks(t, prodLogID) == 0 {
		t.Fatal("lost restore response released production locks before the operator verified it")
	}
	code, restored := applyDeliveryRunAction(t, runID, restoreAttention, "restore_stable")
	if code != 200 {
		t.Fatalf("reconcile already-applied stable restore after restart → %d %v", code, restored)
	}
	if restored["run"].(map[string]any)["status"] != "failed" || restored["run"].(map[string]any)["current_stage"] != "restored" {
		t.Fatalf("recovered stable restore has unexpected run state: %v", restored["run"])
	}
	restoreActionResolved := false
	for _, raw := range restored["actions"].([]any) {
		if action, ok := raw.(map[string]any); ok && action["action_key"] == "blue-green:entry:stable" && action["status"] == "applied" {
			restoreActionResolved = true
			break
		}
	}
	if !restoreActionResolved {
		t.Fatalf("operator restore did not resolve the persisted stable-selector intent: %v", restored["actions"])
	}
	waitForDeliveryStatus(t, prodLogID, "failed", 90*time.Second)
	waitForServiceSelector(t, namespace, entryService, stableLabelKey, "stable", 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)
	if got := productionAppDeployment(t, prodClusterID, appName); got != stableDeployment {
		t.Fatalf("restored production binding points at %q, want pre-cutover active Deployment %q", got, stableDeployment)
	}
	if countDeliveryLocks(t, prodLogID) != 0 {
		t.Fatal("completed stable restore retained active production resource locks")
	}
	runBlueGreenCandidateCleanupRecoveryE2E(t, server, namespace, stableDeployment, entryService, planID, prodLogID, runID, prodClusterID, appName, operatorName, operatorPassword)
	runBlueGreenCancelE2E(t, namespace, appName, testDeployment, entryService, prodClusterID, runnerID, runnerKey, operatorName, operatorPassword)
}

func runBlueGreenCandidateCleanupRecoveryE2E(t *testing.T, server *e2eServer, namespace, stableDeployment, entryService string, planID, deployLogID, runID int64, prodClusterID int, appName, operatorName, operatorPassword string) {
	t.Helper()
	candidateSuffix := "-bg-" + strconv.FormatInt(planID, 10)
	candidateDeployment := stableDeployment + candidateSuffix
	candidateService := entryService + candidateSuffix
	if got := run(t, "kubectl", "get", "deployment", candidateDeployment, "-n", namespace, "-o", "name"); got == "" {
		t.Fatal("restored blue-green candidate Deployment is missing before retention cleanup")
	}
	if got := run(t, "kubectl", "get", "service", candidateService, "-n", namespace, "-o", "name"); got == "" {
		t.Fatal("restored blue-green candidate preview Service is missing before retention cleanup")
	}
	stableUID := run(t, "kubectl", "get", "deployment", stableDeployment, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	// The real retention policy is 48 hours. Age only this random test row in
	// the isolated E2E database so the integration test can exercise cleanup.
	server.stopProcess()
	ageLogQuery := "UPDATE deploy_logs SET finished_at=DATE_SUB(UTC_TIMESTAMP(), INTERVAL 49 HOUR) WHERE id=" + strconv.FormatInt(deployLogID, 10)
	if out, err := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword, "--database", e2eDB, "-e", ageLogQuery).CombinedOutput(); err != nil {
		t.Fatalf("age isolated candidate cleanup retention row: %v\n%s", err, out)
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	code, view := api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
	if code != 200 {
		t.Fatalf("read restored run before candidate cleanup → %d %v", code, view)
	}
	runData := view["run"].(map[string]any)
	if runData["status"] != "failed" || runData["current_stage"] != "restored" {
		t.Fatalf("restored run is not eligible for candidate cleanup: %v", runData)
	}
	target := view["plan"].(map[string]any)["snapshot"].(map[string]any)["target"].(map[string]any)
	stableLabelKey := target["stable_label_key"].(string)
	version := responseID(t, runData, "version")
	server.kubeAPI.ArmCandidateServiceDelete(namespace, candidateService)
	type cleanupResult struct {
		status int
		body   map[string]any
		err    error
	}
	cleanupDone := make(chan cleanupResult, 1)
	cleanupPath := fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID)
	go func() {
		status, body, err := apiRequest("POST", cleanupPath, map[string]any{
			"action": "cleanup_candidate", "expected_version": version,
			"idempotency_key": fmt.Sprintf("e2e-candidate-cleanup-crash-%d", runID),
		}, "")
		cleanupDone <- cleanupResult{status: status, body: body, err: err}
	}()
	select {
	case <-server.kubeAPI.writeApplied:
	case <-time.After(45 * time.Second):
		t.Fatal("Kind did not apply the candidate preview Service delete while the response fault was armed")
	}
	server.stopProcess()
	select {
	case result := <-cleanupDone:
		if result.err == nil {
			t.Fatalf("candidate cleanup unexpectedly completed after backend termination: status=%d body=%v", result.status, result.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("candidate cleanup request did not close after backend termination")
	}
	if got := run(t, "kubectl", "get", "service", candidateService, "-n", namespace, "--ignore-not-found", "-o", "name"); got != "" {
		t.Fatalf("Kind did not persist candidate preview Service deletion before dropping the response: %q", got)
	}
	if got := run(t, "kubectl", "get", "deployment", candidateDeployment, "-n", namespace, "-o", "name"); got == "" {
		t.Fatal("candidate Deployment was deleted before preview Service cleanup was durably confirmed")
	}
	// Cleanup keeps a 15-minute lease when a Kubernetes write outcome is
	// uncertain. Expire only this run's rows in the isolated E2E DB so retry
	// exercises recovery immediately rather than sleeping through the lease.
	expireLeaseQuery := "UPDATE delivery_resource_locks SET expires_at=DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 SECOND) WHERE deploy_log_id=" + strconv.FormatInt(deployLogID, 10)
	if out, err := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword, "--database", e2eDB, "-e", expireLeaseQuery).CombinedOutput(); err != nil {
		t.Fatalf("expire isolated candidate cleanup lease: %v\n%s", err, out)
	}
	server.start(t)
	loginE2EUser(t, operatorName, operatorPassword)
	code, afterCrash := api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
	if code != 200 {
		t.Fatalf("read restored run after candidate cleanup interruption → %d %v", code, afterCrash)
	}
	crashRun := afterCrash["run"].(map[string]any)
	if crashRun["status"] != "failed" || crashRun["current_stage"] != "restored" || crashRun["version"] != float64(version) {
		t.Fatalf("candidate cleanup interruption changed the terminal run state: %v", crashRun)
	}
	code, cleaned := api(t, "POST", cleanupPath, map[string]any{
		"action": "cleanup_candidate", "expected_version": version,
		"idempotency_key": fmt.Sprintf("e2e-candidate-cleanup-resume-%d", runID),
	})
	if code == 409 {
		// The Service delete was already absent and is now confirmed. The
		// resumed Deployment delete can be accepted while its Pods are still
		// terminating, so wait for Kubernetes to finish before the next readback.
		run(t, "kubectl", "wait", "--for=delete", "deployment/"+candidateDeployment, "-n", namespace, "--timeout=120s")
		server.stopProcess()
		expireLeaseQuery = "UPDATE delivery_resource_locks SET expires_at=DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 SECOND) WHERE deploy_log_id=" + strconv.FormatInt(deployLogID, 10)
		if out, err := exec.Command("docker", "exec", e2eMySQLContainer, "mysql", "-uroot", "-p"+e2eMySQLRootPassword, "--database", e2eDB, "-e", expireLeaseQuery).CombinedOutput(); err != nil {
			t.Fatalf("expire isolated candidate cleanup retry lease: %v\n%s", err, out)
		}
		server.start(t)
		loginE2EUser(t, operatorName, operatorPassword)
		var afterDelete map[string]any
		code, afterDelete = api(t, "GET", fmt.Sprintf("/api/v1/delivery-runs/%d", runID), nil)
		if code != 200 {
			t.Fatalf("read restored run after Kubernetes finished candidate deletion → %d %v", code, afterDelete)
		}
		version = responseID(t, afterDelete["run"].(map[string]any), "version")
		code, cleaned = api(t, "POST", cleanupPath, map[string]any{
			"action": "cleanup_candidate", "expected_version": version,
			"idempotency_key": fmt.Sprintf("e2e-candidate-cleanup-finish-%d", runID),
		})
	}
	if code != 200 {
		t.Fatalf("resume candidate cleanup after lost Kubernetes response → %d %v", code, cleaned)
	}
	if got := run(t, "kubectl", "get", "service", candidateService, "-n", namespace, "--ignore-not-found", "-o", "name"); got != "" {
		t.Fatalf("candidate preview Service remains after cleanup recovery: %s", got)
	}
	if got := run(t, "kubectl", "get", "deployment", candidateDeployment, "-n", namespace, "--ignore-not-found", "-o", "name"); got != "" {
		t.Fatalf("candidate Deployment remains after cleanup recovery: %s", got)
	}
	if got := run(t, "kubectl", "get", "deployment", stableDeployment, "-n", namespace, "-o", "jsonpath={.metadata.uid}"); got != stableUID {
		t.Fatalf("candidate cleanup changed stable Deployment UID: before=%s after=%s", stableUID, got)
	}
	if got := productionAppDeployment(t, prodClusterID, appName); got != stableDeployment {
		t.Fatalf("candidate cleanup changed active application binding to %q, want %q", got, stableDeployment)
	}
	waitForServiceSelector(t, namespace, entryService, stableLabelKey, "stable", 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)
	for _, raw := range cleaned["actions"].([]any) {
		action := raw.(map[string]any)
		if (action["action_key"] == "blue-green:cleanup:candidate-service" || action["action_key"] == "blue-green:cleanup:candidate-deployment") && action["status"] != "applied" {
			t.Fatalf("cleanup retry did not confirm candidate deletion action: %v", action)
		}
	}
}

func runBlueGreenCancelE2E(t *testing.T, namespace, appName, testDeployment, entryService string, prodClusterID int, runnerID int64, runnerKey, operatorName, operatorPassword string) {
	t.Helper()
	image, digest := e2eBusyboxArtifact(t)
	code, event := apiWithBearer(t, "POST", fmt.Sprintf("/openapi/v1/runner/bindings/%d/events", runnerID), map[string]any{
		"pipeline_id": "781236", "build_number": "3", "image_full": image,
		"image_digest": digest, "commit_id": "e2e-blue-green-cancel",
	}, runnerKey)
	if code != 202 {
		t.Fatalf("Runner cancellation build event → %d %v", code, event)
	}
	releaseID := responseID(t, event, "release_id")
	waitForRunnerRelease(t, runnerID, releaseID, runnerKey, "success", 3*time.Minute)
	waitForJSONPath(t, "deploy", testDeployment, "{.spec.template.spec.containers[0].image}", strings.Split(image, ":")[0]+"@"+digest, 30*time.Second)

	loginE2EUser(t, operatorName, operatorPassword)
	code, evidence := api(t, "POST", fmt.Sprintf("/api/v1/releases/%d/verification", releaseID), map[string]any{
		"digest": digest, "status": "passed", "kind": "manual",
		"summary":     "第三版测试镜像已就绪，开始验证切流后的取消恢复。",
		"valid_until": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	})
	if code != 200 {
		t.Fatalf("record cancellation release verification → %d %v", code, evidence)
	}
	stableDeployment := productionAppDeployment(t, prodClusterID, appName)

	loginE2EUser(t, "admin", e2ePassword)
	code, plan := api(t, "POST", "/api/v1/delivery-plans", map[string]any{
		"release_id": releaseID, "cluster_id": prodClusterID,
	})
	if code != 201 && code != 200 {
		t.Fatalf("create blue-green cancellation plan → %d %v", code, plan)
	}
	planID := responseID(t, plan, "id")
	code, approval := api(t, "POST", fmt.Sprintf("/api/v1/delivery-plans/%d/request-approval", planID), map[string]any{
		"manual_review_acknowledged": true,
	})
	if code != 202 {
		t.Fatalf("request cancellation plan approval → %d %v", code, approval)
	}
	requestID := responseID(t, approval, "request_id")
	loginE2EUser(t, operatorName, operatorPassword)
	code, approved := api(t, "POST", fmt.Sprintf("/api/v1/promotion-requests/%d/approve", requestID), nil)
	if code != 202 {
		t.Fatalf("approve cancellation plan → %d %v", code, approved)
	}
	prodLogID := responseID(t, approved, "prod_log_id")
	runView := waitForDeliveryStage(t, prodLogID, "awaiting_candidate_review", 4*time.Minute)
	runID := responseID(t, runView["run"].(map[string]any), "id")
	code, control := applyDeliveryRunAction(t, runID, runView, "confirm_cutover")
	if code != 200 {
		t.Fatalf("cut over cancellation candidate → %d %v", code, control)
	}
	target := control["plan"].(map[string]any)["snapshot"].(map[string]any)["target"].(map[string]any)
	waitForServiceSelector(t, namespace, entryService, target["candidate_label_key"].(string), target["candidate_label_value"].(string), 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)

	code, cancelled := applyDeliveryRunAction(t, runID, control, "cancel")
	if code != 200 {
		t.Fatalf("cancel after candidate cutover → %d %v", code, cancelled)
	}
	waitForDeliveryStatus(t, prodLogID, "cancelled", 90*time.Second)
	waitForServiceSelector(t, namespace, entryService, target["stable_label_key"].(string), "stable", 30*time.Second)
	assertServiceHasReadyEndpoint(t, namespace, entryService)
	if got := productionAppDeployment(t, prodClusterID, appName); got != stableDeployment {
		t.Fatalf("cancelled production binding points at %q, want stable Deployment %q", got, stableDeployment)
	}
}

func productionAppDeployment(t *testing.T, clusterID int, appName string) string {
	t.Helper()
	apps := apiArray(t, fmt.Sprintf("/api/v1/apps?cluster_id=%d", clusterID))
	for _, raw := range apps {
		app, ok := raw.(map[string]any)
		if ok && app["app_name"] == appName {
			deployment, _ := app["deployment_name"].(string)
			if deployment != "" {
				return deployment
			}
		}
	}
	t.Fatalf("production binding for %s missing in cluster %d", appName, clusterID)
	return ""
}

func createE2EApp(t *testing.T, clusterID int, appName, deployment, image, namespace string) int64 {
	t.Helper()
	code, app := api(t, "POST", "/api/v1/apps", map[string]any{
		"cluster_id": clusterID, "app_name": appName,
		"match_keyword": appName, "image_repository": strings.Split(image, ":")[0],
		"deployment_name": deployment, "container_name": "web", "namespace": namespace,
		"source_kind": "third_party",
	})
	if code != 200 {
		t.Fatalf("create app mapping %d/%s → %d %v", clusterID, appName, code, app)
	}
	return responseID(t, app, "id")
}

func loginE2EUser(t *testing.T, username, password string) {
	t.Helper()
	code, out := api(t, "POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password})
	if code != 200 {
		t.Fatalf("login as %s → %d %v", username, code, out)
	}
}

func responseID(t *testing.T, out map[string]any, key string) int64 {
	t.Helper()
	switch value := out[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		t.Fatalf("response field %q is not an ID: %v", key, out)
		return 0
	}
}

func waitForRunnerRelease(t *testing.T, bindingID, releaseID int64, key, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		code, out := apiWithBearer(t, "GET", fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d", bindingID, releaseID), nil, key)
		if code == 200 {
			if out["status"] == want {
				return
			}
			if out["status"] == "failed" {
				t.Fatalf("Runner test deployment failed: %v", out)
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Runner release %d did not reach %q", releaseID, want)
}

func waitForDeliveryStage(t *testing.T, deployLogID int64, want string, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	path := fmt.Sprintf("/api/v1/delivery-runs/by-deploy-log/%d", deployLogID)
	for time.Now().Before(deadline) {
		code, out := api(t, "GET", path, nil)
		if code == 200 {
			runData, _ := out["run"].(map[string]any)
			if runData["current_stage"] == want {
				return out
			}
			if runData["status"] == "failed" {
				t.Fatalf("blue-green run failed before %s: %v", want, out)
			}
			if runData["current_stage"] == "needs_attention" {
				events, _ := out["events"].([]any)
				t.Fatalf("blue-green run needs operator attention before %s: events=%v", want, events)
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("delivery run for log %d did not reach %s", deployLogID, want)
	return nil
}

func waitForDeliveryAttention(t *testing.T, deployLogID int64, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	path := fmt.Sprintf("/api/v1/delivery-runs/by-deploy-log/%d", deployLogID)
	for time.Now().Before(deadline) {
		code, out := api(t, "GET", path, nil)
		if code == 200 {
			runData, _ := out["run"].(map[string]any)
			if runData["current_stage"] == "needs_attention" {
				return out
			}
			if runData["status"] == "failed" {
				t.Fatalf("blue-green run failed before recovery attention: %v", out)
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("delivery run for log %d did not reach needs_attention", deployLogID)
	return nil
}

func applyDeliveryRunAction(t *testing.T, runID int64, view map[string]any, action string) (int, map[string]any) {
	t.Helper()
	runData := view["run"].(map[string]any)
	version := responseID(t, runData, "version")
	payload := map[string]any{
		"action": action, "expected_version": version,
		"idempotency_key": fmt.Sprintf("e2e-%s-%d-%d", action, runID, version),
	}
	if action == "restore_stable" || action == "cancel" {
		payload["recovery_compatibility_acknowledged"] = true
	}
	return api(t, "POST", fmt.Sprintf("/api/v1/delivery-runs/%d/actions", runID), payload)
}

func waitForServiceSelector(t *testing.T, namespace, serviceName, key, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var service map[string]any
		if raw, err := exec.Command("kubectl", "get", "service", serviceName, "-n", namespace, "-o", "json").CombinedOutput(); err == nil && json.Unmarshal(raw, &service) == nil {
			spec, _ := service["spec"].(map[string]any)
			selector, _ := spec["selector"].(map[string]any)
			if selector[key] == want {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Service %s did not select candidate %s=%s", serviceName, key, want)
}

func assertServiceHasReadyEndpoint(t *testing.T, namespace, serviceName string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := exec.Command("kubectl", "get", "endpointslices", "-n", namespace,
			"-l", "kubernetes.io/service-name="+serviceName, "-o", "json").CombinedOutput()
		if err == nil {
			var list struct {
				Items []struct {
					Endpoints []struct {
						Conditions struct {
							Ready *bool `json:"ready"`
						} `json:"conditions"`
					} `json:"endpoints"`
				} `json:"items"`
			}
			if json.Unmarshal(raw, &list) == nil {
				for _, slice := range list.Items {
					for _, endpoint := range slice.Endpoints {
						if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
							return
						}
					}
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Service %s has no ready EndpointSlice target", serviceName)
}

func waitForDeliveryStatus(t *testing.T, deployLogID int64, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	path := fmt.Sprintf("/api/v1/delivery-runs/by-deploy-log/%d", deployLogID)
	for time.Now().Before(deadline) {
		code, out := api(t, "GET", path, nil)
		if code == 200 {
			if runData, ok := out["run"].(map[string]any); ok && runData["status"] == want {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("delivery run for log %d did not reach status %s", deployLogID, want)
}

// kubectlApply pipes a manifest to kubectl apply -f -.
func kubectlApply(t *testing.T, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, out)
	}
}

func waitForPods(t *testing.T, selector string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out := run(t, "kubectl", "get", "pods", "-n", e2eNS, "-l", selector, "-o", "jsonpath={.items[*].status.phase}")
		if strings.Contains(out, "Running") {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("pods %s never reached Running", selector)
}

func waitForJSONPath(t *testing.T, kind, name, jsonPath, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		got := run(t, "kubectl", "get", kind, name, "-n", e2eNS, "-o", "jsonpath="+jsonPath)
		if got == want {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s/%s %s never became %q", kind, name, jsonPath, want)
}

// execEcho opens the WS terminal, sends `echo <mark>`, and expects the
// marker back on the stdout channel — stdin→exec→stdout against the real
// apiserver.
func execEcho(t *testing.T, clusterID int, pod string) {
	t.Helper()
	mark := "kbtest-e2e-ok"
	u := "ws" + strings.TrimPrefix(e2eAddr, "http") +
		fmt.Sprintf("/api/v1/k8s/pods/%s/terminal?cluster_id=%d&namespace=%s&command=sh&tty=true",
			pod, clusterID, e2eNS)
	h := http.Header{}
	// The session cookie is scoped Path=/api — the jar only returns it
	// for URLs under that path, so look it up against the API prefix.
	for _, c := range httpClient.Jar.Cookies(mustParse(t, e2eAddr+"/api")) {
		h.Add("Cookie", c.Name+"="+c.Value)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, h)
	if err != nil {
		code := -1
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("terminal dial: %v (http %d)", err, code)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, []byte("echo "+mark+"\r")...)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("terminal read: %v", err)
		}
		if len(data) > 1 && (data[0] == 1 || data[0] == 2) && bytes.Contains(data[1:], []byte(mark)) {
			return
		}
	}
	t.Fatal("exec echo marker never arrived")
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
