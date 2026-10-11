package main

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type syncDeviceRefError struct {
	Code    string
	Message string
	Status  int
}

func (e *syncDeviceRefError) Error() string {
	return e.Message
}

func decodeSyncRef(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &syncDeviceRefError{Code: "invalid_ref", Message: "пустой ref устройства", Status: 400}
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", &syncDeviceRefError{Code: "invalid_ref", Message: "некорректный URL-encoded ref", Status: 400}
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return "", &syncDeviceRefError{Code: "invalid_ref", Message: "пустой ref устройства", Status: 400}
	}
	return decoded, nil
}

func parseHostPortRef(ref string) (host string, port int, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", 0, false
	}
	// Require an explicit host:port form. Bare integers are external_id, not ports.
	if !strings.Contains(ref, ":") {
		return "", 0, false
	}
	h, p, err := net.SplitHostPort(ref)
	if err != nil {
		return "", 0, false
	}
	portNum, err := strconv.Atoi(p)
	if err != nil || portNum < 1 || portNum > 65535 {
		return "", 0, false
	}
	h = strings.TrimSpace(h)
	if h == "" {
		return "", 0, false
	}
	return h, portNum, true
}

// resolveSyncDeviceRef maps Sync {ref} to an inverter row.
// Order: host:port (incl. [ipv6]:port) → external_id exact match.
// Never falls back to internal DB id.
func (a *App) resolveSyncDeviceRef(rawRef string) (inverterID int64, canonicalRef string, err error) {
	ref, err := decodeSyncRef(rawRef)
	if err != nil {
		return 0, "", err
	}
	if host, port, ok := parseHostPortRef(ref); ok {
		var id int64
		var name string
		qErr := a.db.QueryRow(`SELECT id, name FROM inverters WHERE ip = ? AND port = ?`, host, port).Scan(&id, &name)
		if qErr == sql.ErrNoRows {
			return 0, "", &syncDeviceRefError{Code: "device_not_found", Message: "устройство не найдено по ip:port", Status: 404}
		}
		if qErr != nil {
			return 0, "", qErr
		}
		return id, fmt.Sprintf("%s:%d", host, port), nil
	}

	var id int64
	var externalID sql.NullString
	qErr := a.db.QueryRow(`SELECT id, external_id FROM inverters WHERE external_id = ?`, ref).Scan(&id, &externalID)
	if qErr == sql.ErrNoRows {
		return 0, "", &syncDeviceRefError{Code: "device_not_found", Message: "устройство не найдено по external_id", Status: 404}
	}
	if qErr != nil {
		return 0, "", qErr
	}
	if !externalID.Valid || strings.TrimSpace(externalID.String) == "" {
		return 0, "", &syncDeviceRefError{Code: "device_not_found", Message: "устройство не найдено по external_id", Status: 404}
	}
	return id, strings.TrimSpace(externalID.String), nil
}

func (a *App) setInverterExternalID(inverterID int64, externalID string) error {
	externalID = strings.TrimSpace(externalID)
	var value any
	if externalID == "" {
		value = nil
	} else {
		value = externalID
	}
	_, err := a.db.Exec(`UPDATE inverters SET external_id = ? WHERE id = ?`, value, inverterID)
	return err
}
