/*
Copyright 2025 The stuttgart-things Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package client is an HTTP client for the clusterbook REST API.
// Copied from github.com/stuttgart-things/provider-clusterbook/internal/client.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type TLSOptions struct {
	InsecureSkipVerify bool
	CustomCA           string
}

func NewClient(baseURL string, opts *TLSOptions) (*Client, error) {
	transport := &http.Transport{}

	if opts != nil && (opts.InsecureSkipVerify || opts.CustomCA != "") {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec

		if opts.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec
		}

		if opts.CustomCA != "" {
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM([]byte(opts.CustomCA)) {
				return nil, fmt.Errorf("cannot parse custom CA certificate")
			}
			tlsConfig.RootCAs = pool
		}

		transport.TLSClientConfig = tlsConfig
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

type IPInfo struct {
	IP      string `json:"IP"`
	Digit   string `json:"Digit"`
	Status  string `json:"Status"`
	Cluster string `json:"Cluster"`
	FQDN    string `json:"FQDN,omitempty"`
}

type ClusterInfo struct {
	Cluster string `json:"cluster"`
	FQDN    string `json:"fqdn,omitempty"`
	Zone    string `json:"zone,omitempty"`
}

type ReserveRequest struct {
	Cluster   string `json:"cluster"`
	Count     int    `json:"count,omitempty"`
	IP        string `json:"ip,omitempty"`
	CreateDNS bool   `json:"createDNS,omitempty"`
	Status    string `json:"status,omitempty"`
}

type ReserveResponse struct {
	IPs    []string `json:"ips"`
	Status string   `json:"status"`
}

type ReleaseRequest struct {
	IP string `json:"ip"`
}

func (c *Client) ReserveIPs(ctx context.Context, networkKey string, req ReserveRequest) (*ReserveResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal reserve request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/reserve", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot create reserve request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("reserve request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reserve request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ReserveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cannot decode reserve response: %w", err)
	}
	return &result, nil
}

func (c *Client) GetIPs(ctx context.Context, networkKey string) ([]IPInfo, error) {
	url := fmt.Sprintf("%s/api/v1/networks/%s/ips", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create get IPs request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get IPs request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get IPs returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var ips []IPInfo
	if err := json.NewDecoder(resp.Body).Decode(&ips); err != nil {
		return nil, fmt.Errorf("cannot decode IPs response: %w", err)
	}
	return ips, nil
}

func (c *Client) GetClusterInfo(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	url := fmt.Sprintf("%s/api/v1/clusters/%s", c.baseURL, clusterName)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create get cluster info request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get cluster info request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get cluster info returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var info ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("cannot decode cluster info response: %w", err)
	}
	return &info, nil
}

func (c *Client) ReleaseIPs(ctx context.Context, networkKey string, req ReleaseRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cannot marshal release request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/release", c.baseURL, networkKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot create release request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("release request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("release request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *Client) UpdateIP(ctx context.Context, networkKey, ip string, req ReserveRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cannot marshal update request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/networks/%s/ips/%s", c.baseURL, networkKey, ip)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot create update request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("update request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
