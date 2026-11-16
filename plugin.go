package tenantkit

import (
	"fmt"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	settingTenant = "tenantkit:tenant"
	settingSkip   = "tenantkit:skip"
)

// Scope scopes a statement to one tenant. In ModeOn the tenant is injected into the SQL; in
// ModeLog it only records the intent. Use it as db.Scopes(tenantkit.Scope(id)) or, in a chain,
// db.Scopes(...). It is safe to apply to models without a tenant_id column (they are ignored).
func Scope(tenantID string) func(*gorm.DB) *gorm.DB {
	id := Normalize(tenantID)
	return func(db *gorm.DB) *gorm.DB { return db.Set(settingTenant, id) }
}

// Unscoped marks a statement as intentionally cross-tenant (migrations, cron jobs, platform
// admin). Every use is a place a reviewer should look at.
func Unscoped(db *gorm.DB) *gorm.DB { return db.Set(settingSkip, true) }

type plugin struct {
	mode Mode
	seen sync.Map // call sites already logged
}

// Register installs the tenant callbacks on db. In ModeOff it installs nothing at all.
func Register(db *gorm.DB, mode Mode) error {
	if mode != ModeLog && mode != ModeOn {
		return nil
	}
	p := &plugin{mode: mode}
	cb := db.Callback()
	if err := cb.Create().Before("gorm:create").Register("tenantkit:create", p.create); err != nil {
		return err
	}
	if err := cb.Query().Before("gorm:query").Register("tenantkit:query", p.read); err != nil {
		return err
	}
	if err := cb.Update().Before("gorm:update").Register("tenantkit:update", p.write); err != nil {
		return err
	}
	return cb.Delete().Before("gorm:delete").Register("tenantkit:delete", p.write)
}

// tenantField returns the model's tenant column, or nil if the model is not tenant-owned.
func tenantField(db *gorm.DB) *schema.Field {
	if db.Statement == nil || db.Statement.Schema == nil {
		return nil
	}
	return db.Statement.Schema.LookUpField(ColumnName)
}

func skipped(db *gorm.DB) bool {
	v, ok := db.Get(settingSkip)
	b, _ := v.(bool)
	return ok && b
}

// scopeTenant is the tenant the statement is scoped to, from Scope() or the context.
func scopeTenant(db *gorm.DB) (string, bool) {
	if v, ok := db.Get(settingTenant); ok {
		if id, _ := v.(string); id != "" {
			return id, true
		}
	}
	return FromContext(db.Statement.Context)
}

func (p *plugin) read(db *gorm.DB) {
	if db.Error != nil || skipped(db) || tenantField(db) == nil {
		return
	}
	p.scopeStatement(db, "query", true)
}

// write handles update and delete. A statement with no condition at all is left alone so GORM
// still rejects it (ErrMissingWhereClause) instead of us turning it into "whole tenant".
func (p *plugin) write(db *gorm.DB) {
	if db.Error != nil || skipped(db) || tenantField(db) == nil {
		return
	}
	_, hasWhere := db.Statement.Clauses["WHERE"]
	if !hasWhere && !db.AllowGlobalUpdate && !hasPrimaryKeyValue(db) {
		return
	}
	p.scopeStatement(db, "write", true)
}

func (p *plugin) scopeStatement(db *gorm.DB, op string, injectable bool) {
	if tenant, ok := scopeTenant(db); ok {
		if p.mode == ModeOn && injectable {
			db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{tenantCondition(tenant)}})
		}
		return
	}
	if hasTenantCondition(db.Statement.Clauses) {
		return
	}
	p.violation(db, op, ErrTenantRequired)
}

func (p *plugin) create(db *gorm.DB) {
	field := tenantField(db)
	if db.Error != nil || skipped(db) || field == nil {
		return
	}
	tenant, scoped := scopeTenant(db)
	rv := db.Statement.ReflectValue
	apply := func(elem reflect.Value) {
		raw, isZero := field.ValueOf(db.Statement.Context, elem)
		switch {
		case isZero && scoped:
			if p.mode == ModeOn {
				db.AddError(field.Set(db.Statement.Context, elem, tenant))
			}
		case isZero:
			p.violation(db, "create", ErrTenantRequired)
		case scoped && !strings.EqualFold(Normalize(fmt.Sprint(raw)), tenant):
			p.violation(db, "create", ErrTenantMismatch)
		}
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			apply(reflect.Indirect(rv.Index(i)))
		}
	case reflect.Struct:
		apply(rv)
	}
}

// violation logs (once per call site) and, in ModeOn, fails the statement.
func (p *plugin) violation(db *gorm.DB, op string, err error) {
	site := callSite()
	model := ""
	if db.Statement.Schema != nil {
		model = db.Statement.Schema.Name
	}
	key := op + "|" + model + "|" + site + "|" + err.Error()
	if _, dup := p.seen.LoadOrStore(key, struct{}{}); !dup {
		log.Printf("[tenantkit] mode=%s %s on %s: %v (at %s)", p.mode, op, model, err, site)
	}
	if p.mode == ModeOn {
		db.AddError(err)
	}
}

func callSite() string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		// skip GORM's own frames and this package's (the trailing dot keeps tenantkit_test, and
		// any service code, from matching)
		if !strings.Contains(f.File, "gorm.io/") && !strings.HasPrefix(f.Function, "github.com/Widhipradja/tenantkit.") {
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return "unknown"
		}
	}
}

// tenantCondition matches the tenant's rows. The default tenant also owns rows that never got a
// tenant_id (NULL/empty), so a single-tenant database keeps working before it is backfilled; every
// other tenant matches strictly, so unassigned rows can never leak into it.
func tenantCondition(tenant string) clause.Expression {
	col := clause.Column{Table: clause.CurrentTable, Name: ColumnName}
	if IsDefault(tenant) {
		return clause.Expr{SQL: "(? = ? OR ? = '' OR ? IS NULL)", Vars: []interface{}{col, DefaultTenantID, col, col}}
	}
	return clause.Eq{Column: col, Value: tenant}
}

func hasPrimaryKeyValue(db *gorm.DB) bool {
	s := db.Statement
	if s.Schema == nil || s.Schema.PrioritizedPrimaryField == nil || !s.ReflectValue.IsValid() {
		return false
	}
	rv := s.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return rv.Len() > 0
	case reflect.Struct:
		_, zero := s.Schema.PrioritizedPrimaryField.ValueOf(s.Context, rv)
		return !zero
	}
	return false
}

// hasTenantCondition reports whether the statement already filters on tenant_id by hand.
func hasTenantCondition(clauses map[string]clause.Clause) bool {
	c, ok := clauses["WHERE"]
	if !ok {
		return false
	}
	where, ok := c.Expression.(clause.Where)
	if !ok {
		return false
	}
	return exprsMentionTenant(where.Exprs)
}

func exprsMentionTenant(exprs []clause.Expression) bool {
	for _, e := range exprs {
		switch v := e.(type) {
		case clause.Eq:
			if columnIsTenant(v.Column) {
				return true
			}
		case clause.IN:
			if columnIsTenant(v.Column) {
				return true
			}
		case clause.Expr:
			if strings.Contains(strings.ToLower(v.SQL), ColumnName) {
				return true
			}
		case clause.AndConditions:
			if exprsMentionTenant(v.Exprs) {
				return true
			}
		case clause.OrConditions:
			if exprsMentionTenant(v.Exprs) {
				return true
			}
		case clause.NamedExpr:
			if strings.Contains(strings.ToLower(v.SQL), ColumnName) {
				return true
			}
		}
	}
	return false
}

func columnIsTenant(col interface{}) bool {
	switch c := col.(type) {
	case clause.Column:
		return strings.EqualFold(c.Name, ColumnName)
	case string:
		return strings.EqualFold(c, ColumnName)
	}
	return false
}
