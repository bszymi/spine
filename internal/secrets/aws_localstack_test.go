//go:build integration

package secrets_test

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/secrets/contract"
)

// TestAWSClient_Contract_LocalStack runs the cross-provider contract
// suite against a LocalStack-backed Secrets Manager. The test is
// gated by the `integration` build tag and skipped if the LocalStack
// endpoint is unreachable.
//
//	docker compose -f docker-compose.test.yaml up -d spine-test-localstack
//	make test-integration
func TestAWSClient_Contract_LocalStack(t *testing.T) {
	endpoint := os.Getenv("SPINE_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	if !endpointReachable(t, endpoint) {
		t.Skipf("LocalStack endpoint %s not reachable; skipping", endpoint)
	}

	const region = "us-east-1"
	const account = "000000000000"
	const env = "test"

	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("LoadDefaultConfig: %v", err)
	}
	api := secretsmanager.NewFromConfig(awsCfg, func(o *secretsmanager.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	cfg := secrets.AWSConfig{Region: region, Account: account, Env: env}
	client, err := secrets.NewAWSClientWithAPI(api, cfg)
	if err != nil {
		t.Fatalf("NewAWSClientWithAPI: %v", err)
	}

	// Seed the contract fixtures, deleting any prior incarnation.
	seed := func(ref secrets.SecretRef, value string) {
		t.Helper()
		ws, purpose, err := secrets.ParseRef(ref)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", ref, err)
		}
		name := "spine/" + env + "/workspaces/" + ws + "/" + purpose
		ctx := context.Background()
		_, _ = api.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
			SecretId:                   aws.String(name),
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
		// LocalStack delete is async-ish; retry create on collision.
		deadline := time.Now().Add(10 * time.Second)
		for {
			_, err = api.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
				Name:         aws.String(name),
				SecretString: aws.String(value),
			})
			if err == nil {
				return
			}
			var conflict *smtypes.ResourceExistsException
			if !errors.As(err, &conflict) || time.Now().After(deadline) {
				t.Fatalf("CreateSecret(%s): %v", name, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	seed(contract.RefRuntimeDB, contract.FixtureRuntimeDBValue)
	seed(contract.RefGit, contract.FixtureGitValue)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, ref := range []secrets.SecretRef{contract.RefRuntimeDB, contract.RefGit} {
			ws, purpose, _ := secrets.ParseRef(ref)
			name := "spine/" + env + "/workspaces/" + ws + "/" + purpose
			_, _ = api.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
				SecretId:                   aws.String(name),
				ForceDeleteWithoutRecovery: aws.Bool(true),
			})
		}
	})

	contract.RunContract(t, func() secrets.SecretClient {
		return client
	})
}

func endpointReachable(t *testing.T, endpoint string) bool {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Host
	if host == "" {
		host = u.Path
	}
	// Test-only reachability probe for the LocalStack endpoint;
	// the host comes from a developer-controlled env var.
	//nolint:gosec // G304/G107/G704: integration test, controlled input.
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
