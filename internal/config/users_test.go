package config

import "testing"

func TestUserConfigurationValidation(t *testing.T) {
	for name, users := range map[string][]User{
		"no admin":       {{Name: "alice", Password: "secret", Role: "user"}},
		"duplicate":      {{Name: "admin", Password: "secret", Role: "admin"}, {Name: "ADMIN", Password: "secret", Role: "user"}},
		"bad role":       {{Name: "admin", Password: "secret", Role: "root"}},
		"empty password": {{Name: "admin", Role: "admin"}},
		"disabled admin": {{Name: "admin", Password: "secret", Role: "admin", Disabled: true}},
		"bad hash":       {{Name: "admin", PasswordHash: "not-bcrypt", Role: "admin"}},
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
	cfg.Users = []User{{Name: "admin", Role: "admin", Password: "secret"}, {Name: "alice", Password: "secret"}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Users[1].Role != "user" {
		t.Fatal("unspecified role must default to user")
	}
}
