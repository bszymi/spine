package gateway

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/go-chi/chi/v5"
)

// GET /api/v1/subscriptions/{subscription_id}/deliveries
func (s *Server) handleDeliveryList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.read") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	deliveries, err := st.ListDeliveries(r.Context(), subID, status, limit)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": deliveries})
}

// GET /api/v1/subscriptions/{subscription_id}/deliveries/{delivery_id}
func (s *Server) handleDeliveryGet(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.read") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	deliveryID := chi.URLParam(r, "delivery_id")
	delivery, err := st.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Also fetch delivery log entries
	logs, err := st.ListDeliveryHistory(r.Context(), store.DeliveryHistoryQuery{
		SubscriptionID: delivery.SubscriptionID,
		Limit:          20,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	// Filter logs to this delivery only
	var deliveryLogs []store.DeliveryLogEntry
	for _, log := range logs {
		if log.DeliveryID == deliveryID {
			deliveryLogs = append(deliveryLogs, log)
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"delivery": delivery,
		"attempts": deliveryLogs,
	})
}

// POST /api/v1/subscriptions/{subscription_id}/deliveries/{delivery_id}/replay
func (s *Server) handleDeliveryReplay(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.update") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	deliveryID := chi.URLParam(r, "delivery_id")
	delivery, err := st.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		WriteError(w, err)
		return
	}

	if delivery.Status != "failed" && delivery.Status != "dead" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "only failed or dead deliveries can be replayed"))
		return
	}

	// Reset to pending for re-delivery
	if err := st.UpdateDeliveryStatus(r.Context(), deliveryID, "pending", "", nil); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"delivery_id": deliveryID,
		"status":      "pending",
		"replayed_at": time.Now().UTC(),
	})
}

// GET /api/v1/subscriptions/{subscription_id}/stats
func (s *Server) handleSubscriptionStats(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "subscription.read") {
		return
	}

	st, ok := s.needStore(w, r)
	if !ok {
		return
	}

	subID := chi.URLParam(r, "subscription_id")
	stats, err := st.GetDeliveryStats(r.Context(), subID)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, stats)
}
