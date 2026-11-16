package tenantkit

import (
	"fmt"

	"gorm.io/gorm"
)

// ChildTable describes a table that is tenant-owned only through a parent row (a join table, a
// line-item table, ...) and so needs its own tenant_id column filled from that parent.
type ChildTable struct {
	Table  string // child table name, e.g. "sys_user_roles"
	Parent string // parent table name, e.g. "sys_users"
	On     string // join condition using the aliases c (child) and p (parent), e.g. "p.id = c.user_id"
	// Model is the child's GORM model (a pointer). It must declare
	//   TenantID string `gorm:"type:varchar(100);index;default:''"`
	// so the column and its idx_<table>_tenant_id index can be created.
	Model any
}

// BackfillFromParent makes each child table tenant-aware without changing what anyone can see:
//
//  1. adds the tenant_id column and its index if they are missing. AutoMigrate is not relied on
//     for this: it has been seen to skip adding the column to an existing table (no error, no
//     ALTER), so it is done explicitly;
//  2. fills empty (NULL or ”) tenant_id values from the parent row.
//
// It is idempotent, never overwrites a tenant_id a child already has, and leaves a child alone
// when its parent has no tenant. Run it at startup after AutoMigrate. MySQL only.
func BackfillFromParent(db *gorm.DB, tables []ChildTable) error {
	m := db.Migrator()
	for _, t := range tables {
		if !m.HasColumn(t.Model, ColumnName) {
			if err := m.AddColumn(t.Model, ColumnName); err != nil {
				return fmt.Errorf("tenantkit: add %s.%s: %w", t.Table, ColumnName, err)
			}
		}
		if idx := "idx_" + t.Table + "_" + ColumnName; !m.HasIndex(t.Model, idx) {
			if err := m.CreateIndex(t.Model, idx); err != nil {
				return fmt.Errorf("tenantkit: index %s.%s: %w", t.Table, ColumnName, err)
			}
		}
		sql := fmt.Sprintf(
			"UPDATE %s c JOIN %s p ON %s SET c.%[4]s = p.%[4]s "+
				"WHERE (c.%[4]s IS NULL OR c.%[4]s = '') AND p.%[4]s IS NOT NULL AND p.%[4]s <> ''",
			t.Table, t.Parent, t.On, ColumnName)
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("tenantkit: backfill %s.%s from %s: %w", t.Table, ColumnName, t.Parent, err)
		}
	}
	return nil
}
