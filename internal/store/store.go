package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Preference struct {
	Exists  bool
	Enabled bool
	Grants  []string
}

type NativeTrust struct {
	Exists    bool
	SHA256    string
	TrustedAt int64
}

type InstalledPlugin struct {
	ID               string
	SourceURL        string
	RepoOwner        string
	RepoName         string
	RefOrTag         string
	AssetName        string
	InstalledVersion string
	InstalledAt      int64
}

type Widget struct {
	ID, Type, Monitor              string
	X, Y, Width, Height            int
	Placed                         bool
	Collapsed, Locked, AlwaysOnTop bool
	ExpandedHeight                 int
	IconSize                       int
}

type Snapshot struct {
	Initialized bool
	State       json.RawMessage
	Widgets     []Widget
}

type Mutations struct {
	Initialize bool
	State      json.RawMessage
	Create     []Widget
	Delete     []string
}

func Open(dir string) (*Store, error) {
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize() error {
	const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS plugin_preferences (
	id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, grants TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS plugin_state (
	id TEXT PRIMARY KEY, state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS widgets (
	plugin TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL,
	monitor TEXT NOT NULL DEFAULT '', x INTEGER NOT NULL DEFAULT 0,
	y INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0,
	collapsed INTEGER NOT NULL DEFAULT 0, locked INTEGER NOT NULL DEFAULT 0,
	always_on_top INTEGER NOT NULL DEFAULT 0, expanded_height INTEGER NOT NULL DEFAULT 0,
	icon_size INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (plugin, id)
);
CREATE TABLE IF NOT EXISTS native_plugin_trust (
	id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, trusted_at INTEGER NOT NULL
);`
	const migrateV5ToV6 = `
CREATE TABLE IF NOT EXISTS installed_plugins (
	id TEXT PRIMARY KEY,
	source_url TEXT NOT NULL,
	repo_owner TEXT NOT NULL DEFAULT '',
	repo_name TEXT NOT NULL DEFAULT '',
	ref_or_tag TEXT NOT NULL DEFAULT '',
	asset_name TEXT NOT NULL DEFAULT '',
	installed_version TEXT NOT NULL,
	installed_at INTEGER NOT NULL
);
PRAGMA user_version = 6;`
	if _, err := s.db.Exec("PRAGMA busy_timeout = 3000"); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 6 {
		return nil
	}
	if version >= 1 && version <= 5 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if version == 1 {
			const migrateV1ToV2 = `
ALTER TABLE widgets ADD COLUMN collapsed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE widgets ADD COLUMN locked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE widgets ADD COLUMN hover_expand INTEGER NOT NULL DEFAULT 0;
ALTER TABLE widgets ADD COLUMN expanded_height INTEGER NOT NULL DEFAULT 0;`
			if _, err := tx.Exec(migrateV1ToV2); err != nil {
				return err
			}
		}
		if version <= 2 {
			const migrateV2ToV3 = `
ALTER TABLE widgets ADD COLUMN icon_size INTEGER NOT NULL DEFAULT 0;`
			if _, err := tx.Exec(migrateV2ToV3); err != nil {
				return err
			}
		}
		if version <= 3 {
			const migrateV3ToV4 = `
CREATE TABLE IF NOT EXISTS native_plugin_trust (
	id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, trusted_at INTEGER NOT NULL
);`
			if _, err := tx.Exec(migrateV3ToV4); err != nil {
				return err
			}
		}
		if version <= 4 {
			const migrateV4ToV5 = `
ALTER TABLE widgets DROP COLUMN hover_expand;
ALTER TABLE widgets ADD COLUMN always_on_top INTEGER NOT NULL DEFAULT 0;`
			if _, err := tx.Exec(migrateV4ToV5); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(migrateV5ToV6); err != nil {
			return err
		}
		return tx.Commit()
	}
	if version != 0 {
		return fmt.Errorf("unsupported database version %d", version)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema + migrateV5ToV6); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Setting(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DirectoryGrantsSettingKey identifies the persisted user-picked directories for a plugin.
func DirectoryGrantsSettingKey(id string) string {
	return fmt.Sprintf("directories.%x", sha256.Sum256([]byte(id)))
}

func (s *Store) Preference(ctx context.Context, id string) (Preference, error) {
	var p Preference
	var grants string
	err := s.db.QueryRowContext(ctx, "SELECT enabled, grants FROM plugin_preferences WHERE id = ?", id).
		Scan(&p.Enabled, &grants)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.Exists = true
	return p, json.Unmarshal([]byte(grants), &p.Grants)
}

func (s *Store) SetPreference(ctx context.Context, id string, enabled bool, grants []string) error {
	encoded, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO plugin_preferences(id, enabled, grants) VALUES(?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, grants = excluded.grants`, id, enabled, string(encoded))
	return err
}

func (s *Store) NativeTrust(ctx context.Context, id string) (NativeTrust, error) {
	var trust NativeTrust
	err := s.db.QueryRowContext(ctx, "SELECT sha256, trusted_at FROM native_plugin_trust WHERE id = ?", id).
		Scan(&trust.SHA256, &trust.TrustedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return trust, nil
	}
	if err != nil {
		return trust, err
	}
	trust.Exists = true
	return trust, nil
}

func (s *Store) SetNativeTrust(ctx context.Context, id, digest string, trustedAt int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_plugin_trust(id, sha256, trusted_at) VALUES(?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET sha256 = excluded.sha256, trusted_at = excluded.trusted_at`, id, digest, trustedAt)
	return err
}

func (s *Store) DeleteNativeTrust(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM native_plugin_trust WHERE id = ?", id)
	return err
}

const installedPluginColumns = `id, source_url, repo_owner, repo_name, ref_or_tag, asset_name, installed_version, installed_at`

func (s *Store) InstalledPlugin(ctx context.Context, id string) (InstalledPlugin, error) {
	meta, err := scanInstalledPlugin(s.db.QueryRowContext(ctx,
		"SELECT "+installedPluginColumns+" FROM installed_plugins WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return InstalledPlugin{}, nil
	}
	return meta, err
}

func (s *Store) AllInstalledPlugins(ctx context.Context) (map[string]InstalledPlugin, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+installedPluginColumns+" FROM installed_plugins")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	installed := make(map[string]InstalledPlugin)
	for rows.Next() {
		meta, err := scanInstalledPlugin(rows)
		if err != nil {
			return nil, err
		}
		installed[meta.ID] = meta
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return installed, nil
}

func scanInstalledPlugin(row interface{ Scan(...any) error }) (InstalledPlugin, error) {
	var meta InstalledPlugin
	err := row.Scan(&meta.ID, &meta.SourceURL, &meta.RepoOwner, &meta.RepoName, &meta.RefOrTag,
		&meta.AssetName, &meta.InstalledVersion, &meta.InstalledAt)
	return meta, err
}

func (s *Store) SetInstalledPlugin(ctx context.Context, meta InstalledPlugin) error {
	return s.setInstalledPlugin(ctx, meta, false)
}

// SetNewInstalledPlugin clears stale authorizations for a plugin whose package is absent.
func (s *Store) SetNewInstalledPlugin(ctx context.Context, meta InstalledPlugin) error {
	return s.setInstalledPlugin(ctx, meta, true)
}

func (s *Store) setInstalledPlugin(ctx context.Context, meta InstalledPlugin, fresh bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if fresh {
		if _, err := tx.ExecContext(ctx, "DELETE FROM native_plugin_trust WHERE id = ?", meta.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_preferences WHERE id = ?", meta.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", DirectoryGrantsSettingKey(meta.ID)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO installed_plugins (`+installedPluginColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET source_url = excluded.source_url, repo_owner = excluded.repo_owner,
			repo_name = excluded.repo_name, ref_or_tag = excluded.ref_or_tag, asset_name = excluded.asset_name,
			installed_version = excluded.installed_version, installed_at = excluded.installed_at`,
		meta.ID, meta.SourceURL, meta.RepoOwner, meta.RepoName, meta.RefOrTag,
		meta.AssetName, meta.InstalledVersion, meta.InstalledAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PurgePlugin(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", DirectoryGrantsSettingKey(id)); err != nil {
		return err
	}
	for _, query := range []string{
		"DELETE FROM installed_plugins WHERE id = ?",
		"DELETE FROM native_plugin_trust WHERE id = ?",
		"DELETE FROM plugin_preferences WHERE id = ?",
		"DELETE FROM plugin_state WHERE id = ?",
		"DELETE FROM widgets WHERE plugin = ?",
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Snapshot(ctx context.Context, plugin string) (Snapshot, error) {
	out := Snapshot{State: json.RawMessage("{}")}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, "SELECT state FROM plugin_state WHERE id = ?", plugin).Scan(&state)
	if err == nil {
		out.Initialized = true
		out.State = json.RawMessage(state)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, type, monitor, x, y, width, height, placed,
		collapsed, locked, always_on_top, expanded_height, icon_size
		FROM widgets WHERE plugin = ? ORDER BY rowid`, plugin)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var widget Widget
		if err := rows.Scan(&widget.ID, &widget.Type, &widget.Monitor, &widget.X, &widget.Y, &widget.Width, &widget.Height, &widget.Placed,
			&widget.Collapsed, &widget.Locked, &widget.AlwaysOnTop, &widget.ExpandedHeight, &widget.IconSize); err != nil {
			return out, err
		}
		out.Widgets = append(out.Widgets, widget)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) Apply(ctx context.Context, plugin string, change Mutations) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if change.Initialize {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO plugin_state(id, state) VALUES(?, '{}')`, plugin); err != nil {
			return err
		}
	}
	if len(change.State) != 0 {
		if !json.Valid(change.State) {
			return errors.New("invalid plugin state JSON")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_state(id, state) VALUES(?, ?)
			ON CONFLICT(id) DO UPDATE SET state = excluded.state`, plugin, string(change.State)); err != nil {
			return err
		}
	}
	for _, id := range change.Delete {
		if _, err := tx.ExecContext(ctx, "DELETE FROM widgets WHERE plugin = ? AND id = ?", plugin, id); err != nil {
			return err
		}
	}
	for _, item := range change.Create {
		if _, err := tx.ExecContext(ctx, `INSERT INTO widgets(plugin, id, type, width, height) VALUES(?, ?, ?, ?, ?)`,
			plugin, item.ID, item.Type, item.Width, item.Height); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Move(ctx context.Context, plugin string, w Widget) error {
	_, err := s.db.ExecContext(ctx, `UPDATE widgets SET monitor = ?, x = ?, y = ?, width = ?, height = ?, placed = ?,
		collapsed = ?, locked = ?, always_on_top = ?, expanded_height = ?, icon_size = ?
		WHERE plugin = ? AND id = ?`, w.Monitor, w.X, w.Y, w.Width, w.Height, w.Placed,
		w.Collapsed, w.Locked, w.AlwaysOnTop, w.ExpandedHeight, w.IconSize, plugin, w.ID)
	return err
}
