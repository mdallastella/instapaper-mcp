package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to the Instapaper Full API (OAuth 1.0a with xAuth).
type Client struct {
	BaseURL        string
	ConsumerKey    string
	ConsumerSecret string
	Username       string
	Password       string
	HTTP           *http.Client

	mu          sync.Mutex
	token       string
	tokenSecret string
}

// Bookmark is the subset of Instapaper's bookmark object we care about.
type Bookmark struct {
	ID    int64  `json:"bookmark_id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// APIError is an Instapaper error object ({"type":"error","error_code":...}).
type APIError struct {
	Code    int    `json:"error_code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("instapaper error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("instapaper HTTP %d: %s", e.Status, e.Message)
}

// AddBookmark saves url to Instapaper. title, description and folderID are optional.
func (c *Client) AddBookmark(ctx context.Context, rawURL, title, description, folderID string) (*Bookmark, error) {
	params := url.Values{"url": {rawURL}}
	if title != "" {
		params.Set("title", title)
	}
	if description != "" {
		params.Set("description", description)
	}
	if folderID != "" {
		params.Set("folder_id", folderID)
	}

	for attempt := 0; ; attempt++ {
		token, secret, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		body, err := c.post(ctx, "/api/1/bookmarks/add", params, token, secret)
		var apiErr *APIError
		if err != nil && errors.As(err, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) && attempt == 0 {
			c.resetToken()
			continue
		}
		if err != nil {
			return nil, err
		}
		return parseBookmark(body)
	}
}

func parseBookmark(body []byte) (*Bookmark, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	for _, raw := range items {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Type {
		case "bookmark":
			var b Bookmark
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("decode bookmark: %w", err)
			}
			return &b, nil
		case "error":
			var e APIError
			_ = json.Unmarshal(raw, &e)
			return nil, &e
		}
	}
	return nil, fmt.Errorf("no bookmark in response: %s", body)
}

// accessToken returns the cached user token, performing the xAuth exchange on first use.
func (c *Client) accessToken(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, c.tokenSecret, nil
	}
	body, err := c.post(ctx, "/api/1/oauth/access_token", url.Values{
		"x_auth_username": {c.Username},
		"x_auth_password": {c.Password},
		"x_auth_mode":     {"client_auth"},
	}, "", "")
	if err != nil {
		return "", "", fmt.Errorf("xauth: %w", err)
	}
	v, err := url.ParseQuery(string(body))
	if err != nil || v.Get("oauth_token") == "" {
		return "", "", fmt.Errorf("xauth: unexpected response: %s", body)
	}
	c.token, c.tokenSecret = v.Get("oauth_token"), v.Get("oauth_token_secret")
	return c.token, c.tokenSecret, nil
}

func (c *Client) resetToken() {
	c.mu.Lock()
	c.token, c.tokenSecret = "", ""
	c.mu.Unlock()
}

// post sends a signed form POST and returns the body, or an *APIError on HTTP >= 400.
func (c *Client) post(ctx context.Context, path string, params url.Values, token, tokenSecret string) ([]byte, error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", oauthHeader(http.MethodPost, endpoint, params,
		c.ConsumerKey, c.ConsumerSecret, token, tokenSecret, nonce(), strconv.FormatInt(time.Now().Unix(), 10)))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		apiErr := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(body))}
		var items []APIError
		if json.Unmarshal(body, &items) == nil && len(items) > 0 {
			apiErr.Code, apiErr.Message = items[0].Code, items[0].Message
		}
		return nil, apiErr
	}
	return body, nil
}

// oauthHeader builds an OAuth 1.0a HMAC-SHA1 Authorization header.
// token may be empty (xAuth exchange is signed with consumer credentials only).
func oauthHeader(method, endpoint string, params url.Values, consumerKey, consumerSecret, token, tokenSecret, nonce, timestamp string) string {
	oauth := map[string]string{
		"oauth_consumer_key":     consumerKey,
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        timestamp,
		"oauth_version":          "1.0",
	}
	if token != "" {
		oauth["oauth_token"] = token
	}

	var pairs []string
	for k, vs := range params {
		for _, v := range vs {
			pairs = append(pairs, percentEncode(k)+"="+percentEncode(v))
		}
	}
	for k, v := range oauth {
		pairs = append(pairs, percentEncode(k)+"="+percentEncode(v))
	}
	sort.Strings(pairs)

	base := method + "&" + percentEncode(endpoint) + "&" + percentEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(percentEncode(consumerSecret)+"&"+percentEncode(tokenSecret)))
	mac.Write([]byte(base))
	oauth["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	keys := make([]string, 0, len(oauth))
	for k := range oauth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = percentEncode(k) + `="` + percentEncode(oauth[k]) + `"`
	}
	return "OAuth " + strings.Join(parts, ", ")
}

// percentEncode encodes s per RFC 3986 (unreserved: ALPHA DIGIT - . _ ~).
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ('A' <= ch && ch <= 'Z') || ('a' <= ch && ch <= 'z') || ('0' <= ch && ch <= '9') ||
			ch == '-' || ch == '.' || ch == '_' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
