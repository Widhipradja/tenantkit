package tenantkit_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Widhipradja/tenantkit"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Invoice struct {
	ID       uint `gorm:"primaryKey"`
	TenantID string
	Name     string
	Lines    []Line `gorm:"foreignKey:InvoiceID"`
}
type Line struct {
	ID        uint `gorm:"primaryKey"`
	TenantID  string
	InvoiceID uint
	Label     string
}
type Global struct { // no tenant_id: shared table, must be untouched
	ID   uint `gorm:"primaryKey"`
	Name string
}

const (
	A   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	B   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	Def = tenantkit.DefaultTenantID
)

func open(t *testing.T, mode tenantkit.Mode) *gorm.DB {
	t.Helper()
	host := os.Getenv("TEST_DB_HOST")
	if host == "" {
		t.Skip("TEST_DB_HOST not set")
	}
	dsn := "root:pw@tcp(" + host + ":" + os.Getenv("TEST_DB_PORT") + ")/tk?charset=utf8mb4&parseTime=True"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&Line{}, &Invoice{}, &Global{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Invoice{}, &Line{}, &Global{}); err != nil {
		t.Fatal(err)
	}
	// seed BEFORE registering, so seeding is not subject to enforcement
	seed := []Invoice{{TenantID: A, Name: "a1"}, {TenantID: A, Name: "a2"}, {TenantID: B, Name: "b1"}, {TenantID: Def, Name: "d1"}, {TenantID: "", Name: "legacy-empty"}}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO invoices (name, tenant_id) VALUES ('legacy-null', NULL)")
	db.Create(&Line{TenantID: A, InvoiceID: 1, Label: "a-line"})
	db.Create(&Line{TenantID: B, InvoiceID: 1, Label: "b-line-on-a-invoice"})
	db.Create(&Global{Name: "g"})
	if err := tenantkit.Register(db, mode); err != nil {
		t.Fatal(err)
	}
	return db
}

func names(rows []Invoice) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func TestModeOn(t *testing.T) {
	db := open(t, tenantkit.ModeOn)

	t.Run("scoped query only sees its tenant", func(t *testing.T) {
		var rows []Invoice
		if err := db.Scopes(tenantkit.Scope(A)).Find(&rows).Error; err != nil || len(rows) != 2 {
			t.Fatalf("rows=%v err=%v", names(rows), err)
		}
		rows = nil
		db.Scopes(tenantkit.Scope(B)).Find(&rows)
		if len(rows) != 1 || rows[0].Name != "b1" {
			t.Fatalf("B sees %v", names(rows))
		}
	})
	t.Run("default tenant owns unassigned rows, other tenants never see them", func(t *testing.T) {
		var rows []Invoice
		db.Scopes(tenantkit.Scope(Def)).Find(&rows)
		if len(rows) != 3 { // d1, legacy-empty, legacy-null
			t.Fatalf("default sees %v", names(rows))
		}
		rows = nil
		db.Scopes(tenantkit.Scope("")).Find(&rows) // empty normalises to default
		if len(rows) != 3 {
			t.Fatalf("empty scope sees %v", names(rows))
		}
		rows = nil
		db.Scopes(tenantkit.Scope(A)).Find(&rows)
		for _, r := range rows {
			if r.Name == "legacy-empty" || r.Name == "legacy-null" {
				t.Fatalf("tenant A leaked unassigned row %s", r.Name)
			}
		}
	})
	t.Run("unscoped statement is rejected", func(t *testing.T) {
		var rows []Invoice
		if err := db.Find(&rows).Error; !errors.Is(err, tenantkit.ErrTenantRequired) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("hand-written tenant condition counts as scoped", func(t *testing.T) {
		var rows []Invoice
		if err := db.Where("tenant_id = ?", A).Find(&rows).Error; err != nil || len(rows) != 2 {
			t.Fatalf("rows=%v err=%v", names(rows), err)
		}
		if err := db.Where(&Invoice{TenantID: B}).Find(&rows).Error; err != nil {
			t.Fatalf("struct cond: %v", err)
		}
	})
	t.Run("shared table without tenant_id is untouched", func(t *testing.T) {
		var g []Global
		if err := db.Find(&g).Error; err != nil || len(g) != 1 {
			t.Fatalf("g=%v err=%v", g, err)
		}
	})
	t.Run("context tenant is injected", func(t *testing.T) {
		var rows []Invoice
		ctx := tenantkit.WithTenant(context.Background(), B)
		if err := db.WithContext(ctx).Find(&rows).Error; err != nil || len(rows) != 1 {
			t.Fatalf("rows=%v err=%v", names(rows), err)
		}
	})
	t.Run("create stamps tenant, rejects mismatch and missing scope", func(t *testing.T) {
		inv := Invoice{Name: "new"}
		if err := db.Scopes(tenantkit.Scope(A)).Create(&inv).Error; err != nil || inv.TenantID != A {
			t.Fatalf("stamp: %+v err=%v", inv, err)
		}
		if err := db.Scopes(tenantkit.Scope(A)).Create(&Invoice{Name: "x", TenantID: B}).Error; !errors.Is(err, tenantkit.ErrTenantMismatch) {
			t.Fatalf("mismatch err=%v", err)
		}
		if err := db.Create(&Invoice{Name: "x"}).Error; !errors.Is(err, tenantkit.ErrTenantRequired) {
			t.Fatalf("unscoped create err=%v", err)
		}
		batch := []Invoice{{Name: "b1"}, {Name: "b2"}}
		if err := db.Scopes(tenantkit.Scope(B)).Create(&batch).Error; err != nil || batch[0].TenantID != B || batch[1].TenantID != B {
			t.Fatalf("batch: %+v err=%v", batch, err)
		}
	})
	t.Run("update and delete cannot touch another tenant's rows", func(t *testing.T) {
		var b Invoice
		db.Scopes(tenantkit.Scope(B)).First(&b, "name = ?", "b1")
		res := db.Scopes(tenantkit.Scope(A)).Model(&Invoice{}).Where("id = ?", b.ID).Update("name", "hacked")
		if res.Error != nil || res.RowsAffected != 0 {
			t.Fatalf("cross-tenant update: rows=%d err=%v", res.RowsAffected, res.Error)
		}
		res = db.Scopes(tenantkit.Scope(A)).Delete(&Invoice{}, b.ID)
		if res.Error != nil || res.RowsAffected != 0 {
			t.Fatalf("cross-tenant delete: rows=%d err=%v", res.RowsAffected, res.Error)
		}
		res = db.Scopes(tenantkit.Scope(B)).Model(&b).Update("name", "b1-renamed")
		if res.Error != nil || res.RowsAffected != 1 {
			t.Fatalf("own update: rows=%d err=%v", res.RowsAffected, res.Error)
		}
	})
	t.Run("blanket update is still refused by gorm", func(t *testing.T) {
		err := db.Scopes(tenantkit.Scope(A)).Model(&Invoice{}).Update("name", "all").Error
		if !errors.Is(err, gorm.ErrMissingWhereClause) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("preload of a tenant table is scoped", func(t *testing.T) {
		var inv Invoice
		if err := db.Scopes(tenantkit.Scope(A)).Preload("Lines").First(&inv, 1).Error; err != nil {
			t.Fatal(err)
		}
		if len(inv.Lines) != 1 || inv.Lines[0].Label != "a-line" {
			t.Fatalf("lines=%+v", inv.Lines)
		}
	})
	t.Run("Unscoped is the explicit escape hatch", func(t *testing.T) {
		var rows []Invoice
		if err := tenantkit.Unscoped(db).Find(&rows).Error; err != nil || len(rows) < 6 {
			t.Fatalf("rows=%d err=%v", len(rows), err)
		}
	})
}

func TestModeLogChangesNothing(t *testing.T) {
	db := open(t, tenantkit.ModeLog)
	var rows []Invoice
	if err := db.Find(&rows).Error; err != nil || len(rows) != 6 {
		t.Fatalf("unscoped query altered: rows=%d err=%v", len(rows), err)
	}
	rows = nil
	if err := db.Scopes(tenantkit.Scope(A)).Find(&rows).Error; err != nil || len(rows) != 6 {
		t.Fatalf("log mode must not inject: rows=%d err=%v", len(rows), err)
	}
	inv := Invoice{Name: "x"}
	if err := db.Create(&inv).Error; err != nil || inv.TenantID != "" {
		t.Fatalf("log mode must not stamp or reject: %+v err=%v", inv, err)
	}
}

func TestModeOffRegistersNothing(t *testing.T) {
	db := open(t, tenantkit.ModeOff)
	if db.Callback().Query().Get("tenantkit:query") != nil {
		t.Fatal("off mode registered a callback")
	}
	var rows []Invoice
	if err := db.Find(&rows).Error; err != nil || len(rows) != 6 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

func TestAuditAndEnv(t *testing.T) {
	db := open(t, tenantkit.ModeOff)
	missing, err := tenantkit.TablesMissingTenantColumn(db)
	if err != nil || len(missing) != 1 || missing[0] != "globals" {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	for in, want := range map[string]tenantkit.Mode{"": "off", "ON": "on", " log ": "log", "enabled": "off", "tru": "off"} {
		t.Setenv("TENANT_ENFORCEMENT", in)
		if got := tenantkit.ModeFromEnv(); got != want {
			t.Errorf("ModeFromEnv(%q)=%q want %q", in, got, want)
		}
	}
}

func TestViaParentSkipsTheGuard(t *testing.T) {
	db := open(t, tenantkit.ModeOn)
	var lines []Line
	if err := db.Find(&lines).Error; !errors.Is(err, tenantkit.ErrTenantRequired) {
		t.Fatalf("unscoped read should be rejected, err=%v", err)
	}
	if err := tenantkit.ViaParent(db).Where("invoice_id = ?", 1).Find(&lines).Error; err != nil || len(lines) != 2 {
		t.Fatalf("ViaParent read: n=%d err=%v", len(lines), err)
	}
}

// NoteLine is a child table that gets its tenant from its parent invoice.
type NoteLine struct {
	ID        uint   `gorm:"primaryKey"`
	TenantID  string `gorm:"type:varchar(100);index;default:''"`
	InvoiceID uint
}

func TestBackfillFromParent(t *testing.T) {
	db := open(t, tenantkit.ModeOff) // seeds invoices: id 1,2 -> A, 3 -> B, 4 -> default, 5 -> '' (empty)
	if err := db.Migrator().DropTable(&NoteLine{}); err != nil {
		t.Fatal(err)
	}
	// a pre-tenant_id child table: created without the column, then given rows
	db.Exec("CREATE TABLE note_lines (id INT AUTO_INCREMENT PRIMARY KEY, invoice_id INT)")
	db.Exec("INSERT INTO note_lines (invoice_id) VALUES (1), (3), (5)")
	specs := []tenantkit.ChildTable{{Table: "note_lines", Parent: "invoices", On: "p.id = c.invoice_id", Model: &NoteLine{}}}

	for i := 0; i < 2; i++ { // second run must be a no-op
		if err := tenantkit.BackfillFromParent(db, specs); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if !db.Migrator().HasColumn(&NoteLine{}, "tenant_id") || !db.Migrator().HasIndex(&NoteLine{}, "idx_note_lines_tenant_id") {
		t.Fatal("column or index was not added")
	}
	want := map[uint]string{1: A, 3: B, 5: ""} // invoice 5's parent has no tenant: child left empty
	for inv, tenant := range want {
		var got string
		db.Raw("SELECT COALESCE(tenant_id,'') FROM note_lines WHERE invoice_id = ?", inv).Scan(&got)
		if got != tenant {
			t.Errorf("invoice %d line: tenant_id=%q want %q", inv, got, tenant)
		}
	}
	db.Exec("UPDATE note_lines SET tenant_id = 'KEEP' WHERE invoice_id = 1")
	if err := tenantkit.BackfillFromParent(db, specs); err != nil {
		t.Fatal(err)
	}
	var kept string
	db.Raw("SELECT tenant_id FROM note_lines WHERE invoice_id = 1").Scan(&kept)
	if kept != "KEEP" {
		t.Errorf("backfill overwrote an existing tenant_id: %q", kept)
	}
}
