package workflow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// INIT-023 TASK-011 §7 / AC 23: the write path refuses every body the
// projection would refuse, so the API never accepts a workflow that is
// then silently not projected (or quarantined).

// unquotedVersionYAML renders minimalYAML with an UNQUOTED version, which
// strict Parse reads as the string "1.0" / "2026-10-06" but the
// projection's JSON carries as a number / an RFC 3339 timestamp.
func unquotedVersionYAML(id, version string) string {
	out := strings.ReplaceAll(minimalYAML, "{ID}", id)
	return strings.ReplaceAll(out, `"{V}"`, version)
}

func headSHA(t *testing.T, repo string) string {
	t.Helper()
	sha, err := git.NewCLIClient(repo).Head(context.Background())
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	return sha
}

func TestService_Create_RejectsRoundTripBodies(t *testing.T) {
	for _, version := range []string{"1.0", "2026-10-06"} {
		t.Run(version, func(t *testing.T) {
			svc, repo := newSvc(t)
			before := headSHA(t, repo)
			body := unquotedVersionYAML("task-default", version)
			if _, err := workflow.Parse("workflows/task-default.yaml", []byte(body)); err != nil {
				t.Fatalf("precondition: Parse accepts the body, got %v", err)
			}

			_, err := svc.Create(ctxWithActor(), "task-default", body)
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrValidationFailed {
				t.Fatalf("expected ErrValidationFailed, got %v", err)
			}
			if !strings.Contains(err.Error(), "round-trip") {
				t.Errorf("error should name the round trip: %v", err)
			}
			if after := headSHA(t, repo); after != before {
				t.Fatalf("a commit was created: %s -> %s", before, after)
			}
			if _, statErr := os.Stat(filepath.Join(repo, "workflows/task-default.yaml")); !os.IsNotExist(statErr) {
				t.Fatalf("file must not be written: %v", statErr)
			}
		})
	}
}

func TestService_Update_RejectsRoundTripBodies(t *testing.T) {
	for _, version := range []string{"1.0", "2026-10-06"} {
		t.Run(version, func(t *testing.T) {
			svc, repo := newSvc(t)
			if _, err := svc.Create(ctxWithActor(), "task-default", fmtYAML("task-default", "0.9")); err != nil {
				t.Fatalf("Create: %v", err)
			}
			before := headSHA(t, repo)
			original, err := os.ReadFile(filepath.Join(repo, "workflows/task-default.yaml"))
			if err != nil {
				t.Fatal(err)
			}

			_, err = svc.Update(ctxWithActor(), "task-default", unquotedVersionYAML("task-default", version))
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrValidationFailed {
				t.Fatalf("expected ErrValidationFailed, got %v", err)
			}
			if after := headSHA(t, repo); after != before {
				t.Fatalf("a commit was created: %s -> %s", before, after)
			}
			now, err := os.ReadFile(filepath.Join(repo, "workflows/task-default.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(now, original) {
				t.Fatal("the existing file must be left untouched")
			}
		})
	}
}

func TestService_ValidateBody_ReportsProjectionRoundTripRule(t *testing.T) {
	svc, _ := newSvc(t)
	for _, version := range []string{"1.0", "2026-10-06"} {
		result := svc.ValidateBody(ctxWithActor(), "task-default", unquotedVersionYAML("task-default", version))
		if result.Status != "failed" {
			t.Fatalf("%s: expected failed, got %+v", version, result)
		}
		if len(result.Errors) != 1 || result.Errors[0].RuleID != "projection_round_trip" {
			t.Fatalf("%s: expected one projection_round_trip error, got %+v", version, result.Errors)
		}
		if workflow.ProjectionRoundTripRuleID != "projection_round_trip" {
			t.Fatalf("rule id constant drifted: %q", workflow.ProjectionRoundTripRuleID)
		}
	}
	// A strict-parse failure keeps RuleID "parse".
	result := svc.ValidateBody(ctxWithActor(), "task-default", fmtYAML("task-default", "1.0")+"Steps: []\n")
	if result.Status != "failed" || len(result.Errors) != 1 || result.Errors[0].RuleID != "parse" {
		t.Fatalf("expected a parse failure, got %+v", result)
	}
}
