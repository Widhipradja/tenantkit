package tenantkit

import "gorm.io/gorm"

// TablesMissingTenantColumn lists base tables in the current database that have no tenant_id
// column. It is the to-do list for adding tenancy to a service (some, like lookups, may be
// intentionally global).
func TablesMissingTenantColumn(db *gorm.DB) ([]string, error) {
	var tables []string
	err := db.Raw(`
		SELECT t.TABLE_NAME
		FROM INFORMATION_SCHEMA.TABLES t
		WHERE t.TABLE_SCHEMA = DATABASE() AND t.TABLE_TYPE = 'BASE TABLE'
		  AND NOT EXISTS (
		    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS c
		    WHERE c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME AND c.COLUMN_NAME = ?
		  )
		ORDER BY t.TABLE_NAME`, ColumnName).Scan(&tables).Error
	return tables, err
}
