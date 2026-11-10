package artifact_test

// ADR-017 §1–§3: the Assumption artifact type and the depends_on,
// supports and refutes link types.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
)

const assumptionDoc = `---
id: ASM-001
type: Assumption
title: "Bulk edits stay under one second for the largest catalogue"
status: Untested
created: 2026-11-10
proposed_by: ai
impact: high
---

# ASM-001

## Claim
`

func assumptionWith(replaceOld, replaceNew string) []byte {
	return []byte(strings.Replace(assumptionDoc, replaceOld, replaceNew, 1))
}

func TestAssumption_ParsesAndValidates(t *testing.T) {
	a, err := artifact.Parse("assumptions/ASM-001-bulk-edits.md", []byte(assumptionDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Type != domain.ArtifactTypeAssumption || a.ID != "ASM-001" || a.Status != domain.StatusUntested {
		t.Fatalf("parsed %+v", a)
	}
	// Optional fields are carried as metadata; Core does not interpret them.
	if a.Metadata["proposed_by"] != "ai" || a.Metadata["impact"] != "high" || a.Metadata["created"] != "2026-11-10" {
		t.Fatalf("metadata = %v", a.Metadata)
	}
	if r := artifact.Validate(a); r.Status != "passed" {
		t.Fatalf("Validate = %s: %+v", r.Status, r.Errors)
	}
}

// An Assumption must be discovered, or it is never projected: discovery
// skips any Markdown file IsArtifact refuses.
func TestAssumption_IsDiscovered(t *testing.T) {
	if !artifact.IsArtifact([]byte(assumptionDoc)) {
		t.Fatal("IsArtifact refused an Assumption")
	}
}

func TestAssumption_ParseRequiresIDAndCreated(t *testing.T) {
	for name, doc := range map[string][]byte{
		"id":      assumptionWith("id: ASM-001\n", ""),
		"created": assumptionWith("created: 2026-11-10\n", ""),
	} {
		_, err := artifact.Parse("assumptions/x.md", doc)
		if err == nil || !strings.Contains(err.Error(), name+" (Assumption)") {
			t.Errorf("missing %s: err = %v", name, err)
		}
	}
}

func TestAssumption_ValidateRefusals(t *testing.T) {
	base := func() *domain.Artifact {
		return &domain.Artifact{
			Path: "assumptions/ASM-001-x.md", ID: "ASM-001", Type: domain.ArtifactTypeAssumption,
			Title: "A claim", Status: domain.StatusProposed,
			Metadata: map[string]string{"created": "2026-11-10"},
		}
	}
	for _, c := range []struct {
		name   string
		mutate func(*domain.Artifact)
		field  string
		msg    string
	}{
		{"bad id", func(a *domain.Artifact) { a.ID = "ASM-1" }, "id", "invalid ID format"},
		{"adr id", func(a *domain.Artifact) { a.ID = "ADR-001" }, "id", "invalid ID format"},
		{"no id", func(a *domain.Artifact) { a.ID = "" }, "id", "required for Assumption"},
		{"no created", func(a *domain.Artifact) { delete(a.Metadata, "created") }, "created", "required for Assumption"},
		{"bad created", func(a *domain.Artifact) { a.Metadata["created"] = "10/11/2026" }, "created", "invalid date format"},
		{"adr status", func(a *domain.Artifact) { a.Status = domain.StatusAccepted }, "status", "invalid status"},
		{"task status", func(a *domain.Artifact) { a.Status = domain.StatusCompleted }, "status", "invalid status"},
	} {
		a := base()
		c.mutate(a)
		assertHasError(t, artifact.Validate(a), c.field, c.msg)
	}
	if r := artifact.Validate(base()); r.Status != "passed" {
		t.Fatalf("base must validate: %+v", r.Errors)
	}
	for _, s := range domain.ValidStatusesForType(domain.ArtifactTypeAssumption) {
		a := base()
		a.Status = s
		if r := artifact.Validate(a); r.Status != "passed" {
			t.Errorf("status %q refused: %+v", s, r.Errors)
		}
	}
	// The new statuses belong to Assumption only.
	adr := validADR()
	adr.Status = domain.StatusUntested
	assertHasError(t, artifact.Validate(adr), "status", "invalid status")
}

func TestAssumption_IDFieldValidation(t *testing.T) {
	a := &domain.Artifact{Type: domain.ArtifactTypeAssumption}
	if e := artifact.ValidateField(a, "id"); e == nil {
		t.Fatal("empty id accepted")
	}
	a.ID = "ASM-012"
	if e := artifact.ValidateField(a, "id"); e != nil {
		t.Fatalf("ASM-012 refused: %v", e)
	}
}

func TestAssumptionLinkTypes_Validate(t *testing.T) {
	a := validADR()
	a.Links = []domain.Link{
		{Type: domain.LinkTypeDependsOn, Target: "/assumptions/ASM-001-x.md"},
		{Type: domain.LinkTypeSupports, Target: "/assumptions/ASM-001-x.md"},
		{Type: domain.LinkTypeRefutes, Target: "/assumptions/ASM-002-y.md"},
	}
	if r := artifact.Validate(a); r.Status != "passed" {
		t.Fatalf("Validate = %s: %+v", r.Status, r.Errors)
	}
	// Canonical-target rule still applies to the new types.
	a.Links = []domain.Link{{Type: domain.LinkTypeDependsOn, Target: "assumptions/ASM-001-x.md"}}
	assertHasError(t, artifact.Validate(a), "links[0].target", "canonical path")
}

func TestAssumption_BuildArtifactPath(t *testing.T) {
	got := artifact.BuildArtifactPath(domain.ArtifactTypeAssumption, "ASM-003", "bulk-edits", "assumptions")
	if got != "assumptions/asm-003-bulk-edits.md" {
		t.Fatalf("path = %q", got)
	}
}

func TestAssumption_NextID(t *testing.T) {
	client, repo := setupIDAllocatorRepo(t)
	ctx := context.Background()

	id, err := artifact.NextID(ctx, client, "assumptions", domain.ArtifactTypeAssumption, "HEAD")
	if err != nil {
		t.Fatalf("NextID on an empty directory: %v", err)
	}
	if id != "ASM-001" {
		t.Fatalf("first id = %s, want ASM-001", id)
	}

	testutil.WriteFile(t, repo, "assumptions/ASM-001-first.md", assumptionDoc)
	testutil.WriteFile(t, repo, "assumptions/ASM-004-fourth.md",
		strings.Replace(assumptionDoc, "ASM-001", "ASM-004", 2))
	testutil.GitAdd(t, repo, ".", "add assumptions")

	id, err = artifact.NextID(ctx, client, "assumptions", domain.ArtifactTypeAssumption, "HEAD")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != "ASM-005" {
		t.Fatalf("next id = %s, want ASM-005", id)
	}
}
