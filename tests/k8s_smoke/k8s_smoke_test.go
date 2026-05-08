package k8s_smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	repoRoot        string
	borgBinary      string
	smokeHTTPClient = &http.Client{Timeout: 5 * time.Second}
)

func TestMain(m *testing.M) {
	var err error
	repoRoot, err = findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	tempDir, err := os.MkdirTemp("", "borg-k8s-smoke-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}

	borgBinary = filepath.Join(tempDir, "borg-go")
	cmd := exec.Command("go", "build", "-o", borgBinary, "./cmd/borg")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build ./cmd/borg: %v\n%s\n", err, output)
		_ = os.RemoveAll(tempDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

func TestAnnotationDiscoveryAndSelectorRequest(t *testing.T) {
	ctx := newSmokeContext(t, nil)
	ctx.kube.setPods([]pod{
		newPod("annotated", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "vllm",
		}, map[string]string{
			"borg/models":  "alpha,beta",
			"borg/apiport": ctx.upstream.port(),
		}),
		newPod("not-selected", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "other",
		}, map[string]string{
			"borg/models":  "hidden",
			"borg/apiport": ctx.upstream.port(),
		}),
	})

	models := waitForModels(t, ctx.proxy, setOf("alpha", "beta"), setOf("hidden"))
	assertSetEqual(t, models, setOf("alpha", "beta"))
	if !ctx.kube.hasRequest("models", "borg/expose=vllm") {
		t.Fatalf("fake Kubernetes did not record expected selector request; got %#v", ctx.kube.requests())
	}
}

func TestAutomodelDiscoveryQueriesUpstream(t *testing.T) {
	ctx := newSmokeContext(t, []string{"auto-alpha", "auto-beta"})
	ctx.kube.setPods([]pod{
		newPod("automodel", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "vllm",
		}, map[string]string{
			"borg/apiport": ctx.upstream.port(),
		}),
	})

	models := waitForModels(t, ctx.proxy, setOf("auto-alpha", "auto-beta"), nil)
	assertSetEqual(t, models, setOf("auto-alpha", "auto-beta"))

	modelRequests := ctx.upstream.modelRequests()
	if len(modelRequests) == 0 {
		t.Fatal("dummy upstream did not record a /v1/models request")
	}
	last := modelRequests[len(modelRequests)-1]
	if last.Path != "/v1/models" {
		t.Fatalf("model request path = %q, want /v1/models", last.Path)
	}
	if got := last.Headers["authorization"]; got != "Bearer EMPTY" {
		t.Fatalf("model request authorization = %q, want Bearer EMPTY", got)
	}
}

func TestSuccessfulRefreshRemovesMissingPods(t *testing.T) {
	ctx := newSmokeContext(t, nil)
	ctx.kube.setPods([]pod{
		newPod("temporary", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "vllm",
		}, map[string]string{
			"borg/models":  "temporary-model",
			"borg/apiport": ctx.upstream.port(),
		}),
	})
	waitForModels(t, ctx.proxy, setOf("temporary-model"), nil)

	ctx.kube.setPods(nil)
	models := waitForModels(t, ctx.proxy, nil, setOf("temporary-model"))
	assertSetEqual(t, models, nil)
}

func TestFailedRefreshPreservesLastSuccessfulSnapshot(t *testing.T) {
	ctx := newSmokeContext(t, nil)
	ctx.kube.setPods([]pod{
		newPod("stable", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "vllm",
		}, map[string]string{
			"borg/models":  "stable-model",
			"borg/apiport": ctx.upstream.port(),
		}),
	})
	waitForModels(t, ctx.proxy, setOf("stable-model"), nil)

	ctx.kube.setPods(nil)
	ctx.kube.setFailLists(true)
	waitForKubeFailures(t, ctx.kube)
	models, err := modelIDs(ctx.proxy)
	if err != nil {
		t.Fatalf("list models after failed refresh: %v\n%s", err, ctx.proxy.logs())
	}
	if _, ok := models["stable-model"]; !ok {
		t.Fatalf("stable-model disappeared after failed refresh; got %#v\n%s", models, ctx.proxy.logs())
	}

	ctx.kube.setFailLists(false)
	models = waitForModels(t, ctx.proxy, nil, setOf("stable-model"))
	assertSetEqual(t, models, nil)
}

func TestEndpointAnnotationOverridesAreUsedForForwarding(t *testing.T) {
	ctx := newSmokeContext(t, nil)
	ctx.kube.setPods([]pod{
		newPod("with-base", "models", ctx.upstream.host(), map[string]string{
			"borg/expose": "vllm",
		}, map[string]string{
			"borg/models":   "override-model",
			"borg/protocol": "http",
			"borg/apiport":  ctx.upstream.port(),
			"borg/apibase":  "/openai",
		}),
	})
	waitForModels(t, ctx.proxy, setOf("override-model"), nil)

	response := postJSON(t, ctx.proxy.url+"/v1/chat/completions", []byte(`{"model":"override-model","messages":[]}`))
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("proxy POST status = %d, want 200; body=%s\n%s", response.StatusCode, body, ctx.proxy.logs())
	}

	var payload struct {
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode proxy response: %v", err)
	}
	if payload.Upstream != "ok" {
		t.Fatalf("proxy response upstream = %q, want ok", payload.Upstream)
	}

	record := ctx.upstream.lastRecord(t)
	if record.Path != "/openai/v1/chat/completions" {
		t.Fatalf("upstream POST path = %q, want /openai/v1/chat/completions", record.Path)
	}
	if got := record.Headers["authorization"]; got != "Bearer EMPTY" {
		t.Fatalf("upstream authorization = %q, want Bearer EMPTY", got)
	}
}

type smokeContext struct {
	kube     *fakeKubernetesAPI
	upstream *dummyUpstream
	proxy    *runningProxy
}

func newSmokeContext(t *testing.T, upstreamModels []string) *smokeContext {
	t.Helper()
	if len(upstreamModels) == 0 {
		upstreamModels = []string{"smoke"}
	}

	tempDir := t.TempDir()
	kube := newFakeKubernetesAPI()
	upstream := newDummyUpstream(upstreamModels)
	ctx := &smokeContext{
		kube:     kube,
		upstream: upstream,
	}
	t.Cleanup(func() {
		if ctx.proxy != nil {
			ctx.proxy.terminate()
		}
		upstream.close()
		kube.close()
	})

	kubeconfig := writeKubeconfig(t, tempDir, kube)
	config := writeBorgConfig(t, tempDir)
	ctx.proxy = runGoProxy(t, tempDir, config, kubeconfig)
	return ctx
}

type dummyUpstream struct {
	server    *httptest.Server
	models    []string
	mu        sync.Mutex
	records   []requestRecord
	modelReqs []requestRecord
}

type requestRecord struct {
	Method  string
	Path    string
	Query   string
	Headers map[string]string
	Body    any
}

func newDummyUpstream(models []string) *dummyUpstream {
	upstream := &dummyUpstream{models: append([]string(nil), models...)}
	upstream.server = newLocalHTTPServer(http.HandlerFunc(upstream.handle))
	return upstream
}

func (d *dummyUpstream) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models"):
		headers := lowerHeaders(r.Header)
		d.recordModelRequest(requestRecord{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Headers: headers,
		})

		data := make([]map[string]any, 0, len(d.models))
		for _, model := range d.models {
			data = append(data, map[string]any{
				"id":       model,
				"object":   "model",
				"created":  nil,
				"owned_by": "k8s-smoke",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   data,
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "read body"})
			return
		}
		headers := lowerHeaders(r.Header)
		var body any
		if err := json.Unmarshal(rawBody, &body); err != nil {
			body = nil
		}
		record := requestRecord{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Headers: headers,
			Body:    body,
		}
		d.record(record)
		writeJSON(w, http.StatusOK, map[string]any{
			"upstream": "ok",
			"path":     r.URL.Path,
			"auth":     headers["authorization"],
			"body":     body,
		})
	default:
		http.NotFound(w, r)
	}
}

func (d *dummyUpstream) host() string {
	host, _, err := net.SplitHostPort(d.mustURL().Host)
	if err != nil {
		panic(err)
	}
	return host
}

func (d *dummyUpstream) port() string {
	_, port, err := net.SplitHostPort(d.mustURL().Host)
	if err != nil {
		panic(err)
	}
	return port
}

func (d *dummyUpstream) mustURL() *url.URL {
	parsed, err := url.Parse(d.server.URL)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (d *dummyUpstream) close() {
	d.server.Close()
}

func (d *dummyUpstream) record(record requestRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, record)
}

func (d *dummyUpstream) recordModelRequest(record requestRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.modelReqs = append(d.modelReqs, record)
}

func (d *dummyUpstream) lastRecord(t *testing.T) requestRecord {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.records) == 0 {
		t.Fatal("dummy upstream did not record a POST")
	}
	return cloneRecord(d.records[len(d.records)-1])
}

func (d *dummyUpstream) modelRequests() []requestRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	records := make([]requestRecord, 0, len(d.modelReqs))
	for _, record := range d.modelReqs {
		records = append(records, cloneRecord(record))
	}
	return records
}

type fakeKubernetesAPI struct {
	server             *httptest.Server
	mu                 sync.Mutex
	pods               []pod
	listRequests       []listRequest
	failLists          bool
	failedRequestCount int
}

type listRequest struct {
	Namespace string
	Selector  string
}

func newFakeKubernetesAPI() *fakeKubernetesAPI {
	kube := &fakeKubernetesAPI{}
	kube.server = newLocalHTTPServer(http.HandlerFunc(kube.handle))
	return kube
}

func (f *fakeKubernetesAPI) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	namespace, ok := namespaceFromPodListPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	selector := r.URL.Query().Get("labelSelector")
	f.recordRequest(namespace, selector)
	if f.shouldFailLists() {
		f.recordFailedRequest()
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"kind":       "Status",
			"apiVersion": "v1",
			"status":     "Failure",
			"message":    "forced fake Kubernetes failure",
		})
		return
	}

	var items []pod
	for _, candidate := range f.currentPods() {
		if candidate.Metadata.Namespace == namespace && matchesSelector(candidate, selector) {
			items = append(items, candidate)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       "PodList",
		"apiVersion": "v1",
		"metadata":   map[string]string{"resourceVersion": "1"},
		"items":      items,
	})
}

func (f *fakeKubernetesAPI) url() string {
	return f.server.URL
}

func (f *fakeKubernetesAPI) close() {
	f.server.Close()
}

func (f *fakeKubernetesAPI) setPods(pods []pod) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods = append([]pod(nil), pods...)
}

func (f *fakeKubernetesAPI) currentPods() []pod {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pod(nil), f.pods...)
}

func (f *fakeKubernetesAPI) setFailLists(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failLists = fail
}

func (f *fakeKubernetesAPI) shouldFailLists() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failLists
}

func (f *fakeKubernetesAPI) recordRequest(namespace string, selector string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listRequests = append(f.listRequests, listRequest{Namespace: namespace, Selector: selector})
}

func (f *fakeKubernetesAPI) requests() []listRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]listRequest(nil), f.listRequests...)
}

func (f *fakeKubernetesAPI) hasRequest(namespace string, selector string) bool {
	for _, request := range f.requests() {
		if request.Namespace == namespace && request.Selector == selector {
			return true
		}
	}
	return false
}

func (f *fakeKubernetesAPI) recordFailedRequest() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failedRequestCount++
}

func (f *fakeKubernetesAPI) failureCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failedRequestCount
}

type pod struct {
	Kind       string      `json:"kind"`
	APIVersion string      `json:"apiVersion"`
	Metadata   podMetadata `json:"metadata"`
	Status     podStatus   `json:"status"`
}

type podMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

type podStatus struct {
	Phase string `json:"phase"`
	PodIP string `json:"podIP"`
}

func newPod(name string, namespace string, podIP string, labels map[string]string, annotations map[string]string) pod {
	return pod{
		Kind:       "Pod",
		APIVersion: "v1",
		Metadata: podMetadata{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Status: podStatus{
			Phase: "Running",
			PodIP: podIP,
		},
	}
}

type runningProxy struct {
	url        string
	cmd        *exec.Cmd
	stdoutPath string
	stderrPath string
	done       chan error

	mu      sync.Mutex
	exited  bool
	waitErr error
}

func runGoProxy(t *testing.T, tempDir string, config string, kubeconfig string) *runningProxy {
	t.Helper()

	port := freePort(t)
	stdoutPath := filepath.Join(tempDir, "borg-go.stdout.log")
	stderrPath := filepath.Join(tempDir, "borg-go.stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout log: %v", err)
	}
	defer stdout.Close()
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create stderr log: %v", err)
	}
	defer stderr.Close()

	cmd := exec.Command(
		borgBinary,
		"--config", config,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
	)
	cmd.Dir = repoRoot
	cmd.Env = goProxyEnv(kubeconfig)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start Go proxy: %v", err)
	}

	proxy := &runningProxy{
		url:        fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:        cmd,
		stdoutPath: stdoutPath,
		stderrPath: stderrPath,
		done:       make(chan error, 1),
	}
	go func() {
		proxy.done <- cmd.Wait()
	}()

	waitUntilReady(t, proxy)
	return proxy
}

func (p *runningProxy) pollExit() bool {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return true
	}
	p.mu.Unlock()

	select {
	case err := <-p.done:
		p.setExited(err)
		return true
	default:
		return false
	}
}

func (p *runningProxy) setExited(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exited = true
	p.waitErr = err
}

func (p *runningProxy) exitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *runningProxy) terminate() {
	if p.pollExit() {
		return
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case err := <-p.done:
		p.setExited(err)
		return
	case <-time.After(5 * time.Second):
	}

	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case err := <-p.done:
		p.setExited(err)
	case <-time.After(5 * time.Second):
		p.setExited(fmt.Errorf("process did not exit after kill"))
	}
}

func (p *runningProxy) logs() string {
	return fmt.Sprintf("stdout:\n%s\n\nstderr:\n%s", readLog(p.stdoutPath), readLog(p.stderrPath))
}

func writeBorgConfig(t *testing.T, tempDir string) string {
	t.Helper()
	path := filepath.Join(tempDir, "config.yaml")
	config := []byte(`borg:
  auth_key: "EMPTY"
  auth_prefix: "PROXY:"
  update_interval: 1
  instances: []
  k8s_discover:
    - namespace: "models"
      selector: "borg/expose=vllm"
      modelkey: "borg/models"
`)
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeKubeconfig(t *testing.T, tempDir string, kube *fakeKubernetesAPI) string {
	t.Helper()
	path := filepath.Join(tempDir, "kubeconfig.yaml")
	config := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: %q
    insecure-skip-tls-verify: true
contexts:
- name: fake
  context:
    cluster: fake
    user: fake
current-context: fake
users:
- name: fake
  user: {}
`, kube.url())
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func goProxyEnv(kubeconfig string) []string {
	scrub := map[string]struct{}{
		"AUTH_KEY":                {},
		"BORG_AUTH_KEY":           {},
		"PROXY_CONFIG":            {},
		"PORT":                    {},
		"KUBECONFIG":              {},
		"KUBERNETES_SERVICE_HOST": {},
		"KUBERNETES_SERVICE_PORT": {},
	}

	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := scrub[key]; ok {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"KUBECONFIG="+kubeconfig,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
	return env
}

func waitUntilReady(t *testing.T, proxy *runningProxy) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if proxy.pollExit() {
			t.Fatalf("Go proxy exited before readiness: %v\n%s", proxy.exitErr(), proxy.logs())
		}
		response, err := client.Get(proxy.url + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	proxy.terminate()
	t.Fatalf("Go proxy did not become ready\n%s", proxy.logs())
}

func waitForModels(t *testing.T, proxy *runningProxy, include map[string]struct{}, absent map[string]struct{}) map[string]struct{} {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var lastModels map[string]struct{}
	var lastErr error

	for time.Now().Before(deadline) {
		if proxy.pollExit() {
			t.Fatalf("Go proxy exited while waiting for models: %v\n%s", proxy.exitErr(), proxy.logs())
		}

		lastModels, lastErr = modelIDs(proxy)
		if lastErr == nil && includesAll(lastModels, include) && excludesAll(lastModels, absent) {
			return lastModels
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for models include=%v absent=%v; last models=%v; last err=%v\n%s", setKeys(include), setKeys(absent), setKeys(lastModels), lastErr, proxy.logs())
	return nil
}

func waitForKubeFailures(t *testing.T, kube *fakeKubernetesAPI) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if kube.failureCount() > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for fake Kubernetes list failure")
}

func modelIDs(proxy *runningProxy) (map[string]struct{}, error) {
	response, err := smokeHTTPClient.Get(proxy.url + "/v1/models")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, body)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}

	models := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		models[item.ID] = struct{}{}
	}
	return models, nil
}

func postJSON(t *testing.T, requestURL string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build POST request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := smokeHTTPClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", requestURL, err)
	}
	return response
}

func namespaceFromPodListPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "namespaces" && parts[4] == "pods" {
		return parts[3], true
	}
	return "", false
}

func matchesSelector(pod pod, selector string) bool {
	if selector == "" {
		return true
	}

	for _, requirement := range strings.Split(selector, ",") {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		key, value, ok := strings.Cut(requirement, "=")
		if !ok {
			return false
		}
		if pod.Metadata.Labels[strings.TrimSpace(key)] != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func lowerHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		out[strings.ToLower(key)] = value
	}
	return out
}

func cloneRecord(record requestRecord) requestRecord {
	clone := record
	clone.Headers = make(map[string]string, len(record.Headers))
	for key, value := range record.Headers {
		clone.Headers[key] = value
	}
	return clone
}

func writeJSON(w http.ResponseWriter, statusCode int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(raw)
}

func newLocalHTTPServer(handler http.Handler) *httptest.Server {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("httptest listen on 127.0.0.1: %v", err))
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func findRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate smoke test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("locate repo root from %s: %w", file, err)
	}
	return root, nil
}

func readLog(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(raw) > 4000 {
		raw = raw[len(raw)-4000:]
	}
	return string(raw)
}

func setOf(values ...string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func includesAll(haystack map[string]struct{}, needles map[string]struct{}) bool {
	for needle := range needles {
		if _, ok := haystack[needle]; !ok {
			return false
		}
	}
	return true
}

func excludesAll(haystack map[string]struct{}, needles map[string]struct{}) bool {
	for needle := range needles {
		if _, ok := haystack[needle]; ok {
			return false
		}
	}
	return true
}

func assertSetEqual(t *testing.T, got map[string]struct{}, want map[string]struct{}) {
	t.Helper()
	if got == nil {
		got = map[string]struct{}{}
	}
	if want == nil {
		want = map[string]struct{}{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model set = %v, want %v", setKeys(got), setKeys(want))
	}
}

func setKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
