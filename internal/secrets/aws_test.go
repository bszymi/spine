package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/bszymi/spine/internal/secrets"
)

// fakeAPI is a controllable SecretsManagerAPI used to drive provider
// tests without LocalStack. It records calls, returns scripted values
// or errors keyed by SecretId, and lets each test set its own state.
type fakeAPI struct {
	calls   []string
	values  map[string]fakeValue
	errs    map[string]error
	errOnce error
}

type fakeValue struct {
	str    *string
	binary []byte
	verID  string
}

func (f *fakeAPI) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	id := aws.ToString(in.SecretId)
	f.calls = append(f.calls, id)
	if f.errOnce != nil {
		err := f.errOnce
		f.errOnce = nil
		return nil, err
	}
	if err, ok := f.errs[id]; ok {
		return nil, err
	}
	if v, ok := f.values[id]; ok {
		out := &secretsmanager.GetSecretValueOutput{Name: in.SecretId, VersionId: aws.String(v.verID)}
		if v.str != nil {
			out.SecretString = v.str
		}
		if v.binary != nil {
			out.SecretBinary = v.binary
		}
		return out, nil
	}
	return nil, &types.ResourceNotFoundException{Message: aws.String("Secrets Manager can't find the specified secret.")}
}

func newAWSClient(t *testing.T, api secrets.SecretsManagerAPI) *secrets.AWSClient {
	t.Helper()
	c, err := secrets.NewAWSClientWithAPI(api, secrets.AWSConfig{
		Region:  "eu-west-1",
		Account: "123456789012",
		Env:     "test",
	})
	if err != nil {
		t.Fatalf("NewAWSClientWithAPI: %v", err)
	}
	return c
}

func TestAWSConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  secrets.AWSConfig
	}{
		{"missing region", secrets.AWSConfig{Account: "1", Env: "x"}},
		{"missing account", secrets.AWSConfig{Region: "r", Env: "x"}},
		{"missing env", secrets.AWSConfig{Region: "r", Account: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := secrets.NewAWSClientWithAPI(&fakeAPI{}, tc.cfg); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestAWSClient_NilAPI(t *testing.T) {
	_, err := secrets.NewAWSClientWithAPI(nil, secrets.AWSConfig{Region: "r", Account: "1", Env: "x"})
	if err == nil {
		t.Fatalf("expected error for nil api")
	}
}

func TestAWSClient_GetReturnsString(t *testing.T) {
	api := &fakeAPI{values: map[string]fakeValue{
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db": {
			str:   aws.String("postgres://user:pass@host:5432/db"),
			verID: "AWSCURRENT",
		},
	}}
	c := newAWSClient(t, api)

	val, ver, err := c.Get(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := string(val.Reveal()); got != "postgres://user:pass@host:5432/db" {
		t.Fatalf("Reveal() = %q", got)
	}
	if ver != "AWSCURRENT" {
		t.Fatalf("VersionID = %q, want AWSCURRENT", ver)
	}
	if len(api.calls) != 1 || api.calls[0] != "arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db" {
		t.Fatalf("calls = %v", api.calls)
	}
}

func TestAWSClient_GetReturnsBinary(t *testing.T) {
	api := &fakeAPI{values: map[string]fakeValue{
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/git": {
			binary: []byte{0x01, 0x02, 0x03},
			verID:  "v2",
		},
	}}
	c := newAWSClient(t, api)
	val, _, err := c.Get(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeGit))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(val.Reveal(), []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("Reveal() = %x", val.Reveal())
	}
}

func TestAWSClient_GetEmptyPayloadIsNotFound(t *testing.T) {
	api := &fakeAPI{values: map[string]fakeValue{
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db": {verID: "v1"}, // neither string nor binary
	}}
	c := newAWSClient(t, api)
	_, _, err := c.Get(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB))
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for empty payload, got %v", err)
	}
}

func TestAWSClient_GetInvalidRef(t *testing.T) {
	c := newAWSClient(t, &fakeAPI{})
	_, _, err := c.Get(context.Background(), secrets.SecretRef("not-a-ref"))
	if !errors.Is(err, secrets.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

// fakeAPIError is a smithy.APIError used to drive code-based mapping
// in mapAWSError without depending on a specific SDK error struct.
type fakeAPIError struct {
	code    string
	message string
}

func (e *fakeAPIError) Error() string                 { return e.code + ": " + e.message }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.message }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestAWSClient_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		apiErr  error
		want    error
		notWant error
	}{
		{
			name:   "ResourceNotFoundException → ErrSecretNotFound",
			apiErr: &types.ResourceNotFoundException{Message: aws.String("nope")},
			want:   secrets.ErrSecretNotFound,
		},
		{
			name:   "AccessDeniedException → ErrAccessDenied",
			apiErr: &fakeAPIError{code: "AccessDeniedException", message: "denied"},
			want:   secrets.ErrAccessDenied,
			// must NOT be conflated with NotFound
			notWant: secrets.ErrSecretNotFound,
		},
		{
			name:   "UnauthorizedOperation → ErrAccessDenied",
			apiErr: &fakeAPIError{code: "UnauthorizedOperation", message: "denied"},
			want:   secrets.ErrAccessDenied,
		},
		{
			name:   "ThrottlingException → ErrSecretStoreDown",
			apiErr: &fakeAPIError{code: "ThrottlingException", message: "rate limited"},
			want:   secrets.ErrSecretStoreDown,
		},
		{
			name: "ServiceUnavailable HTTP 503 → ErrSecretStoreDown",
			apiErr: &smithy.OperationError{
				ServiceID:     "Secrets Manager",
				OperationName: "GetSecretValue",
				Err:           &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 503}}},
			},
			want: secrets.ErrSecretStoreDown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{errs: map[string]error{
				"arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db": tc.apiErr,
			}}
			c := newAWSClient(t, api)
			_, _, err := c.Get(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB))
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
			if tc.notWant != nil && errors.Is(err, tc.notWant) {
				t.Fatalf("error should not match %v: %v", tc.notWant, err)
			}
		})
	}
}

func TestAWSClient_InvalidateIsNoop(t *testing.T) {
	api := &fakeAPI{}
	c := newAWSClient(t, api)

	if err := c.Invalidate(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("Invalidate should not call AWS, got calls: %v", api.calls)
	}
}

func TestAWSClient_InvalidateRejectsBadRef(t *testing.T) {
	c := newAWSClient(t, &fakeAPI{})
	if err := c.Invalidate(context.Background(), secrets.SecretRef("garbage")); !errors.Is(err, secrets.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

func TestAWSClient_IAMResourcePattern(t *testing.T) {
	c := newAWSClient(t, &fakeAPI{})
	got, err := c.IAMResourcePattern(secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB))
	if err != nil {
		t.Fatalf("IAMResourcePattern: %v", err)
	}
	// Secrets Manager appends a generated 6-char suffix to the ARN of
	// every secret; the canonical IAM Resource pattern uses ?? to
	// match it.
	want := "arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db-??????"
	if got != want {
		t.Fatalf("IAMResourcePattern =\n got %q\nwant %q", got, want)
	}
}

func TestAWSClient_IAMResourcePattern_RejectsInvalidRef(t *testing.T) {
	c := newAWSClient(t, &fakeAPI{})
	_, err := c.IAMResourcePattern(secrets.SecretRef("garbage"))
	if !errors.Is(err, secrets.ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef, got %v", err)
	}
}

func TestAWSClient_SecretValueDoesNotAppearInLogsOrErrors(t *testing.T) {
	const sensitive = "TOPSECRET-postgres://u:p@h/db"

	api := &fakeAPI{values: map[string]fakeValue{
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:spine/test/workspaces/acme/runtime_db": {
			str:   aws.String(sensitive),
			verID: "v1",
		},
	}}
	c := newAWSClient(t, api)

	val, ver, err := c.Get(context.Background(), secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Log the value through slog (the canonical Spine log path) and
	// assert the secret never lands in the log buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("fetched secret",
		"ref", string(secrets.WorkspaceRef("acme", secrets.PurposeRuntimeDB)),
		"version", ver,
		"value", val,
	)
	if strings.Contains(buf.String(), sensitive) {
		t.Fatalf("secret leaked to slog: %s", buf.String())
	}

	// Also assert the value doesn't appear when formatted via fmt
	// in either an error or %v context.
	wrapped := fmt.Errorf("oops: %v", val)
	if strings.Contains(wrapped.Error(), sensitive) {
		t.Fatalf("secret leaked through fmt error wrapping: %s", wrapped)
	}
}

func TestAWSClient_NotFoundDoesNotLeakSecretIDFormatToCallers(t *testing.T) {
	// Defense-in-depth: when AWS returns a generic "not found",
	// our wrapped error mentions the *ref* (canonical) but never
	// any secret value. Here the not-found path is the one with no
	// scripted value — fakeAPI returns ResourceNotFoundException.
	c := newAWSClient(t, &fakeAPI{})
	_, _, err := c.Get(context.Background(), secrets.WorkspaceRef("missing-ws", secrets.PurposeRuntimeDB))
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing-ws") {
		t.Fatalf("expected error to reference workspace ID, got %q", err.Error())
	}
}
