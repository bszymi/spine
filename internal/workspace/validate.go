package workspace

import (
	"fmt"
	"regexp"
)

// maxIDLength caps workspace IDs so the provisioner's derived
// PostgreSQL database name (`spine_ws_<sanitized-id>`) stays under the
// default 63-byte NAMEDATALEN. Anything longer is silently truncated
// by PostgreSQL on CREATE DATABASE while replaceDatabaseInURL returns
// the untruncated name, producing a post-provision connection that
// points at a different database (or collides with a sibling ID that
// shares the same truncated prefix).
const maxIDLength = 63 - len("spine_ws_") // 54

// validIDPattern is the conservative allowlist for workspace IDs.
//
// Rules:
//   - start with an ASCII letter or digit
//   - otherwise: ASCII letters, digits, underscore, or hyphen
//   - total length 1..54 (see maxIDLength)
//
// The leading character rule blocks flag-injection-shaped IDs (-rm) and
// hidden-file names. Disallowing "/", "\\", ".", whitespace, and control
// characters keeps a caller-supplied ID from reshaping filesystem
// paths when it's joined into SPINE_WORKSPACE_REPOS_DIR or used as a
// route parameter.
var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,53}$`)

// ValidateID rejects workspace IDs that are empty, too long, or that
// contain characters outside the conservative allowlist. Every entry
// point that accepts a caller-supplied workspace ID — the operator
// API, the CLI, the registry provider, and the database/repo
// provisioners — must call this before any path join, SQL statement,
// or persistence write so a traversal-shaped ID can't escape the
// intended namespace.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("workspace_id is empty")
	}
	if len(id) > maxIDLength {
		return fmt.Errorf("workspace_id %q exceeds %d-byte limit", id, maxIDLength)
	}
	if !validIDPattern.MatchString(id) {
		return fmt.Errorf("workspace_id %q must match %s", id, validIDPattern.String())
	}
	return nil
}
