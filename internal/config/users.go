package config

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func (c *Config) validateUsers() error {
	seen, admin := map[string]bool{}, false
	namePattern := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	for i := range c.Users {
		u := &c.Users[i]
		u.Name = strings.TrimSpace(u.Name)
		if u.Role == "" {
			u.Role = "user"
		}
		if !namePattern.MatchString(u.Name) || seen[strings.ToLower(u.Name)] {
			return fmt.Errorf("users: names must be unique (ignoring case) and contain 1–64 letters, digits, dots, underscores or hyphens")
		}
		seen[strings.ToLower(u.Name)] = true
		if u.Role != "admin" && u.Role != "user" {
			return fmt.Errorf("users: %s role must be admin or user", u.Name)
		}
		if (u.Password == "") == (u.PasswordHash == "") {
			return fmt.Errorf("users: %s needs exactly one of password and password_hash", u.Name)
		}
		if u.PasswordHash != "" {
			if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
				return fmt.Errorf("users: %s has an invalid bcrypt password_hash", u.Name)
			}
		}
		if u.Role == "admin" && !u.Disabled {
			admin = true
		}
	}
	if len(c.Users) > 0 && !admin {
		return fmt.Errorf("users: configure at least one enabled admin account")
	}
	return nil
}
