// Package reporequest defines the domain model + storage contract for
// the repo-creation approval queue served at /api/repo*. It is the
// artemis-side counterpart to the universe-cli `repo` command group:
// staff submit a Request (status pending); an admin approves, at which
// point the handler mints an Apollo-11 App token and creates the repo
// synchronously (status active) or records the failure (status failed).
//
// The Request struct is the shape that crosses the package boundary
// between the store backend and the handler; backends marshal it to
// their wire encoding (Valkey hash fields), handlers marshal it to the
// camelCase JSON contract documented in the universe-cli dossier (§I).
package reporequest

import (
	"errors"
	"regexp"
	"time"
)

// Status is the lifecycle position of a repo request.
//
//	pending ──approve──▶ approved ──gh create ok──▶ active
//	   │                     └──gh create fails──▶ failed
//	   └──reject──▶ rejected
//
// active, rejected, and failed are terminal.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusActive   Status = "active"
	StatusRejected Status = "rejected"
	StatusFailed   Status = "failed"
)

// Valid reports whether s is one of the five known states.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusApproved, StatusActive, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether s admits no further transition.
func (s Status) Terminal() bool {
	switch s {
	case StatusActive, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// CanResolve reports whether an approve/reject transition is legal from
// s. Only pending requests may be resolved; the store enforces this as a
// compare-and-set guard so concurrent admins cannot double-resolve.
func (s Status) CanResolve() bool {
	return s == StatusPending
}

func (s Status) HoldsName() bool {
	switch s {
	case StatusPending, StatusApproved, StatusActive:
		return true
	default:
		return false
	}
}

// Visibility is the GitHub repo visibility.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Valid reports whether v is public or private.
func (v Visibility) Valid() bool {
	return v == VisibilityPublic || v == VisibilityPrivate
}

// NameRE constrains repo names: start with a letter or digit, then
// letters, digits, '.', '_', '-'; max 100 chars. Carried verbatim from
// the Windmill flow (f/repo_mgmt/types.ts) and kept byte-identical to
// the universe-cli REPO_NAME_RE so client preflight and server
// validation never disagree (dossier §V5).
var NameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// ValidName reports whether name satisfies NameRE.
func ValidName(name string) bool {
	return NameRE.MatchString(name)
}

// Request is the in-memory representation of one repo-creation request.
// Timestamps are UTC; the store stamps CreatedAt/UpdatedAt.
type Request struct {
	ID           string
	Name         string
	Owner        string
	Visibility   Visibility
	Description  string
	Template     string // empty ⇒ blank repo (no template generate)
	Status       Status
	URL          string // set when Status == active
	Error        string // set when Status == failed
	RequestedBy  string // GitHub login of the submitter
	Approver     string // GitHub login that resolved it
	RejectReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Sentinel errors returned by the store. Callers compare with errors.Is;
// the handler layer maps them to HTTP status codes.
var (
	// ErrNotFound — no request with the given id. → 404.
	ErrNotFound = errors.New("reporequest: not found")

	// ErrAlreadyExists — a pending/active request for the same repo
	// name already exists (dedupe guard). → 409.
	ErrAlreadyExists = errors.New("reporequest: request already exists")

	// ErrNotPending — an approve/reject targeted a request that is no
	// longer pending (resolved by another admin). → 409 already_resolved.
	ErrNotPending = errors.New("reporequest: request is not pending")

	ErrNotActive = errors.New("reporequest: request is not active")
)
