package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
)

// SendFileOpts parameterises SendFile across the three axes the `spine
// artifact`/`spine workflow` CLI commands vary along: HTTP method, request
// endpoint, the JSON field carrying the file contents (`content` for
// artifacts, `body` for workflows), and any additional fields sent
// alongside (e.g. `path` on artifact-create, `id` on workflow-create).
type SendFileOpts struct {
	Method    string            // http.MethodPost or http.MethodPut
	Endpoint  string            // e.g. "/api/v1/artifacts"
	BodyField string            // "content" or "body"
	Extra     map[string]string // fields merged into the request JSON alongside the file contents
}

// SendFile reads a file from disk and POSTs/PUTs it as JSON through the
// given Client. On success the raw response bytes are returned.
//
// Centralising this matches the file-read-then-request pattern shared by
// artifact.Create/Update and workflow.Create/Update/Validate so each new
// CLI command doesn't copy the same six lines.
func SendFile(ctx context.Context, client *Client, file string, opts SendFileOpts) ([]byte, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	payload := map[string]string{opts.BodyField: string(content)}
	for k, v := range opts.Extra {
		payload[k] = v
	}
	switch opts.Method {
	case http.MethodPost:
		return client.Post(ctx, opts.Endpoint, payload)
	case http.MethodPut:
		return client.Put(ctx, opts.Endpoint, payload)
	default:
		return nil, fmt.Errorf("unsupported method %q", opts.Method)
	}
}
