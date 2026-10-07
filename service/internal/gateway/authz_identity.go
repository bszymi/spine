package gateway

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/go-chi/chi/v5"
)

// authorizeActorSkillChange is authorize() for the actor-skill grant
// routes (INIT-023 TASK-013, audit F6). Beyond the role map it refuses
// a non-admin caller targeting its own actor id, with a distinct
// reason, and audits that refusal: an actor granting itself a review
// skill is the most suspicious request these routes see.
//
// targetActorID must already be decoded (actorIDParam). Dev mode (no
// actor in the context) keeps authorize()'s behaviour: allow-all with
// SPINE_DEV_MODE, 401 otherwise.
func (s *Server) authorizeActorSkillChange(w http.ResponseWriter, r *http.Request, op auth.Operation, targetActorID string) bool {
	caller := actorFromContext(r.Context())
	if caller == nil {
		return s.authorize(w, r, op)
	}
	if err := auth.AuthorizeActorSkillChange(caller, op, targetActorID); err != nil {
		if errorReason(err) == auth.ReasonSelfSkillChange {
			// The caller's actor_id and the trace_id are already on the
			// logger (authMiddleware's observe.WithActorID).
			observe.AuditLog(r.Context(), "actor_skill_change_refused",
				"operation", string(op),
				"target_actor_id", targetActorID,
				"reason", auth.ReasonSelfSkillChange)
		}
		WriteError(w, err)
		return false
	}
	return true
}

// actorIDParam returns the {actor_id} route parameter decoded exactly
// once, the way the client meant it.
//
// chi routes on r.URL.RawPath whenever it is set, and net/http sets it
// only when the client used a non-canonical escaping — so for
// "/actors/op%2D1/skills/x" chi.URLParam returns "op%2D1", still
// escaped, while a plain "/actors/op-1/..." yields "op-1" from the
// already-decoded Path. Without decoding, the self check would compare
// "op%2D1" with the caller's "op-1" and treat a self-grant as a grant
// to "another" actor. Decoding only when RawPath is set (rather than
// always) avoids a second decode of a Path that net/http already
// decoded: "op%252D1" means the literal id "op%2D1", not "op-1".
//
// A malformed escape is a 400 invalid_params. net/http itself refuses a
// request line with one, so this is reachable only from an in-process
// caller or a front end that hands the raw path through; it is answered
// before authorization, and the decoded id is what both the
// authorization check and the store call see. The target actor is
// deliberately not looked up before authorization: a 404-before-403
// order would give non-admins an actor-existence oracle.
func actorIDParam(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "actor_id")
	if r.URL.RawPath == "" {
		return raw, nil
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", domain.NewError(domain.ErrInvalidParams, "actor_id is not a valid path segment")
	}
	return decoded, nil
}

// errorReason returns the "reason" entry of a SpineError's
// map[string]string detail, or "".
func errorReason(err error) string {
	var se *domain.SpineError
	if !errors.As(err, &se) {
		return ""
	}
	d, ok := se.Detail.(map[string]string)
	if !ok {
		return ""
	}
	return d["reason"]
}
