package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SaveURLInput struct {
	URL         string `json:"url" jsonschema:"the URL of the page to save"`
	Title       string `json:"title,omitempty" jsonschema:"optional title; Instapaper fetches one if omitted"`
	Description string `json:"description,omitempty" jsonschema:"optional short description or note"`
	FolderID    string `json:"folder_id,omitempty" jsonschema:"optional Instapaper folder id; defaults to Home (unread)"`
}

type SaveURLOutput struct {
	BookmarkID int64  `json:"bookmark_id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on LISTEN_ADDR and exit 0 if healthy")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	addr := envOr("LISTEN_ADDR", ":8080")

	if *healthcheck {
		os.Exit(probe(addr))
	}

	client, err := newClientFromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "instapaper-mcp", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_url",
		Description: "Save a URL to the user's Instapaper account for reading later.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveURLInput) (*mcp.CallToolResult, SaveURLOutput, error) {
		if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
			return nil, SaveURLOutput{}, fmt.Errorf("url must start with http:// or https://")
		}
		if in.FolderID != "" {
			if _, err := strconv.ParseInt(in.FolderID, 10, 64); err != nil {
				return nil, SaveURLOutput{}, fmt.Errorf("folder_id must be numeric")
			}
		}
		b, err := client.AddBookmark(ctx, in.URL, in.Title, in.Description, in.FolderID)
		if err != nil {
			logger.Warn("save_url failed", "url", in.URL, "err", err)
			return nil, SaveURLOutput{}, err
		}
		logger.Info("saved", "url", b.URL, "bookmark_id", b.ID)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Saved: %s (bookmark_id %d)", b.Title, b.ID)}},
		}, SaveURLOutput{BookmarkID: b.ID, Title: b.Title, URL: b.URL}, nil
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: logger}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

func newClientFromEnv() (*Client, error) {
	c := &Client{
		BaseURL:        envOr("INSTAPAPER_URL", "https://www.instapaper.com"),
		ConsumerKey:    os.Getenv("INSTAPAPER_CONSUMER"),
		ConsumerSecret: os.Getenv("INSTAPAPER_SECRET"),
		Username:       os.Getenv("INSTAPAPER_USERNAME"),
		Password:       os.Getenv("INSTAPAPER_PASSWORD"),
		HTTP:           &http.Client{Timeout: 15 * time.Second},
	}
	// The API lives on www; a redirect from the bare domain would drop the signed POST body.
	c.BaseURL = strings.Replace(strings.TrimRight(c.BaseURL, "/"), "://instapaper.com", "://www.instapaper.com", 1)

	var missing []string
	for name, v := range map[string]string{
		"INSTAPAPER_CONSUMER": c.ConsumerKey,
		"INSTAPAPER_SECRET":   c.ConsumerSecret,
		"INSTAPAPER_USERNAME": c.Username,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing env vars: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func probe(addr string) int {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + host + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
