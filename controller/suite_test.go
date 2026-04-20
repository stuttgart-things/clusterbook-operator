package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
	cfg       *rest.Config
	scheme    *runtime.Scheme
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme = runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := argov1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "kcl", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		_ = testEnv.Stop()
		panic(err)
	}

	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

// fakeClusterbook is an in-process stand-in for the clusterbook REST API.
type fakeClusterbook struct {
	server *httptest.Server

	mu           sync.Mutex
	reserved     map[string]string  // cluster name -> IP
	state        map[string]ipState // IP -> current state
	released     []string
	updates      []cbkclient.ReserveRequest // every UpdateIP body, in order
	fqdn         string                     // returned by /clusters/{name}; empty means "no DNS"
	nextIP       int                        // next host octet to hand out (starts at 42)
	reserveCalls int                        // count of /reserve requests received
}

type ipState struct {
	cluster   string
	createDNS bool
}

func newFakeClusterbook() *fakeClusterbook {
	f := &fakeClusterbook{
		reserved: map[string]string{},
		state:    map[string]ipState{},
		nextIP:   42,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/networks/", f.handleNetworks)
	mux.HandleFunc("/api/v1/clusters/", f.handleClusters)
	f.server = httptest.NewServer(mux)
	return f
}

func (f *fakeClusterbook) handleNetworks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	action := parts[4]
	switch action {
	case "reserve":
		var req cbkclient.ReserveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.reserveCalls++
		// Adversarial behaviour: every /reserve hands out a fresh IP
		// even when this cluster already has one. Mirrors real-world
		// clusterbook behaviour observed during smoke testing. The
		// operator must defend itself by calling GetIPs before Reserve.
		ip := fmt.Sprintf("10.0.0.%d", f.nextIP)
		f.nextIP++
		f.reserved[req.Cluster] = ip
		f.state[ip] = ipState{cluster: req.Cluster, createDNS: req.CreateDNS}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(cbkclient.ReserveResponse{IPs: []string{ip}, Status: "ASSIGNED"})
	case "release":
		var req cbkclient.ReleaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.released = append(f.released, req.IP)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "ips":
		switch {
		case len(parts) == 5 && r.Method == http.MethodGet:
			f.handleListIPs(w, r)
		case len(parts) == 6 && r.Method == http.MethodPut:
			f.handleUpdateIP(w, r, parts[5])
		default:
			http.Error(w, "bad ips path", http.StatusBadRequest)
		}
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

func (f *fakeClusterbook) handleListIPs(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]cbkclient.IPInfo, 0, len(f.state))
	for ip, st := range f.state {
		status := "ASSIGNED"
		if st.createDNS {
			status = "ASSIGNED:DNS"
		}
		out = append(out, cbkclient.IPInfo{
			IP:      ip,
			Cluster: st.cluster,
			Status:  status,
		})
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeClusterbook) handleUpdateIP(w http.ResponseWriter, r *http.Request, ip string) {
	var req cbkclient.ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.updates = append(f.updates, req)
	st := f.state[ip]
	st.createDNS = req.CreateDNS
	f.state[ip] = st
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeClusterbook) handleClusters(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	name := parts[len(parts)-1]
	_ = json.NewEncoder(w).Encode(cbkclient.ClusterInfo{
		Cluster: name,
		FQDN:    f.fqdn,
		Zone:    "example.com",
	})
}

func (f *fakeClusterbook) releasedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.released)
}

func (f *fakeClusterbook) reserveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reserveCalls
}

func (f *fakeClusterbook) reservedIPs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.reserved))
	for _, ip := range f.reserved {
		out = append(out, ip)
	}
	return out
}

func (f *fakeClusterbook) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

func (f *fakeClusterbook) lastUpdate() cbkclient.ReserveRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) == 0 {
		return cbkclient.ReserveRequest{}
	}
	return f.updates[len(f.updates)-1]
}
