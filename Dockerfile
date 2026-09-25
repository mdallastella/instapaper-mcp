FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /instapaper-mcp .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /instapaper-mcp /instapaper-mcp
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/instapaper-mcp"]
