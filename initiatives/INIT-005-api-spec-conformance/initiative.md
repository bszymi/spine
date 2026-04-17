---
id: INIT-005
type: Initiative
title: "API Spec Conformance"
status: Completed
owner: bszymi
created: 2026-03-27
links:
  - type: related_to
    target: /initiatives/INIT-002-implementation/initiative.md
---

# INIT-005 — API Spec Conformance

---

## 1. Intent

Align the HTTP API implementation with the OpenAPI specification defined in `/api/spec.yaml`. All routes are registered and have working handlers, but request/response schemas diverge from the spec in field names, missing fields, structural shape, and parameter handling. This initiative closes those gaps so the spec is the authoritative contract.

---

## 2. Scope

### In scope

- Response schema conformance (field names, required fields, structure)
- Request schema conformance (WriteContext object, StepSubmitRequest, supersede body)
- Query parameter conformance (link filters, pagination, param naming)
- One spec correction (query.graph `root` param)
- Low-priority operational improvements (async rebuild, health metrics)

### Out of scope

- New endpoints not in the spec
- Endpoints implemented beyond the spec (discussions, tokens, divergence, assignments, metrics)
- Performance or load testing
- Authentication/authorization changes

---

## 3. Success Criteria

1. Every response from spec-defined endpoints matches its OpenAPI schema exactly
2. Every request body and query parameter matches the spec
3. The spec itself is corrected where the implementation is demonstrably better
4. Existing tests continue to pass after conformance changes

---

## 4. Primary Artifacts Produced

- Updated handler files in `internal/gateway/handlers_*.go`
- Response DTO layer where domain models don't match API schemas
- Updated artifact/workflow services to return commit SHAs
- One spec fix in `api/spec.yaml`

---

## 5. Risks

- **Breaking existing clients:** Response shape changes may break CLI or test clients that depend on current field names
- **Service layer changes:** Returning `commit_sha` and `write_mode` requires changes to the artifact service, not just the gateway

Mitigations:

- Update CLI client and tests in the same task as handler changes
- Service layer changes are isolated to return values, not behavior

---

## 6. Work Breakdown

### Epics

```
/initiatives/INIT-005-api-spec-conformance/
  /epics/
    /EPIC-001-spec-conformance/
```

### EPIC-001 — Spec Conformance

Purpose: Fix all request/response schema mismatches between the API implementation and `api/spec.yaml`, plus one spec correction.

---

## 7. Exit Criteria

INIT-005 may be marked complete when:

- All handler responses match their OpenAPI schemas
- All request bodies and query parameters match the spec
- The spec is updated where implementation is better
- All existing tests pass
