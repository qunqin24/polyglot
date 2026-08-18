package config

import "testing"

func TestDefaultListenIsLoopback(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PORT", "")
	t.Setenv("POLYGLOT_PORT", "")
	t.Setenv("LISTEN", "")
	t.Setenv("POLYGLOT_LISTEN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:3000" {
		t.Errorf("listen = %q, want loopback default", cfg.Listen)
	}
}

func TestListenOverridesRemainExplicit(t *testing.T) {
	t.Run("port", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("PORT", "4321")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Listen != ":4321" {
			t.Errorf("listen = %q", cfg.Listen)
		}
	})
	t.Run("listen", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("PORT", "4321")
		t.Setenv("LISTEN", "192.0.2.10:9876")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Listen != "192.0.2.10:9876" {
			t.Errorf("listen = %q", cfg.Listen)
		}
	})
}

func TestConfiguredSetupTokenIsLoadedWithoutBeingTransformed(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("POLYGLOT_SETUP_TOKEN", "operator-setup-token")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SetupToken != "operator-setup-token" {
		t.Errorf("setup token was changed: %q", cfg.SetupToken)
	}
}

func TestUpdateCheckDefaultsAndRepositoryValidation(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.UpdateCheckEnabled || cfg.UpdateRepository != "qunqin24/polyglot" {
			t.Errorf("update config = enabled %v, repository %q", cfg.UpdateCheckEnabled, cfg.UpdateRepository)
		}
	})
	t.Run("disabled and overridden", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("UPDATE_CHECK_ENABLED", "false")
		t.Setenv("UPDATE_REPOSITORY", "owner/fork")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.UpdateCheckEnabled || cfg.UpdateRepository != "owner/fork" {
			t.Errorf("update config = enabled %v, repository %q", cfg.UpdateCheckEnabled, cfg.UpdateRepository)
		}
	})
	t.Run("invalid repository", func(t *testing.T) {
		t.Setenv("DATA_DIR", t.TempDir())
		t.Setenv("UPDATE_REPOSITORY", "https://example.com/repo")
		if _, err := Load(); err == nil {
			t.Fatal("invalid update repository was accepted")
		}
	})
}
