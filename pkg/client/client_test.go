/*
Copyright 2025 The stuttgart-things Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0
*/

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	t.Run("plain HTTP", func(t *testing.T) {
		c, err := NewClient("http://localhost:8080", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.baseURL != "http://localhost:8080" {
			t.Errorf("baseURL = %q, want %q", c.baseURL, "http://localhost:8080")
		}
	})

	t.Run("trims trailing slash", func(t *testing.T) {
		c, err := NewClient("http://localhost:8080/", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.baseURL != "http://localhost:8080" {
			t.Errorf("baseURL = %q, want %q", c.baseURL, "http://localhost:8080")
		}
	})

	t.Run("insecure skip verify", func(t *testing.T) {
		c, err := NewClient("https://localhost:8443", &TLSOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("client is nil")
		}
	})

	t.Run("invalid custom CA", func(t *testing.T) {
		_, err := NewClient("https://localhost:8443", &TLSOptions{CustomCA: "not-a-cert"})
		if err == nil {
			t.Fatal("expected error for invalid CA")
		}
	})
}

func TestReserveIPs(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if r.URL.Path != "/api/v1/networks/10.31.103/reserve" {
				t.Errorf("path = %s", r.URL.Path)
			}
			var req ReserveRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("cannot decode: %v", err)
			}
			if req.Cluster != "mycluster" || req.Count != 1 {
				t.Errorf("request = %+v", req)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(ReserveResponse{IPs: []string{"10.31.103.10"}, Status: "ASSIGNED"}) //nolint:errcheck
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL, nil)
		resp, err := c.ReserveIPs(context.Background(), "10.31.103", ReserveRequest{Cluster: "mycluster", Count: 1})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.IPs) != 1 || resp.IPs[0] != "10.31.103.10" {
			t.Errorf("IPs = %v", resp.IPs)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL, nil)
		_, err := c.ReserveIPs(context.Background(), "10.31.103", ReserveRequest{Cluster: "mycluster", Count: 1})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestGetClusterInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clusters/mycluster" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(ClusterInfo{ //nolint:errcheck
			Cluster: "mycluster",
			FQDN:    "*.mycluster.example.com",
			Zone:    "example.com",
		})
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, nil)
	info, err := c.GetClusterInfo(context.Background(), "mycluster")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.FQDN != "*.mycluster.example.com" {
		t.Errorf("FQDN = %q", info.FQDN)
	}
}

func TestReleaseIPs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, nil)
	if err := c.ReleaseIPs(context.Background(), "10.31.103", ReleaseRequest{IP: "10.31.103.10"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
