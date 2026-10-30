package githttp

import "regexp"

// The request path a read or a push may have (INIT-024 TASK-004, from
// TASK-006's security review). git-http-backend matches its routes
// (http-backend.c services[]) against the END of PATH_INFO and serves
// everything before the match as a directory under GIT_PROJECT_ROOT —
// whatever is there. This package used to match path suffixes too, so a
// path prefix inside the served tree reached git-http-backend as a
// repository (another git directory inside the working tree), token-less
// for a trusted peer; dot segments reached it as well and were refused
// only by its alias check (a 500). The classification now accepts exactly
// the routes git serves at the repository root, so the directory
// git-http-backend serves is GIT_PROJECT_ROOT itself:
//
//   - "/info/refs" (GET; the query decides read or push, infoRefsServiceOf);
//   - "/git-upload-pack" (POST, a read) and "/git-receive-pack" (POST, a
//     push);
//   - the dumb-protocol files (GET, reads): "/HEAD",
//     "/objects/info/{alternates,http-alternates,packs}", a loose object
//     "/objects/<2 hex>/<38 or 62 hex>" and a pack or its index
//     "/objects/pack/pack-<40 or 64 hex>.{pack,idx}" — git's own patterns,
//     lower-case hex as git's.
//
// Every other path — a prefix, a "." or ".." segment, a doubled or
// trailing slash, any other name — is neither a read nor a push, so
// ServeHTTP refuses it (403) before CGI. git-upload-archive stays
// unsupported, as before.
const (
	infoRefsPath    = "/info/refs"
	uploadPackPath  = "/git-upload-pack"
	receivePackPath = "/git-receive-pack"
)

// dumbProtocolPath is git-http-backend's GET routes other than info/refs,
// anchored at the repository root.
var dumbProtocolPath = regexp.MustCompile(`^/(?:HEAD|objects/info/(?:alternates|http-alternates|packs)|` +
	`objects/[0-9a-f]{2}/(?:[0-9a-f]{38}|[0-9a-f]{62})|objects/pack/pack-(?:[0-9a-f]{40}|[0-9a-f]{64})\.(?:pack|idx))$`)
