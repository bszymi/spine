package gateway

import "time"

// SetSSEHeartbeatForTest shortens the SSE heartbeat — and with it the
// bearer-revalidation interval (INIT-024 TASK-007) — for one server. Call
// it before the server's handler serves its first request.
func SetSSEHeartbeatForTest(s *Server, d time.Duration) { s.sseHeartbeat = d }
