package config

import "testing"

// RDS_URL is go-config's to read; the auditor's own settings must not undo it (the staging crash:
// the host fell back to go-config's default, postgres.chewedfeed).
func TestBuildKeepsRDSURL(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	t.Setenv("BUGFIXES_LOCAL_ONLY", "true")
	t.Setenv("RDS_URL", "postgres://aud:pw@postgres-ll27.railway.internal:6543/railway")
	t.Setenv("AUDIT_SECRET", "s3cret")
	c, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	d := c.Database.Details
	if d.Host != "postgres-ll27.railway.internal" || d.Port != 6543 || d.User != "aud" || d.DBName != "railway" {
		t.Fatalf("database: %s:%d user %s db %s", d.Host, d.Port, d.User, d.DBName)
	}
	if c.Auditor.Secret != "s3cret" {
		t.Fatalf("secret: %q", c.Auditor.Secret)
	}
}
