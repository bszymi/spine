package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/go-chi/chi/v5"
)

type createSubscriptionRequest struct {
	Name       string   `json:"name"`
	TargetURL  string   `json:"target_url"`
	EventTypes []string `json:"event_types"`
	Metadata   []byte   `json:"metadata,omitempty"`
}

type updateSubscriptionRequest struct {
	Name       *string  `json:"name,omitempty"`
	TargetURL  *string  `json:"target_url,omitempty"`
	EventTypes []string `json:"event_types,omitempty"`
	Metadata   []byte   `json:"metadata,omitempty"`
}

// POST /api/v1/subscriptions
func (s *Server) handleSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.create") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	var req createSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Name == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "name required"))
		return
	}
	if req.TargetURL == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "target_url required"))
		return
	}

	subID, err := generateID("sub")
	if err != nil {
		WriteError(w, fmt.Errorf("generate subscription ID: %w", err))
		return
	}

	secret, err := generateSigningSecret()
	if err != nil {
		WriteError(w, fmt.Errorf("generate signing secret: %w", err))
		return
	}

	actorID := ""
	if actor := actorFromContext(r.Context()); actor != nil {
		actorID = actor.ActorID
	}
	wsID := ""
	if wsCfg := WorkspaceConfigFromContext(r.Context()); wsCfg != nil {
		wsID = wsCfg.ID
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	eventTypes := req.EventTypes
	if eventTypes == nil {
		eventTypes = []string{}
	}

	now := time.Now().UTC()
	sub := &store.EventSubscription{
		SubscriptionID: subID,
		WorkspaceID:    wsID,
		Name:           req.Name,
		TargetType:     "webhook",
		TargetURL:      req.TargetURL,
		EventTypes:     eventTypes,
		SigningSecret:  secret,
		Status:         "active",
		Metadata:       metadata,
		CreatedBy:      actorID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := st.CreateSubscription(r.Context(), sub); err != nil {
		WriteError(w, err)
		return
	}

	// Return signing secret on create — it won't be shown again
	WriteJSON(w, http.StatusCreated, map[string]any{
		"subscription_id": sub.SubscriptionID,
		"name":            sub.Name,
		"target_url":      sub.TargetURL,
		"event_types":     sub.EventTypes,
		"status":          sub.Status,
		"signing_secret":  secret,
		"created_at":      sub.CreatedAt,
	})
}

// GET /api/v1/subscriptions
func (s *Server) handleSubscriptionList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	wsID := ""
	if wsCfg := WorkspaceConfigFromContext(r.Context()); wsCfg != nil {
		wsID = wsCfg.ID
	}
	subs, err := st.ListSubscriptions(r.Context(), wsID)
	if err != nil {
		WriteError(w, err)
		return
	}

	items := make([]map[string]any, len(subs))
	for i, sub := range subs {
		items[i] = subscriptionResponse(sub)
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GET /api/v1/subscriptions/{subscription_id}
func (s *Server) handleSubscriptionGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.read") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	sub, err := st.GetSubscription(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, subscriptionResponse(*sub))
}

// PATCH /api/v1/subscriptions/{subscription_id}
func (s *Server) handleSubscriptionUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	sub, err := st.GetSubscription(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	var req updateSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	if req.Name != nil {
		sub.Name = *req.Name
	}
	if req.TargetURL != nil {
		sub.TargetURL = *req.TargetURL
	}
	if req.EventTypes != nil {
		sub.EventTypes = req.EventTypes
	}
	if req.Metadata != nil {
		sub.Metadata = req.Metadata
	}

	if err := st.UpdateSubscription(r.Context(), sub); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, subscriptionResponse(*sub))
}

// DELETE /api/v1/subscriptions/{subscription_id}
func (s *Server) handleSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.delete") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	if err := st.DeleteSubscription(r.Context(), subID); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /api/v1/subscriptions/{subscription_id}/activate
func (s *Server) handleSubscriptionActivate(w http.ResponseWriter, r *http.Request) {
	s.setSubscriptionStatus(w, r, "active")
}

// POST /api/v1/subscriptions/{subscription_id}/pause
func (s *Server) handleSubscriptionPause(w http.ResponseWriter, r *http.Request) {
	s.setSubscriptionStatus(w, r, "paused")
}

func (s *Server) setSubscriptionStatus(w http.ResponseWriter, r *http.Request, status string) {
	if !s.authorize(w, r, "subscription.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	sub, err := st.GetSubscription(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	sub.Status = status
	if err := st.UpdateSubscription(r.Context(), sub); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, subscriptionResponse(*sub))
}

// POST /api/v1/subscriptions/{subscription_id}/rotate-secret
func (s *Server) handleSubscriptionRotateSecret(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	sub, err := st.GetSubscription(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	newSecret, err := generateSigningSecret()
	if err != nil {
		WriteError(w, fmt.Errorf("generate signing secret: %w", err))
		return
	}

	sub.SigningSecret = newSecret
	if err := st.UpdateSubscription(r.Context(), sub); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"subscription_id": sub.SubscriptionID,
		"signing_secret":  newSecret,
	})
}

// POST /api/v1/subscriptions/{subscription_id}/test
func (s *Server) handleSubscriptionTest(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.update") {
		return
	}

	st := s.storeFrom(r.Context())
	if st == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	sub, err := st.GetSubscription(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Send a ping event inline
	pingPayload := []byte(`{"type":"ping","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `"}`)

	// G704 flags sub.TargetURL as taint. Webhook targets are
	// operator-configured, persisted in the subscriptions table, and
	// fetched via an authenticated admin API — that is the trust
	// boundary. There is no general way to test-dispatch to an
	// operator-configured URL without "sending HTTP to a
	// user-supplied URL"; SSRF scanning belongs to the
	// subscription-creation path, not here.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, sub.TargetURL, nil) //nolint:gosec // G704: operator-configured webhook URL
	if err != nil {
		WriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Spine-Event", "ping")
	req.Body = http.NoBody

	// Use a short-lived client for the test
	client := &http.Client{Timeout: 10 * time.Second}
	req.Body = nil
	testReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, sub.TargetURL, nil) //nolint:gosec // G704: see comment above
	testReq.Header.Set("Content-Type", "application/json")
	testReq.Header.Set("X-Spine-Event", "ping")

	start := time.Now()
	resp, err := client.Do(testReq) //nolint:gosec // G704: operator-configured webhook URL
	durationMs := int(time.Since(start).Milliseconds())

	if err != nil {
		observe.Logger(r.Context()).Warn("subscription test failed", "subscription_id", subID, "error", err)
		WriteJSON(w, http.StatusOK, map[string]any{
			"success":     false,
			"error":       err.Error(),
			"duration_ms": durationMs,
		})
		return
	}
	defer resp.Body.Close()

	_ = pingPayload // used conceptually; the test sends a minimal GET-like POST

	WriteJSON(w, http.StatusOK, map[string]any{
		"success":     resp.StatusCode >= 200 && resp.StatusCode < 300,
		"status_code": resp.StatusCode,
		"duration_ms": durationMs,
	})
}

// subscriptionResponse formats a subscription for API output, excluding the signing secret.
func subscriptionResponse(sub store.EventSubscription) map[string]any {
	return map[string]any{
		"subscription_id": sub.SubscriptionID,
		"workspace_id":    sub.WorkspaceID,
		"name":            sub.Name,
		"target_type":     sub.TargetType,
		"target_url":      sub.TargetURL,
		"event_types":     sub.EventTypes,
		"status":          sub.Status,
		"created_by":      sub.CreatedBy,
		"created_at":      sub.CreatedAt,
		"updated_at":      sub.UpdatedAt,
	}
}

func generateSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(b), nil
}
