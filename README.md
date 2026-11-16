# tenantkit

Tenant isolation for GORM services, switchable per service with `TENANT_ENFORCEMENT=off|log|on`
(default `off`: nothing is registered, behaviour is unchanged).

```go
// once, after gorm.Open
if err := tenantkit.Register(db, tenantkit.ModeFromEnv()); err != nil { ... }

// per query, from the tenant resolved in your auth middleware
db.Scopes(tenantkit.Scope(tenantID)).Find(&rows)

// or per request, for code that passes a context to GORM
tkgin.Set(c, tenantID)                       // in the auth middleware
db.WithContext(c.Request.Context()).Find(&rows)

// deliberate cross-tenant work (migrations, cron, platform admin)
tenantkit.Unscoped(db).Find(&rows)
```

Rollout per service: `off` -> `log` (read the `[tenantkit]` lines: each is a call site missing a
scope) -> fix those -> `on`. `tenantkit.TablesMissingTenantColumn(db)` lists tables still needing
a `tenant_id` column. Raw SQL (`Raw`/`Exec`) and the joined side of `Joins()` are not scoped.

Tests need a MySQL: `TEST_DB_HOST=127.0.0.1 TEST_DB_PORT=<port> go test ./...` (user root/pw, db `tk`).
