package main

import (
	"database/sql"
	"fmt"
	"log"
)

// migrateLegacyPlaintextSecrets upgrades a database created by an older
// version of govpn-server — one that stored auth keys and device
// tokens in plaintext — to the current hashed-at-rest schema, WITHOUT
// losing any existing keys/devices. It's a no-op on a fresh/already-
// migrated database (checked via PRAGMA table_info, cheap and safe to
// run on every startup).
//
// SQLite's ALTER TABLE can't retrofit a new PRIMARY KEY or drop a
// column portably across the sqlite versions this might run against,
// so this uses the standard "rename old table, create new one, copy
// data across (computing hashes along the way), drop the old table"
// pattern instead.
func migrateLegacyPlaintextSecrets(db *sql.DB) error {
	if hasColumn(db, "auth_keys", "key") {
		log.Printf("migrating legacy auth_keys table (plaintext -> hashed) ...")
		if _, err := db.Exec(`ALTER TABLE auth_keys RENAME TO auth_keys_legacy`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE TABLE auth_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key_hash TEXT UNIQUE NOT NULL,
			key_prefix TEXT NOT NULL,
			note TEXT,
			reusable INTEGER NOT NULL DEFAULT 0,
			used INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER
		)`); err != nil {
			return err
		}
		rows, err := db.Query(`SELECT key, note, reusable, used, created_at FROM auth_keys_legacy`)
		if err != nil {
			return err
		}
		type oldKey struct {
			key            string
			note           sql.NullString
			reusable, used int
			createdAt      int64
		}
		var olds []oldKey
		for rows.Next() {
			var k oldKey
			if err := rows.Scan(&k.key, &k.note, &k.reusable, &k.used, &k.createdAt); err != nil {
				rows.Close()
				return err
			}
			olds = append(olds, k)
		}
		rows.Close()
		for _, k := range olds {
			prefix := k.key
			if len(prefix) > 14 {
				prefix = prefix[:14]
			}
			if _, err := db.Exec(`INSERT INTO auth_keys (key_hash, key_prefix, note, reusable, used, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				hashSecret(k.key), prefix, k.note.String, k.reusable, k.used, k.createdAt); err != nil {
				return err
			}
		}
		if _, err := db.Exec(`DROP TABLE auth_keys_legacy`); err != nil {
			return err
		}
		log.Printf("migrated %d auth key(s) to hashed storage", len(olds))
	}

	if hasColumn(db, "devices", "device_token") {
		log.Printf("migrating legacy devices table (plaintext token -> hashed) ...")
		if _, err := db.Exec(`ALTER TABLE devices RENAME TO devices_legacy`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE TABLE devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pub_key TEXT UNIQUE NOT NULL,
			device_token_hash TEXT UNIQUE NOT NULL,
			hostname TEXT,
			ip TEXT UNIQUE NOT NULL,
			endpoint TEXT,
			last_seen INTEGER
		)`); err != nil {
			return err
		}
		rows, err := db.Query(`SELECT pub_key, device_token, hostname, ip, endpoint, last_seen FROM devices_legacy`)
		if err != nil {
			return err
		}
		type oldDevice struct {
			pubKey, token          string
			hostname, ip, endpoint sql.NullString
			lastSeen               sql.NullInt64
		}
		var olds []oldDevice
		for rows.Next() {
			var d oldDevice
			if err := rows.Scan(&d.pubKey, &d.token, &d.hostname, &d.ip, &d.endpoint, &d.lastSeen); err != nil {
				rows.Close()
				return err
			}
			olds = append(olds, d)
		}
		rows.Close()
		for _, d := range olds {
			if _, err := db.Exec(`INSERT INTO devices (pub_key, device_token_hash, hostname, ip, endpoint, last_seen) VALUES (?, ?, ?, ?, ?, ?)`,
				d.pubKey, hashSecret(d.token), d.hostname.String, d.ip.String, d.endpoint.String, d.lastSeen.Int64); err != nil {
				return err
			}
		}
		if _, err := db.Exec(`DROP TABLE devices_legacy`); err != nil {
			return err
		}
		log.Printf("migrated %d device(s) to hashed token storage — EXISTING DEVICE TOKENS ARE UNCHANGED (same bearer value), only how they're stored server-side changed, so clients keep working without re-registering", len(olds))
	}

	return nil
}

func hasColumn(db *sql.DB, table, col string) bool {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == col {
			return true
		}
	}
	return false
}
