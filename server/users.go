package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Roles. Only these two exist; anything else is rejected.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

type UserRow struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    int64
}

// EnsureBootstrapAdmin creates the very first admin account from
// --admin-user/--admin-pass if the admin_users table is still empty
// (fresh install, or a database from before this feature existed).
// After that first run, those flags are only a fallback — all real
// user management happens through the admin panel's "Usuários" page.
func (s *Store) EnsureBootstrapAdmin(username, plaintextPass string) (created bool, err error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil // already has at least one user; don't touch anything
	}
	if err := s.CreateUser(username, plaintextPass, RoleAdmin); err != nil {
		return false, err
	}
	return true, nil
}

// CreateUser adds a new admin-panel account. role must be "admin" or
// "user"; a "user" account can log in and see the mesh (read-only)
// but none of the admin-only actions (auth keys, device removal,
// backups, user management) — those routes are gated server-side by
// requireAdmin, not just hidden in the UI.
func (s *Store) CreateUser(username, plaintextPass, role string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("usuário não pode ser vazio")
	}
	if len(plaintextPass) < 8 {
		return fmt.Errorf("senha precisa ter pelo menos 8 caracteres")
	}
	if role != RoleAdmin && role != RoleUser {
		return fmt.Errorf("papel inválido: %s", role)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintextPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO admin_users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, string(hash), role, time.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("já existe um usuário com esse nome")
	}
	return err
}

// UserByUsername looks up an account for login. Returns (nil, nil) —
// not an error — when the username just doesn't exist, so callers
// can't distinguish "wrong user" from "wrong password" by error type
// alone (both should produce the same generic login failure message).
func (s *Store) UserByUsername(username string) (*UserRow, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, created_at FROM admin_users WHERE username = ?`, username)
	var u UserRow
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByID(id int64) (*UserRow, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, created_at FROM admin_users WHERE id = ?`, id)
	var u UserRow
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsers() ([]UserRow, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, role, created_at FROM admin_users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// countAdmins is used to block the one action that could permanently
// lock everyone out of the panel: deleting or demoting the last
// remaining admin account.
func (s *Store) countAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_users WHERE role = ?`, RoleAdmin).Scan(&n)
	return n, err
}

// DeleteUser removes an account. Refuses to delete the last admin.
func (s *Store) DeleteUser(id int64) error {
	u, err := s.UserByID(id)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("usuário não encontrado")
	}
	if u.Role == RoleAdmin {
		n, err := s.countAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return fmt.Errorf("não é possível remover o último administrador")
		}
	}
	_, err = s.db.Exec(`DELETE FROM admin_users WHERE id = ?`, id)
	return err
}

// SetUserPassword changes a user's own (or, for an admin resetting
// someone else's, any) password.
func (s *Store) SetUserPassword(id int64, newPlaintextPass string) error {
	if len(newPlaintextPass) < 8 {
		return fmt.Errorf("senha precisa ter pelo menos 8 caracteres")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPlaintextPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE admin_users SET password_hash = ? WHERE id = ?`, string(hash), id)
	return err
}

// SetUserRole changes a user's role. Refuses to demote the last admin.
func (s *Store) SetUserRole(id int64, role string) error {
	if role != RoleAdmin && role != RoleUser {
		return fmt.Errorf("papel inválido: %s", role)
	}
	u, err := s.UserByID(id)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("usuário não encontrado")
	}
	if u.Role == RoleAdmin && role != RoleAdmin {
		n, err := s.countAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return fmt.Errorf("não é possível rebaixar o último administrador")
		}
	}
	_, err = s.db.Exec(`UPDATE admin_users SET role = ? WHERE id = ?`, role, id)
	return err
}

// CheckPassword verifies a plaintext password against the user's
// bcrypt hash in constant time (bcrypt's comparison is already
// timing-safe internally).
func (u *UserRow) CheckPassword(plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plaintext)) == nil
}
