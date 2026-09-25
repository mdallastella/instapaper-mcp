# instapaper-mcp

Minimal MCP server (Streamable HTTP) with one tool, `save_url`, that saves a URL to Instapaper via the Full API (OAuth 1.0a + xAuth).

## Configure

```sh
cp .env.example .env   # fill in consumer key/secret, Instapaper login, HERMES_NETWORK
```

The xAuth exchange runs on the first tool call; the resulting token is kept in memory.

## Run

```sh
docker compose up -d --build
```

## Hermes

`~/.hermes/config.yaml`:

```yaml
mcp_servers:
  instapaper:
    url: "http://instapaper-mcp:8080/mcp"
    timeout: 60
```

## Develop

```sh
go test ./...
go run .   # needs the INSTAPAPER_* vars in the environment
```
