package githttp

// Fair git slots (INIT-024 TASK-005, review finding N-13).
//
// The endpoint used to take one process-wide semaphore of five slots for
// every git request, before a push's command section was read. Only the
// server's whole-request ReadTimeout bounded that read, so one
// authenticated member — of any workspace — could hold every slot with a
// few stalled requests and answer 503 to every tenant's git traffic.
//
// Now a request takes a slot from three caps at once:
//
//   - a per-principal cap: one (workspace, actor) pair, or — for a
//     token-less read (a trusted-CIDR runner, the development no-auth
//     flag) — one (workspace, peer address) pair. A token-less request is
//     never keyed on an empty actor ID, which every such request would
//     share;
//   - a per-workspace cap, so the members of one workspace cannot take
//     every slot of the process;
//   - the process-wide ceiling, kept as a backstop for the host.
//
// Defaults (owner question 1): 2 per principal, 4 per workspace, 8 in
// all. A request that finds any cap full is answered 503 with
// Retry-After at once (it never waits), as before.
//
// A push takes its slot only after its command section has been read
// (under the command-read deadline) and admitted provisionally, so a
// client that stalls before or inside the command section holds none
// (prereceive). The caps are in-process: replicas do not share them,
// exactly as the SSE limiter's do.

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// Default slot caps of the git endpoint (INIT-024 TASK-005, owner
// question 1) and its command-read deadline.
const (
	DefaultMaxConcurrentPerActor     = 2
	DefaultMaxConcurrentPerWorkspace = 4
	DefaultMaxConcurrent             = 8
	// DefaultCommandReadTimeout bounds the read of a push's command
	// section (and its push-options section), separately from the
	// server's whole-request ReadTimeout.
	DefaultCommandReadTimeout = 10 * time.Second
)

// slotPrincipal is who a git request counts against. An authenticated
// request is its (workspace, actor ID); a request with no actor (or an
// empty actor ID) is its (workspace, peer address). peer separates the
// two key spaces, so no actor ID can collide with an address.
type slotPrincipal struct {
	workspace string
	peer      bool
	id        string
}

// slotPrincipalOf keys r. The workspace is the one the gateway resolved
// the request to (observe.WithWorkspaceID, set by handleGit before every
// delegation; "" when the handler is used without the gateway, which
// then has one workspace). The actor is the authenticated one the
// gateway attached; a token-less read has none and is keyed on its
// direct TCP peer (r.RemoteAddr: the gateway grants token-less access to
// direct peers only and never rewrites RemoteAddr).
func slotPrincipalOf(r *http.Request) slotPrincipal {
	ws := observe.WorkspaceID(r.Context())
	if a := domain.ActorFromContext(r.Context()); a != nil && a.ActorID != "" {
		return slotPrincipal{workspace: ws, id: a.ActorID}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return slotPrincipal{workspace: ws, peer: true, id: host}
}

// slotCap names the cap that refused a slot, for the log and the reply.
type slotCap string

const (
	capPrincipal slotCap = "principal"
	capWorkspace slotCap = "workspace"
	capGlobal    slotCap = "global"
)

// gitSlots is the three-level limiter. Safe for concurrent use.
type gitSlots struct {
	perPrincipal, perWorkspace, global int

	mu         sync.Mutex
	principals map[slotPrincipal]int
	workspaces map[string]int
	total      int
}

func newGitSlots(perPrincipal, perWorkspace, global int) *gitSlots {
	return &gitSlots{
		perPrincipal: perPrincipal,
		perWorkspace: perWorkspace,
		global:       global,
		principals:   map[slotPrincipal]int{},
		workspaces:   map[string]int{},
	}
}

// acquire takes one slot for p from every cap, or none: it returns the
// release (idempotent) on success, or nil and the first full cap.
func (s *gitSlots) acquire(p slotPrincipal) (func(), slotCap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.principals[p] >= s.perPrincipal:
		return nil, capPrincipal
	case s.workspaces[p.workspace] >= s.perWorkspace:
		return nil, capWorkspace
	case s.total >= s.global:
		return nil, capGlobal
	}
	s.principals[p]++
	s.workspaces[p.workspace]++
	s.total++
	var once sync.Once
	return func() { once.Do(func() { s.release(p) }) }, ""
}

func (s *gitSlots) release(p slotPrincipal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principals[p]--
	if s.principals[p] <= 0 {
		delete(s.principals, p)
	}
	s.workspaces[p.workspace]--
	if s.workspaces[p.workspace] <= 0 {
		delete(s.workspaces, p.workspace)
	}
	s.total--
}
