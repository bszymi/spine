package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smithy "github.com/aws/smithy-go"
)

// AWSConfig configures the AWS Secrets Manager-backed SecretClient.
// All three fields are required so that ARN/name construction is
// deterministic from a SecretRef alone.
type AWSConfig struct {
	Region  string // AWS region, e.g. "eu-west-1"
	Account string // 12-digit account ID
	Env     string // environment prefix, e.g. "prod" or "staging"
}

// SecretsManagerAPI is the subset of the AWS SDK secretsmanager
// surface that the AWS provider depends on. Tests substitute a fake.
type SecretsManagerAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// AWSClient implements SecretClient against AWS Secrets Manager. It is
// read-only — rotation and seeding are platform-side concerns
// (ADR-010 § Decision).
type AWSClient struct {
	api SecretsManagerAPI
	cfg AWSConfig
}

// Compile-time assertion: AWSClient implements SecretClient.
var _ SecretClient = (*AWSClient)(nil)

// NewAWSClient builds an AWSClient using the default AWS config chain
// (IAM role, env, shared config). Region is taken from cfg.
func NewAWSClient(ctx context.Context, cfg AWSConfig, optFns ...func(*config.LoadOptions) error) (*AWSClient, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	awsCfg, err := config.LoadDefaultConfig(
		ctx,
		append([]func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}, optFns...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &AWSClient{api: secretsmanager.NewFromConfig(awsCfg), cfg: cfg}, nil
}

// NewAWSClientWithAPI builds an AWSClient with a caller-supplied SDK
// client. Used by tests (fake) and by the LocalStack integration
// suite (real SDK pointed at a custom endpoint).
func NewAWSClientWithAPI(api SecretsManagerAPI, cfg AWSConfig) (*AWSClient, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if api == nil {
		return nil, errors.New("aws secret client: api is nil")
	}
	return &AWSClient{api: api, cfg: cfg}, nil
}

func (c AWSConfig) validate() error {
	if c.Region == "" {
		return errors.New("aws secret client: region is required")
	}
	if c.Account == "" {
		return errors.New("aws secret client: account is required")
	}
	if c.Env == "" {
		return errors.New("aws secret client: env is required")
	}
	return nil
}

// Get fetches the value for ref from AWS Secrets Manager.
func (c *AWSClient) Get(ctx context.Context, ref SecretRef) (SecretValue, VersionID, error) {
	if _, _, err := ParseRef(ref); err != nil {
		return SecretValue{}, "", err
	}
	// Pass the partial ARN (without the random 6-char suffix) so that
	// AWS resolves the secret in the configured account/region rather
	// than wherever the active credentials happen to point. Secrets
	// Manager accepts partial ARNs as SecretId (see "Finding a secret
	// from a partial ARN" in the AWS docs).
	out, err := c.api.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(c.partialARN(ref)),
	})
	if err != nil {
		return SecretValue{}, "", mapAWSError(err, ref)
	}

	var raw []byte
	switch {
	case out.SecretString != nil:
		raw = []byte(*out.SecretString)
	case out.SecretBinary != nil:
		raw = out.SecretBinary
	default:
		return SecretValue{}, "", fmt.Errorf("%w: empty payload for %s", ErrSecretNotFound, ref)
	}

	var vid VersionID
	if out.VersionId != nil {
		vid = VersionID(*out.VersionId)
	}
	return NewSecretValue(raw), vid, nil
}

// Invalidate is a no-op for AWS Secrets Manager. Spine-side caches
// (binding cache, connection pool) live above SecretClient and own
// their invalidation; AWS itself has no client-side cache to drop.
func (c *AWSClient) Invalidate(_ context.Context, ref SecretRef) error {
	if _, _, err := ParseRef(ref); err != nil {
		return err
	}
	return nil
}

// secretName is the deterministic Secrets Manager friendly name for
// a ref:
//
//	spine/{env}/workspaces/{workspace_id}/{purpose}
func (c *AWSClient) secretName(ref SecretRef) string {
	ws, purpose, _ := ParseRef(ref) // ref is validated before this call
	return fmt.Sprintf("spine/%s/workspaces/%s/%s", c.cfg.Env, ws, purpose)
}

// partialARN returns the partial ARN for a ref — the ARN without
// Secrets Manager's generated 6-character random suffix. AWS accepts
// this form as SecretId, and using it pins the resolution to the
// configured account and region.
func (c *AWSClient) partialARN(ref SecretRef) string {
	return fmt.Sprintf(
		"arn:aws:secretsmanager:%s:%s:secret:%s",
		c.cfg.Region, c.cfg.Account, c.secretName(ref),
	)
}

// IAMResourcePattern returns an IAM-policy-suitable Resource pattern
// for ref. Secrets Manager appends a generated 6-character suffix to
// every secret ARN, so an exact ARN cannot be hardcoded; the canonical
// IAM pattern uses a `-??????` (or `-*`) suffix. This helper returns:
//
//	arn:aws:secretsmanager:{region}:{account}:secret:{name}-??????
//
// Use this in IAM policy documents that grant
// secretsmanager:GetSecretValue / DescribeSecret on a specific ref.
func (c *AWSClient) IAMResourcePattern(ref SecretRef) (string, error) {
	if _, _, err := ParseRef(ref); err != nil {
		return "", err
	}
	return c.partialARN(ref) + "-??????", nil
}

// mapAWSError translates AWS SDK errors into Spine sentinels.
//
// Network / throttling / 5xx / unknown errors map to ErrSecretStoreDown
// so callers can treat the secret store as transiently unavailable.
//
// The ref appears in the wrapped error to aid debugging; the secret
// value never does (this function never sees the value).
func mapAWSError(err error, ref SecretRef) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ResourceNotFoundException":
			return fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
		case "AccessDeniedException", "UnauthorizedOperation", "UnauthorizedException":
			return fmt.Errorf("%w: %s", ErrAccessDenied, ref)
		case "InvalidParameterException", "InvalidRequestException":
			return fmt.Errorf("aws secrets manager: invalid request for %s: %s: %s",
				ref, apiErr.ErrorCode(), apiErr.ErrorMessage())
		}
	}
	return fmt.Errorf("%w: %s: %v", ErrSecretStoreDown, ref, err)
}
