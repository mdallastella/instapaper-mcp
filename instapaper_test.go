package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Example from the Twitter OAuth 1.0a signing docs.
func TestOAuthSignature(t *testing.T) {
	params := url.Values{
		"status":           {"Hello Ladies + Gentlemen, a signed OAuth request!"},
		"include_entities": {"true"},
	}
	h := oauthHeader("POST", "https://api.twitter.com/1.1/statuses/update.json", params,
		"xvz1evFS4wEEPTGEFPHBog", "kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw",
		"370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb", "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE",
		"kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg", "1318622958")
	want := `oauth_signature="hCtSmYh%2BiHYCEqBWrE7C7hYmtUk%3D"`
	if !strings.Contains(h, want) {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", h, want)
	}
}

func TestAddBookmark(t *testing.T) {
	var xauthCalls, addCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "OAuth ") {
			t.Errorf("missing OAuth header on %s", r.URL.Path)
		}
		r.ParseForm()
		switch r.URL.Path {
		case "/api/1/oauth/access_token":
			xauthCalls++
			if r.Form.Get("x_auth_username") != "me@example.com" || r.Form.Get("x_auth_mode") != "client_auth" {
				t.Errorf("bad xauth form: %v", r.Form)
			}
			w.Write([]byte("oauth_token_secret=ts&oauth_token=tok"))
		case "/api/1/bookmarks/add":
			addCalls++
			if !strings.Contains(r.Header.Get("Authorization"), `oauth_token="tok"`) {
				t.Errorf("user token not sent")
			}
			if r.Form.Get("url") == "https://bad.example" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`[{"type":"error","error_code":1240,"message":"Invalid URL specified"}]`))
				return
			}
			w.Write([]byte(`[{"type":"bookmark","bookmark_id":42,"title":"Example","url":"` + r.Form.Get("url") + `"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, ConsumerKey: "ck", ConsumerSecret: "cs", Username: "me@example.com", HTTP: srv.Client()}

	b, err := c.AddBookmark(context.Background(), "https://example.com", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != 42 || b.Title != "Example" {
		t.Fatalf("unexpected bookmark: %+v", b)
	}
	if _, err := c.AddBookmark(context.Background(), "https://example.org", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if xauthCalls != 1 || addCalls != 2 {
		t.Fatalf("xauth=%d add=%d, want 1 and 2", xauthCalls, addCalls)
	}

	_, err = c.AddBookmark(context.Background(), "https://bad.example", "", "", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 1240 {
		t.Fatalf("want APIError 1240, got %v", err)
	}
}

func TestBearerAuth(t *testing.T) {
	h := bearerAuth("secret", slog.New(slog.DiscardHandler), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"", http.StatusUnauthorized},
		{"secret", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"Bearer secret", http.StatusTeapot},
	} {
		req := httptest.NewRequest("POST", "/mcp", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Authorization %q: got %d, want %d", tc.header, rec.Code, tc.want)
		}
	}
}
