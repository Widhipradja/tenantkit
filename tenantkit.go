// Package tenantkit makes a GORM-backed service tenant-safe without rewriting its repositories.
//
// It is built to be switched on gradually and to change nothing until asked to:
//
//	TENANT_ENFORCEMENT=off  (default) nothing is registered; behaviour is exactly as before.
//	TENANT_ENFORCEMENT=log  statements that touch a tenant table without being tenant-scoped are
//	                        logged (once per call site); nothing is altered or rejected.
//	TENANT_ENFORCEMENT=on   the tenant is injected into every query/update/delete on a tenant
//	                        table and stamped on every create; unscoped statements are rejected.
//
// A "tenant table" is any model with a tenant_id column. The tenant for a statement comes from
// Scope(tenantID) or, for code that passes a context to GORM, from WithTenant(ctx, id). A
// statement that already carries its own tenant_id condition counts as scoped.
//
// Not covered: raw SQL (db.Raw / db.Exec) and the joined side of Joins(); review those by hand.
package tenantkit

import (
	"context"
	"errors"
	"os"
	"strings"
)

// DefaultTenantID is the tenant every pre-multi-tenant record belongs to. It matches auth-api.
const DefaultTenantID = "00000000-0000-0000-0000-000000000001"

const (
	zeroTenantID = "00000000-0000-0000-0000-000000000000"
	// ColumnName is the column that marks a table as tenant-owned.
	ColumnName = "tenant_id"
)

// Mode selects how strictly tenant scoping is applied.
type Mode string

const (
	ModeOff Mode = "off"
	ModeLog Mode = "log"
	ModeOn  Mode = "on"
)

// ErrTenantRequired is returned (in ModeOn) for a statement on a tenant table that has no tenant.
var ErrTenantRequired = errors.New("tenantkit: statement on a tenant table has no tenant scope")

// ErrTenantMismatch is returned (in ModeOn) when a create names a different tenant than the scope.
var ErrTenantMismatch = errors.New("tenantkit: record tenant_id does not match the scoped tenant")

// ModeFromEnv reads TENANT_ENFORCEMENT. Anything unrecognised is treated as off, so a typo can
// never switch enforcement on.
func ModeFromEnv() Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(os.Getenv("TENANT_ENFORCEMENT")))) {
	case ModeLog:
		return ModeLog
	case ModeOn:
		return ModeOn
	default:
		return ModeOff
	}
}

// Normalize returns the tenant a raw value stands for: empty or the zero UUID mean the default.
func Normalize(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" || id == zeroTenantID {
		return DefaultTenantID
	}
	return id
}

// IsDefault reports whether the raw tenant value stands for the default tenant.
func IsDefault(raw string) bool { return strings.EqualFold(Normalize(raw), DefaultTenantID) }

type ctxKey struct{}

// WithTenant returns a context carrying the tenant. Pass it to GORM with db.WithContext(ctx).
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, Normalize(tenantID))
}

// FromContext returns the tenant stored by WithTenant.
func FromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok && id != ""
}
