package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserConfigurationValidation(t *testing.T) {
	for name, users := range map[string][]User{
		"no admin":        {{Name: "alice", Password: "secret", Role: "user"}},
		"duplicate":       {{Name: "admin", Password: "secret", Role: "admin"}, {Name: "ADMIN", Password: "secret", Role: "user"}},
		"bad role":        {{Name: "admin", Password: "secret", Role: "root"}},
		"empty password":  {{Name: "admin", Role: "admin"}},
		"disabled admin":  {{Name: "admin", Password: "secret", Role: "admin", Disabled: true}},
		"bad hash":        {{Name: "admin", PasswordHash: "not-bcrypt", Role: "admin"}},
		"unknown media":   {{Name: "admin", Password: "secret", Role: "admin"}, {Name: "alice", Password: "secret", AllowedMedia: []string{"audio"}}},
		"duplicate media": {{Name: "admin", Password: "secret", Role: "admin"}, {Name: "alice", Password: "secret", AllowedMedia: []string{"music", "music"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Users = users
			if cfg.validate() == nil {
				t.Fatal("invalid accounts accepted")
			}
		})
	}
	cfg := Default()
	cfg.Users = []User{{Name: "admin", Role: "admin", Password: "secret"}, {Name: "alice", Password: "secret", AllowedMedia: []string{"anime"}}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Users[1].Role != "user" {
		t.Fatal("unspecified role must default to user")
	}
}

func TestLoadUserLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`users:
  - name: admin
    role: admin
    password: secret
  - name: alice
    password: secret
    language: " SV "
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Users[1].Language; got != "sv" {
		t.Fatalf("user language = %q, want sv", got)
	}
}
