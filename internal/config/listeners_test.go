package config

import "testing"

func TestListenerConfigurationFailsClosed(t *testing.T) {
	cfg := Default()
	if err := cfg.ValidateServing(); err == nil {
		t.Fatal("anonymous administration enabled")
	}
	cfg.AuthPassword = "admin-secret"
	if err := cfg.ValidateServing(); err != nil {
		t.Fatal(err)
	}
	cfg.PublicListen = "0.0.0.0:8788"
	if err := cfg.ValidateServing(); err == nil {
		t.Fatal("public listener accepted legacy admin as a user")
	}
	cfg.Users = []User{{Name: "admin", Role: "admin", Password: "a"}, {Name: "alice", Role: "user", Password: "b"}}
	if err := cfg.ValidateServing(); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"0.0.0.0/0", "::/0", "192.168.0.0/8", "203.0.113.0/24", "not-a-cidr"} {
		copy := *cfg
		copy.Admin.AllowedNetworks = []string{network}
		if err := copy.ValidateServing(); err == nil {
			t.Errorf("public/invalid admin network accepted: %s", network)
		}
	}
	for _, address := range []string{cfg.Listen, "0.0.0.0:0", "0.0.0.0:65536", "bad-address"} {
		copy := *cfg
		copy.PublicListen = address
		if err := copy.ValidateServing(); err == nil {
			t.Errorf("invalid public listener accepted: %s", address)
		}
	}
}
