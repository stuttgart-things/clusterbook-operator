package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

const (
	// labelClusterReady is stamped (latching) onto the rendered ArgoCD
	// cluster Secret once the downstream API server has answered /readyz.
	// ApplicationSets gate on it next to allocation-ip — see issue #119.
	labelClusterReady = clusterbookPrefix + "cluster-ready"

	// conditionClusterReachable reports the outcome of the readiness probe.
	conditionClusterReachable = "ClusterReachable"

	// probeTimeout bounds a single /readyz call. A cluster still
	// bootstrapping typically fails fast (no route to host, connection
	// refused); the timeout only caps the silent-drop case.
	probeTimeout = 5 * time.Second

	// probeRequeue is how often a not-yet-ready cluster is probed again.
	probeRequeue = 30 * time.Second
)

// ClusterProbe checks whether the API server described by cfg answers.
// A nil error means ready.
type ClusterProbe func(ctx context.Context, cfg *rest.Config) error

// defaultClusterProbe is the probe used when Reconciler.ProbeCluster is nil.
// A package variable so the envtest suite can swap it for one that does not
// dial the fake kubeconfig's unresolvable hosts on every test.
var defaultClusterProbe ClusterProbe = probeReadyz

// probeReadyz issues an authenticated GET /readyz and treats HTTP 200 as
// ready. Anything else — transport error, 401/403, 5xx — is not ready.
func probeReadyz(ctx context.Context, cfg *rest.Config) error {
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = probeTimeout
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return fmt.Errorf("build HTTP client: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cfg.Host, "/")+"/readyz", nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("GET /readyz: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// restConfigFromArgo builds the client config ArgoCD itself will use for
// this cluster: the rendered data.server plus the rendered data.config.
// Probing with anything else (e.g. the raw kubeconfig's server) could pass
// while Argo still cannot connect — or the other way around.
func restConfigFromArgo(server string, cfgJSON []byte) (*rest.Config, error) {
	var ac argoClusterConfig
	if err := json.Unmarshal(cfgJSON, &ac); err != nil {
		return nil, fmt.Errorf("decode argo cluster config: %w", err)
	}
	return &rest.Config{
		Host:        server,
		BearerToken: ac.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   ac.TLSClientConfig.Insecure,
			ServerName: ac.TLSClientConfig.ServerName,
			CAData:     ac.TLSClientConfig.CAData,
			CertData:   ac.TLSClientConfig.CertData,
			KeyData:    ac.TLSClientConfig.KeyData,
		},
	}, nil
}
