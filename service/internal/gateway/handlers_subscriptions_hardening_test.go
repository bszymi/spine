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
	"time"

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

// gatewayResetServer reads the request, then resets the connection
// (SO_LINGER 0): the client's read fails after a successful connect.
func gatewayResetServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4096)
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Read(buf)
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
				c.Close()
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// Review round 1 (I5): a target that accepts the connection and then
// resets it used to put "read tcp 127.0.0.1:<p>->127.0.0.1:<port>: ..."
// — the resolved address and Spine's own source address — into the
// subscription-test response and its log line. Both now name the
// caller's host:port only.
func TestSubscriptionTest_PostConnectErrorOmitsAddresses(t *testing.T) {
	logs := captureGatewayLogs(t)
	port := gatewayResetServer(t)
	ts, fs, token := setupSubServerWithCfg(t, func(cfg *gateway.ServerConfig) {
		cfg.WebhookTargets = delivery.NewTargetValidator([]string{"localhost"})
	})
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", TargetURL: fmt.Sprintf("http://localhost:%d/services/T/B/s3cr3tPathToken", port)}

	_, got := postSubscriptionTest(t, ts.URL, token, "sub-1")
	msg, _ := got["error"].(string)
	if got["success"] != false || !strings.Contains(msg, fmt.Sprintf("tcp localhost:%d", port)) {
		t.Fatalf("body %v, want a failure naming localhost:%d", got, port)
	}
	var failLine string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "subscription test failed") {
			failLine = line
		}
	}
	if failLine == "" {
		t.Fatal("test-failure log line missing")
	}
	for _, h := range []string{msg, failLine} {
		for _, leak := range []string{"127.0.0.1", "[::1]", "s3cr3tPathToken"} {
			if strings.Contains(h, leak) {
				t.Fatalf("%q names %s", h, leak)
			}
		}
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

func errorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Errors) == 0 {
		return ""
	}
	return body.Errors[0].Code
}

// AC4: metadata is accepted as a JSON object (the documented form) or as
// a base64 string (the legacy wire form) and stored identically; an
// unknown or non-string signature_version is a 400 invalid_params.
func TestSubscriptionCreate_MetadataForms(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	const md = `{"signature_version":"v2","tls":{"pinned_spki_sha256":"ab"}}`
	create := func(name string, metadata any) (*http.Response, *store.EventSubscription) {
		t.Helper()
		resp := doJSON(t, http.MethodPost, ts.URL+"/api/v1/subscriptions", token, map[string]any{
			"name": name, "target_url": "https://example.test/hook", "metadata": metadata,
		})
		for _, s := range fs.subs {
			if s.Name == name {
				return resp, s
			}
		}
		return resp, nil
	}

	respObj, objSub := create("object-form", json.RawMessage(md))
	respObj.Body.Close()
	respB64, b64Sub := create("base64-form", []byte(md)) // json.Marshal encodes []byte as base64
	respB64.Body.Close()
	if respObj.StatusCode != http.StatusCreated || respB64.StatusCode != http.StatusCreated {
		t.Fatalf("object form %d, base64 form %d, want 201/201", respObj.StatusCode, respB64.StatusCode)
	}
	if objSub == nil || b64Sub == nil || string(objSub.Metadata) != md || string(b64Sub.Metadata) != md {
		t.Fatalf("stored metadata differs: object=%s base64=%s", objSub.Metadata, b64Sub.Metadata)
	}

	// Whitespace in the object form is normalised to the same bytes.
	respWS, wsSub := create("object-form-spaced", json.RawMessage(`{ "signature_version" : "v2", "tls": {"pinned_spki_sha256":"ab"} }`))
	respWS.Body.Close()
	if respWS.StatusCode != http.StatusCreated || string(wsSub.Metadata) != md {
		t.Fatalf("spaced object form: %d %s", respWS.StatusCode, wsSub.Metadata)
	}

	// No metadata: {} as before.
	respNone, noneSub := create("no-metadata", nil)
	respNone.Body.Close()
	if respNone.StatusCode != http.StatusCreated || string(noneSub.Metadata) != "{}" {
		t.Fatalf("absent metadata: %d %s", respNone.StatusCode, noneSub.Metadata)
	}

	for i, bad := range []any{
		json.RawMessage(`{"signature_version":"v3"}`),
		json.RawMessage(`{"signature_version":2}`),
		json.RawMessage(`{"Signature_Version":"v2"}`),
		[]byte(`{"signature_version":"v3"}`),
		json.RawMessage(`{"tls":{"pinned_spki_sha256":"not-hex"}}`),
		json.RawMessage(`[1,2]`),
		json.RawMessage(`"not base64!"`),
		[]byte(`not json`),
		json.RawMessage(`7`),
	} {
		name := fmt.Sprintf("bad-%d", i)
		resp, sub := create(name, bad)
		code := errorCode(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || code != "invalid_params" || sub != nil {
			t.Errorf("metadata %s: status %d code %q stored=%v, want 400 invalid_params and nothing stored", bad, resp.StatusCode, code, sub != nil)
		}
	}
}

// AC4: PATCH validates metadata the same way; a rejected update leaves
// the row unchanged; GET reports signature_version.
func TestSubscriptionUpdate_MetadataValidationAndSignatureVersion(t *testing.T) {
	ts, fs, token := setupSubServer(t)
	fs.subs["sub-1"] = &store.EventSubscription{SubscriptionID: "sub-1", Name: "a", TargetURL: "https://example.test/hook",
		Status: "active", Metadata: []byte(`{"source":"api"}`)}

	resp := doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token, map[string]any{
		"metadata": json.RawMessage(`{"signature_version":"v9"}`),
	})
	code := errorCode(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || code != "invalid_params" {
		t.Fatalf("invalid PATCH: %d %q, want 400 invalid_params", resp.StatusCode, code)
	}
	if string(fs.subs["sub-1"].Metadata) != `{"source":"api"}` {
		t.Fatalf("rejected PATCH changed metadata: %s", fs.subs["sub-1"].Metadata)
	}

	resp = doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token, map[string]any{
		"metadata": json.RawMessage(`{"signature_version":"v2"}`),
	})
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got["signature_version"] != "v2" {
		t.Fatalf("valid PATCH: %d %v, want 200 with signature_version v2", resp.StatusCode, got)
	}
	if string(fs.subs["sub-1"].Metadata) != `{"signature_version":"v2"}` {
		t.Fatalf("PATCH metadata stored as %s (update replaces metadata wholesale)", fs.subs["sub-1"].Metadata)
	}

	// A PATCH without metadata keeps it.
	resp = doJSON(t, http.MethodPatch, ts.URL+"/api/v1/subscriptions/sub-1", token, map[string]any{"name": "renamed"})
	resp.Body.Close()
	if string(fs.subs["sub-1"].Metadata) != `{"signature_version":"v2"}` {
		t.Fatalf("PATCH without metadata changed it: %s", fs.subs["sub-1"].Metadata)
	}

	for id, tc := range map[string]struct {
		metadata string
		want     string
	}{
		"sub-none":    {"", "v1"},
		"sub-v1":      {`{"source":"bootstrap"}`, "v1"},
		"sub-v2":      {`{"signature_version":"v2"}`, "v2"},
		"sub-invalid": {`{"signature_version":"v3"}`, "invalid"},
		// Review round 1: metadata the dispatcher refuses for a reason
		// other than the version is "invalid" too, not "v1"/"v2".
		"sub-base64-pin":   {`{"tls":{"pinned_spki_sha256":"n4bQgYhMfWWaL+qgxVrQFaO/TxsrC4Is0V1sFbDwCgg="}}`, "invalid"},
		"sub-v2-bad-pin":   {`{"signature_version":"v2","tls":{"pinned_spki_sha256":"zz"}}`, "invalid"},
		"sub-empty-ca":     {`{"tls":{"ca_bundle_pem":"not a certificate"}}`, "invalid"},
		"sub-ambiguous":    {`{"TLS":{"pinned_spki_sha256":"ab"},"tls":null}`, "invalid"},
		"sub-v2-good-pin":  {`{"signature_version":"v2","tls":{"pinned_spki_sha256":"ab"}}`, "v2"},
		"sub-v1-upper-tls": {`{"TLS":{"pinned_spki_sha256":"ab"}}`, "v1"},
	} {
		fs.subs[id] = &store.EventSubscription{SubscriptionID: id, Metadata: []byte(tc.metadata)}
		resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/subscriptions/"+id, token, nil)
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body["signature_version"] != tc.want {
			t.Errorf("%s: signature_version = %v, want %s", id, body["signature_version"], tc.want)
		}
	}
}
