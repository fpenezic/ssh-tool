package store

import (
	"database/sql"
	"errors"
)

// AppSetting is a simple key/value pair stored in app_settings.
// Used for things that are global to the app rather than per-connection,
// e.g. preferred browser binary path, theme, font size.

func (d *DB) GetSetting(key string) (string, bool, error) {
	var v string
	err := d.conn.QueryRow("SELECT value FROM app_settings WHERE key = ?", key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(
		`INSERT INTO app_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (d *DB) DeleteSetting(key string) error {
	_, err := d.conn.Exec("DELETE FROM app_settings WHERE key = ?", key)
	return err
}
