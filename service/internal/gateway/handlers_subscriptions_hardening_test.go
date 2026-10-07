package gateway_test

// INIT-023 TASK-010: the subscription handlers' share of the webhook
// delivery hardening — no resolved address and no secret URL part in
// the subscription-test response or its log line (P3-25, P3-26).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/service/internal/gateway"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureGatewayLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func postSubscriptionTest(t *testing.T, base, token, id string) (int, map[string]any) {
	t.Helper()
	resp := doJSON(t, http.MethodPost, base+"/api/v1/subscriptions/"+id+"/test", token, nil)
	defer resp.Body.Close()
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// AC17: the test endpoint's JSON error and its log line omit secret path
// and query parts of the target URL.
func TestSubscriptionTest_RedactsSecretURLParts(t *testing.T) {
	logs := captureGatewayLogs(t)
	port := closedLoopbackPort(t)
	ts, fs, token := setupSubServer(t) // nil validator: the dial is attempted
	target := fmt.Sprintf("http://127.0.0.1:%d/services/T000/B000/s3cr3tPathToken?token=s3cr3tQueryToken", port)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: target}

	code, got := postSubscriptionTest(t, ts.URL, token, "sub-1")
	if code != http.StatusOK || got["success"] != false {
		t.Fatalf("status %d body %v, want 200 success=false", code, got)
	}
	msg, _ := got["error"].(string)
	if !strings.Contains(msg, fmt.Sprintf("127.0.0.1:%d/[redacted]", port)) {
		t.Fatalf("error %q lost the endpoint's host:port", msg)
	}
	for _, h := range []string{msg, logs.String()} {
		for _, secret := range []string{"s3cr3tPathToken", "s3cr3tQueryToken"} {
			if strings.Contains(h, secret) {
				t.Fatalf("secret %q leaked into %q", secret, h)
			}
		}
	}
	if !strings.Contains(logs.String(), "subscription test failed") {
		t.Fatalf("the test-failure log line is missing")
	}

	// A target that does not even parse: the parse error is redacted too.
	fs.subs["sub-2"] = &store.EventSubscription{SubscriptionID: "sub-2", TargetURL: "http://exa mple.com/s3cr3tPathToken"}
	_, got = postSubscriptionTest(t, ts.URL, token, "sub-2")
	if msg, _ := got["error"].(string); msg == "" || strings.Contains(msg, "s3cr3tPathToken") {
		t.Fatalf("parse error %q echoes the URL", msg)
	}
}

// AC16 (design check): a connect failure to an allowlisted DNS-named
// host names the caller's host:port, never the resolved address.
func TestSubscriptionTest_DialErrorOmitsResolvedAddress(t *testing.T) {
	logs := captureGatewayLogs(t)
	port := closedLoopbackPort(t)
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator([]string{"localhost"})
	})
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: fmt.Sprintf("http://localhost:%d/hook", port)}

	_, got := postSubscriptionTest(t, ts.URL, token, "sub-1")
	msg, _ := got["error"].(string)
	if got["success"] != false || !strings.Contains(msg, fmt.Sprintf("localhost:%d", port)) {
		t.Fatalf("body %v, want a failure naming localhost:%d", got, port)
	}
	for _, leak := range []string{"127.0.0.1", "[::1]"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error %q names the resolved address %s", msg, leak)
		}
	}
	// The operator log carries the same redacted line (the address
	// detail is logged by the dialer at Debug, for the operator only).
	if !strings.Contains(logs.String(), "subscription test failed") {
		t.Fatal("test-failure log line missing")
	}
}

// AC16: testing a target whose hostname resolves to loopback is refused
// with no address and no address class in the response.
func TestSubscriptionTest_LoopbackHostRefusedWithoutAddress(t *testing.T) {
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator(nil)
	})
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: "https://localhost/hook"}

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions/sub-1/test", token, nil)
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", resp.StatusCode, body.String())
	}
	for _, leak := range []string{"127.0.0.1", "::1", "loopback"} {
		if strings.Contains(body.String(), leak) {
			t.Fatalf("response %s reveals %q", body.String(), leak)
		}
	}
	if !strings.Contains(body.String(), "non-public address") {
		t.Fatalf("response %s lacks the generic refusal", body.String())
	}
}
