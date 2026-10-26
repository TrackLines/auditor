// Package config is the auditor's settings, read with go-config like the backend's, so the variable
// names are the same: RDS_URL (or RDS_HOSTNAME, RDS_PORT, RDS_DB, RDS_USERNAME, RDS_PASSWORD) for the
// auditor's own database, BUGFIXES_AGENT_KEY/SECRET, BUGFIXES_LOCAL_ONLY, HTTP_PORT (or Railway's PORT).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	bugfixes "github.com/bugfixes/go-bugfixes"
	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/caarlos0/env/v8"
	"github.com/joho/godotenv"
	configbugfixes "github.com/keloran/go-config/v2/bugfixes"
	"github.com/keloran/go-config/v2/database/postgres"
	"github.com/keloran/go-config/v2/local"
	"github.com/keloran/go-config/v2/onepassword"
)

// Service is the auditor's name in Bugfixes reports.
const Service = "auditor"

type Config struct {
	Local    local.System
	Database postgres.System
	Bugfixes configbugfixes.System
	Auditor  Auditor
}

// Auditor is the auditor's own settings. It's parsed on its own: parsing the whole Config would
// re-read go-config's parts from the environment and undo what their Build did (RDS_URL's host).
type Auditor struct {
	// Secret is the token the backend sends to /internal/ routes (1Password audit_auditor; the
	// backend's copy is audit_backend). Without it those routes refuse everything.
	Secret string `env:"AUDIT_SECRET"`
}

// secrets come from 1Password's tracklines vault, an item per service; nothing without
// OP_SERVICE_ACCOUNT_TOKEN, and a variable that's already set wins. Only what the code reads.
var secrets = map[string]string{
	"bugfixes_auditor.username":   "BUGFIXES_AGENT_KEY",
	"bugfixes_auditor.credential": "BUGFIXES_AGENT_SECRET",
	"audit_auditor.credential":    "AUDIT_SECRET",
}

// Build reads .env (if there is one; the environment wins), 1Password, then each part's settings, and
// sends errors to Bugfixes with them.
func Build() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}
	if err := onepassword.Load("tracklines", secrets); err != nil {
		return nil, fmt.Errorf("1password: %w", err)
	}
	l, err := local.Build()
	if err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	db := postgres.NewSystem()
	if _, err := db.Build(); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	bf := configbugfixes.NewSystem()
	if _, err := bf.Build(); err != nil {
		return nil, fmt.Errorf("bugfixes: %w", err)
	}
	c := &Config{Local: *l, Database: *db, Bugfixes: *bf}
	if err := env.Parse(&c.Auditor); err != nil {
		return nil, fmt.Errorf("auditor settings: %w", err)
	}

	bugfixesConfig := bugfixes.Config{
		Server:      c.Bugfixes.Server,
		AgentKey:    c.Bugfixes.AgentKey,
		AgentSecret: c.Bugfixes.AgentSecret,
		LocalOnly:   c.Local.KeepLocal,
		Service:     Service,
	}
	bugfixes.SetDefaultConfig(bugfixesConfig)
	logger := &logs.BugFixes{}
	logger.Setup(c.Bugfixes.AgentKey, c.Bugfixes.AgentSecret)
	logger.SetConfig(bugfixesConfig)
	c.Bugfixes.Logger = logger
	return c, nil
}

// Port is Railway's PORT when it sets one, else HTTP_PORT.
func (c *Config) Port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return strconv.Itoa(c.Local.HTTPPort)
}

func loadDotEnv() error {
	if _, err := os.Stat(".env"); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat .env: %w", err)
	}
	if err := godotenv.Load(".env"); err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}
