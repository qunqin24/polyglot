package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/version"
)

// The command line is deliberately tiny: running the binary with no arguments
// starts the server, and everything else exists to get an operator out of a
// hole. There is no flag library and no config file — see README.

const usageText = `Polyglot — an LLM API protocol conversion gateway.

Usage:
  polyglot                  Start the gateway (the only thing you normally do)
  polyglot reset-password   Recover a locked-out administrator
  polyglot config           Print the effective configuration and where each value came from
  polyglot version          Print the version

Configuration is environment variables only — there is no config file. The
table below is what exists; ` + "`polyglot config`" + ` shows what this install is
actually using and where each value came from.

Everything else — providers, models, aliases, API keys — lives in the SQLite
database under DATA_DIR and is managed from the WebUI.
`

func versionString() string {
	return fmt.Sprintf("%s (%s)", version.Version, version.Commit)
}

// runCLI handles a subcommand and reports whether it consumed the invocation.
// Unknown arguments are an error rather than being ignored: silently starting
// the server after `polyglot rest-password` would be worse than failing.
func runCLI(args []string) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch strings.TrimLeft(args[0], "-") {
	case "help", "h":
		printHelp()
		return true, nil
	case "version", "v":
		fmt.Printf("polyglot %s\n", versionString())
		return true, nil
	case "config":
		return true, printConfig()
	case "reset-password", "resetpassword":
		return true, resetPassword()
	default:
		return true, fmt.Errorf("unknown command %q\n\n%s", args[0], usageText)
	}
}

// resetPassword is the way back in when the administrator password is lost.
// There is no email, no security question and no second account to fall back
// on, so the recovery credential is filesystem access: whoever can run this
// binary against DATA_DIR already owns the machine and could read the database
// anyway. That is the same trust boundary every other self-hosted single-admin
// tool uses.
func resetPassword() error {
	username, password, err := resetAdminPassword()
	if err != nil {
		return err
	}
	fmt.Printf(`
Administrator password reset.

  Username   %s
  Password   %s

This password is shown once. Sign in and change it from Settings.
All existing sessions were signed out.
`, username, password)
	return nil
}

// resetAdminPassword does the work and returns what to show, so the recovery
// path can be tested without capturing stdout.
func resetAdminPassword() (username, password string, err error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}

	ctx := context.Background()
	// The gateway can stay running: SQLite is in WAL mode, so a second process
	// may write while it serves traffic.
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return "", "", fmt.Errorf("open %s: %w", cfg.DBPath, err)
	}
	defer st.Close()

	n, err := st.AdminCount(ctx)
	if err != nil {
		return "", "", err
	}
	if n == 0 {
		return "", "", fmt.Errorf("no administrator exists yet in %s — open the WebUI and create one", cfg.DBPath)
	}

	admin, err := st.OnlyAdmin(ctx)
	if err != nil {
		return "", "", err
	}

	// Generated rather than prompted: it avoids a TTY dependency inside
	// `docker exec`, keeps the password out of shell history and out of the
	// process list, and is stronger than what someone locked out at midnight
	// would invent. It is shown once, exactly like an API key.
	password = idgen.Secret()[:20]
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", "", fmt.Errorf("hash password: %w", err)
	}
	if err := st.UpdateAdminPassword(ctx, admin.ID, hash); err != nil {
		return "", "", err
	}
	// Anyone still holding a session cookie loses it. A password reset that
	// left old sessions alive would not actually be a reset.
	if err := st.DeleteSessionsForAdmin(ctx, admin.ID); err != nil {
		return "", "", fmt.Errorf("clear existing sessions: %w", err)
	}
	return admin.Username, password, nil
}
