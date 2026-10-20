package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	subnet *net.IPNet
	dbPath string
	dsn    string
}

func OpenStore(path string, subnetCIDR string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite serializes better with a single connection; the
	// coordination server's write volume is tiny (registrations/beacons).
	db.SetMaxOpenConns(1)
	schema := `
	CREATE TABLE IF NOT EXISTS auth_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key_hash TEXT UNIQUE NOT NULL,
		key_prefix TEXT NOT NULL,
		note TEXT,
		reusable INTEGER NOT NULL DEFAULT 0,
		used INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER
	);
	CREATE TABLE IF NOT EXISTS devices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		pub_key TEXT UNIQUE NOT NULL,
		device_token_hash TEXT UNIQUE NOT NULL,
		hostname TEXT,
		ip TEXT UNIQUE NOT NULL,
		endpoint TEXT,
		last_seen INTEGER
	);
	CREATE TABLE IF NOT EXISTS ip_seq (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		next_offset INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := migrateLegacyPlaintextSecrets(db); err != nil {
		return nil, fmt.Errorf("migrate legacy schema: %w", err)
	}
	_, _, err = net.ParseCIDR(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %w", err)
	}
	_, ipnet, _ := net.ParseCIDR(subnetCIDR)

	// seed ip_seq starting at offset 2 (offset 1 reserved for the server/gateway)
	_, err = db.Exec(`INSERT OR IGNORE INTO ip_seq (id, next_offset) VALUES (1, 2)`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, subnet: ipnet, dbPath: path, dsn: dsn}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func randToken(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret returns the hex SHA-256 of a secret (auth key or device
// token). These are both high-entropy (192-bit) random values, not
// human passwords, so a fast hash is fine here — the point isn't to
// slow down guessing (infeasible either way at that entropy), it's to
// make the stored value useless by itself if the database file (or a
// backup of it) ever leaks, same reasoning as hashing API keys/session
// tokens in any other system.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// CreateAuthKey generates a new auth key usable by one (or, if reusable,
// unlimited) client registrations. The plaintext key is returned once
// to the caller (shown in the admin panel / genkey CLI output) and
// never stored — only its hash and a short, non-secret prefix (for
// display/identification) are persisted.
func (s *Store) CreateAuthKey(note string, reusable bool) (string, error) {
	key, err := randToken(24)
	if err != nil {
		return "", err
	}
	key = "govpn-" + key
	prefix := key
	if len(prefix) > 14 {
		prefix = prefix[:14]
	}
	_, err = s.db.Exec(`INSERT INTO auth_keys (key_hash, key_prefix, note, reusable, used, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		hashSecret(key), prefix, note, boolToInt(reusable), time.Now().Unix())
	if err != nil {
		return "", err
	}
	return key, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) checkAndConsumeAuthKey(key string) error {
	hash := hashSecret(key)
	row := s.db.QueryRow(`SELECT reusable, used FROM auth_keys WHERE key_hash = ?`, hash)
	var reusable, used int
	if err := row.Scan(&reusable, &used); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("invalid auth key")
		}
		return err
	}
	if reusable == 0 && used == 1 {
		return fmt.Errorf("auth key already used")
	}
	if reusable == 0 {
		_, err := s.db.Exec(`UPDATE auth_keys SET used = 1 WHERE key_hash = ?`, hash)
		return err
	}
	return nil
}

// nextIP allocates the next free IP in the subnet (sequential, no reuse of
// freed IPs in this MVP).
func (s *Store) nextIP() (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var offset int64
	if err := tx.QueryRow(`SELECT next_offset FROM ip_seq WHERE id = 1`).Scan(&offset); err != nil {
		return "", err
	}
	ip, err := offsetToIP(s.subnet, offset)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE ip_seq SET next_offset = ? WHERE id = 1`, offset+1); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ip, nil
}

func offsetToIP(subnet *net.IPNet, offset int64) (string, error) {
	base := subnet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("only IPv4 subnets supported")
	}
	baseInt := binary.BigEndian.Uint32(base)
	ones, bits := subnet.Mask.Size()
	maxHosts := int64(1) << (bits - ones)
	if offset >= maxHosts {
		return "", fmt.Errorf("address pool exhausted")
	}
	ipInt := baseInt + uint32(offset)
	ipBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(ipBytes, ipInt)
	return net.IP(ipBytes).String(), nil
}

// RegisterDevice registers a device by its WireGuard pubkey. A valid
// auth key is REQUIRED on every call, including when the pubkey is
// already registered (rotating its token) — there is no "pubkey
// already known, skip the auth key check" shortcut, because every
// other peer in the mesh can see any device's pubkey via /api/peers,
// so skipping auth there would let anyone who knows (or guesses) a
// victim's pubkey steal its live bearer token.
func (s *Store) RegisterDevice(authKey, pubKey, hostname string) (token, ip string, err error) {
	if err := s.checkAndConsumeAuthKey(authKey); err != nil {
		return "", "", err
	}

	token, err = randToken(24)
	if err != nil {
		return "", "", err
	}
	tokenHash := hashSecret(token)

	// existing device (same pubkey)? rotate its token, keep its IP.
	row := s.db.QueryRow(`SELECT ip FROM devices WHERE pub_key = ?`, pubKey)
	var existingIP string
	if err := row.Scan(&existingIP); err == nil {
		_, err = s.db.Exec(`UPDATE devices SET device_token_hash = ?, hostname = ? WHERE pub_key = ?`,
			tokenHash, hostname, pubKey)
		if err != nil {
			return "", "", err
		}
		return token, existingIP, nil
	}

	ip, err = s.nextIP()
	if err != nil {
		return "", "", err
	}
	_, err = s.db.Exec(`INSERT INTO devices (pub_key, device_token_hash, hostname, ip, last_seen) VALUES (?, ?, ?, ?, ?)`,
		pubKey, tokenHash, hostname, ip, time.Now().Unix())
	if err != nil {
		return "", "", err
	}
	return token, ip, nil
}

func (s *Store) DeviceByToken(token string) (pubKey, ip string, err error) {
	row := s.db.QueryRow(`SELECT pub_key, ip FROM devices WHERE device_token_hash = ?`, hashSecret(token))
	err = row.Scan(&pubKey, &ip)
	return
}

func (s *Store) UpdateEndpoint(token, endpoint string) error {
	_, err := s.db.Exec(`UPDATE devices SET endpoint = ?, last_seen = ? WHERE device_token_hash = ?`,
		endpoint, time.Now().Unix(), hashSecret(token))
	return err
}

type DeviceRow struct {
	PubKey   string
	IP       string
	Endpoint string
	Hostname string
	LastSeen int64
}

func (s *Store) ListPeers(excludePubKey string) ([]DeviceRow, error) {
	rows, err := s.db.Query(`SELECT pub_key, ip, COALESCE(endpoint,''), COALESCE(hostname,''), last_seen FROM devices WHERE pub_key != ?`, excludePubKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRow
	for rows.Next() {
		var d DeviceRow
		if err := rows.Scan(&d.PubKey, &d.IP, &d.Endpoint, &d.Hostname, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// ListAllDevices returns every registered device, for the admin panel.
func (s *Store) ListAllDevices() ([]DeviceRow, error) {
	rows, err := s.db.Query(`SELECT pub_key, ip, COALESCE(endpoint,''), COALESCE(hostname,''), last_seen FROM devices ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRow
	for rows.Next() {
		var d DeviceRow
		if err := rows.Scan(&d.PubKey, &d.IP, &d.Endpoint, &d.Hostname, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// DeleteDevice removes a device from the mesh. It stops appearing in peer
// lists and its own device token stops working immediately.
func (s *Store) DeleteDevice(pubKey string) error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE pub_key = ?`, pubKey)
	return err
}

type AuthKeyRow struct {
	ID        int64
	KeyPrefix string
	Note      string
	Reusable  bool
	Used      bool
	CreatedAt int64
}

func (s *Store) ListAuthKeys() ([]AuthKeyRow, error) {
	rows, err := s.db.Query(`SELECT id, key_prefix, COALESCE(note,''), reusable, used, created_at FROM auth_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthKeyRow
	for rows.Next() {
		var k AuthKeyRow
		var reusable, used int
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Note, &reusable, &used, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.Reusable = reusable != 0
		k.Used = used != 0
		out = append(out, k)
	}
	return out, nil
}

// RevokeAuthKey deletes an auth key (by its row id — the plaintext key
// itself is never stored, so it can't be looked up by value) so it can
// no longer be used to register new devices. It does not affect
// devices already registered.
func (s *Store) RevokeAuthKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM auth_keys WHERE id = ?`, id)
	return err
}

// --- settings (key/value, used for backup configuration) ---

func (s *Store) GetSetting(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- backup / restore ---

// Backup writes a consistent snapshot of the whole database (schema +
// data) to destPath using SQLite's VACUUM INTO, which is safe to run
// while the server keeps serving requests.
func (s *Store) Backup(destPath string) error {
	// VACUUM INTO refuses to overwrite an existing file, and destPath is
	// always a fresh timestamped filename from the caller, so this is
	// just defense in depth.
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("destination already exists: %s", destPath)
	}
	_, err := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(destPath, "'", "''")))
	return err
}

// DBPath returns the filesystem path of the live database file.
func (s *Store) DBPath() string {
	return s.dbPath
}
