package main

import (
	"time"
)

// ACL rules implement simple per-device "allowlist" permissions: by
// default every device can reach every other device (today's
// behavior, unchanged). The moment an admin gives a device its own
// rules, THAT device becomes restricted and can only see the peers
// it explicitly lists — see ListPeers in store.go for how this
// combines with the other side's own restriction.
//
// Rules are DIRECTED (owner_device -> peer_device): restricting
// device X only ever touches X's own rows. It must never also make
// some other device Y implicitly restricted just because X listed Y
// as an allowed peer — Y's own access stays exactly as it was.

// aclAllowedSet returns the set of pubkeys this device has explicitly
// declared itself allowed to reach, and whether it has declared ANY
// rule at all (restricted). restricted=false means "no restriction
// configured for this device" — the caller should treat that as full
// access, not as an empty allowlist.
func (s *Store) aclAllowedSet(pubKey string) (map[string]bool, bool, error) {
	rows, err := s.db.Query(`SELECT peer_device FROM acl_rules WHERE owner_device = ?`, pubKey)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	set := map[string]bool{}
	found := false
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			return nil, false, err
		}
		found = true
		set[peer] = true
	}
	return set, found, nil
}

// DeviceRestriction describes one device's current ACL state, for the
// admin panel's "Permissões" page.
type DeviceRestriction struct {
	Restricted    bool
	AllowedPubKey []string
}

func (s *Store) GetDeviceRestriction(pubKey string) (DeviceRestriction, error) {
	set, restricted, err := s.aclAllowedSet(pubKey)
	if err != nil {
		return DeviceRestriction{}, err
	}
	dr := DeviceRestriction{Restricted: restricted}
	for k := range set {
		dr.AllowedPubKey = append(dr.AllowedPubKey, k)
	}
	return dr, nil
}

// SetDeviceAllowedPeers replaces pubKey's OWN rules with a fresh set
// pointing at exactly the given peer pubkeys. This never touches any
// other device's rules — restricting X never restricts Y just
// because Y was listed as one of X's allowed peers. Passing an empty
// slice makes the device restricted-with-nobody-allowed (fully
// isolated); to go back to unrestricted/full-mesh, use
// ClearDeviceRestriction instead.
func (s *Store) SetDeviceAllowedPeers(pubKey string, allowedPeers []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM acl_rules WHERE owner_device = ?`, pubKey); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, peer := range allowedPeers {
		if peer == "" || peer == pubKey {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO acl_rules (owner_device, peer_device, created_at) VALUES (?, ?, ?)`, pubKey, peer, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearDeviceRestriction removes pubKey's OWN rules, putting it back
// to the default unrestricted (full mesh) state. It does not touch
// any rule another device has that happens to mention pubKey as an
// allowed peer — that's the other device's own business.
func (s *Store) ClearDeviceRestriction(pubKey string) error {
	_, err := s.db.Exec(`DELETE FROM acl_rules WHERE owner_device = ?`, pubKey)
	return err
}

// RemoveDeviceFromACL is called when a device is deleted from the
// mesh entirely: drops its own rules AND any other device's rule
// that references it (which would otherwise be a dangling, pointless
// permission naming a device that no longer exists).
func (s *Store) RemoveDeviceFromACL(pubKey string) error {
	_, err := s.db.Exec(`DELETE FROM acl_rules WHERE owner_device = ? OR peer_device = ?`, pubKey, pubKey)
	return err
}
