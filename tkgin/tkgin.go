// Package tkgin connects tenantkit to Gin.
package tkgin

import (
	"github.com/Widhipradja/tenantkit"

	"github.com/gin-gonic/gin"
)

// Set stores the tenant on the request context. Call it from the service's own auth middleware,
// right after the user is resolved, so db.WithContext(c.Request.Context()) is tenant-scoped.
func Set(c *gin.Context, tenantID string) {
	c.Request = c.Request.WithContext(tenantkit.WithTenant(c.Request.Context(), tenantID))
}

// TenantID returns the tenant stored by Set.
func TenantID(c *gin.Context) (string, bool) {
	return tenantkit.FromContext(c.Request.Context())
}
