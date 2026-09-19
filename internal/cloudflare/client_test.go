package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPointPreservesGreyAndWaitsOldTTL(t *testing.T) {
	mutations := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/zones":
			if r.URL.Query().Get("name") == "example.com" {
				fmt.Fprint(w, `{"success":true,"result":[{"id":"z"}]}`)
			} else {
				fmt.Fprint(w, `{"success":true,"result":[]}`)
			}
		case r.Method == "GET":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"r","type":"A","content":"8.8.8.8","proxied":false,"ttl":3600}]}`)
		case r.Method == "PATCH":
			mutations++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["content"] != "8.8.4.4" || body["ttl"] != float64(60) {
				t.Errorf("wrong body: %v", body)
			}
			if _, ok := body["proxied"]; ok {
				t.Error("should preserve proxy flag")
			}
			fmt.Fprint(w, `{"success":true,"result":{"id":"r","type":"A","content":"8.8.4.4","proxied":false,"ttl":60}}`)
		default:
			t.Errorf("unexpected request %s", r.Method)
		}
	}))
	defer s.Close()
	c := New()
	c.base = s.URL
	c.http = s.Client()
	ttl, err := c.Point(t.Context(), "test-token", "proxy.example.com", "8.8.4.4")
	if err != nil || ttl != 3600 || mutations != 1 {
		t.Fatalf("ttl=%d mutations=%d err=%v", ttl, mutations, err)
	}
}

func TestAmbiguousRecordsAndAPIFailuresNeverMutate(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"result":[{"type":"AAAA"}]}`,
		`{"success":true,"result":[{"type":"CNAME"}]}`,
		`{"success":true,"result":[{"type":"A"},{"type":"A"}]}`,
		`{"success":true,"result":[{"type":"A","proxied":true}]}`,
		`{"success":false,"errors":[{"code":10000,"message":"test-token"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected mutation")
				}
				if r.URL.Path == "/zones" {
					fmt.Fprint(w, `{"success":true,"result":[{"id":"z"}]}`)
				} else {
					fmt.Fprint(w, body)
				}
			}))
			defer s.Close()
			c := New()
			c.base = s.URL
			c.http = s.Client()
			_, err := c.Point(t.Context(), "test-token", "proxy.example.com", "8.8.4.4")
			if err == nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), "test-token") {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestPointCreatesMissingARecord(t *testing.T) {
	created := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zones" {
			fmt.Fprint(w, `{"success":true,"result":[{"id":"z"}]}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"success":true,"result":[]}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || body["type"] != "A" || body["name"] != "proxy.example.com" || body["proxied"] != false {
			t.Errorf("unexpected create: %v", body)
		}
		created = true
		fmt.Fprint(w, `{"success":true,"result":{"type":"A","content":"8.8.4.4","proxied":false,"ttl":60}}`)
	}))
	defer s.Close()
	c := New()
	c.base = s.URL
	c.http = s.Client()
	if _, err := c.Point(t.Context(), "test-token", "proxy.example.com", "8.8.4.4"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing record not created")
	}
}
