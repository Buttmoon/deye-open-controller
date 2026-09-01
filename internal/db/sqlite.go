package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const dbPath = "data/app.db"

func InitSQLite() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping sqlite db: %w", err)
	}

	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA synchronous=NORMAL;`,
		`PRAGMA cache_size=-64000;`,
		`PRAGMA temp_store=MEMORY;`,
		`PRAGMA mmap_size=268435456;`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	if err := createTables(db); err != nil {
		return nil, err
	}

	if err := migrateInvertersTable(db); err != nil {
		return nil, err
	}

	if err := ensureInverterModelColumn(db); err != nil {
		return nil, err
	}
	if err := ensureInverterProfileWriteConfirmationColumn(db); err != nil {
		return nil, err
	}
	if err := ensureLogDeliveryPeriodColumns(db); err != nil {
		return nil, err
	}
	if err := ensureLogDeliveryLocationColumn(db); err != nil {
		return nil, err
	}

	if err := ensureDefaultSettings(db); err != nil {
		return nil, err
	}

	if err := ensureSettingsColumns(db); err != nil {
		return nil, err
	}

	if err := ensureSchedulesAppliedTemplateColumn(db); err != nil {
		return nil, err
	}

	if err := createIndexes(db); err != nil {
		return nil, err
	}

	return db, nil
}

func createTables(db *sql.DB) error {
	inverterTable := `
	CREATE TABLE IF NOT EXISTS inverters (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT '',
		ip TEXT NOT NULL,
		port INTEGER NOT NULL DEFAULT 8899,
		model_key TEXT NOT NULL DEFAULT 'deye_hybrid_60kw_legacy',
		profile_write_confirmed INTEGER NOT NULL DEFAULT 0,
		UNIQUE(ip, port)
	);
	`

	settingsTable := `
	CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		application_name TEXT NOT NULL DEFAULT '',
		timezone TEXT NOT NULL DEFAULT 'UTC',
		scheduler_enabled INTEGER NOT NULL DEFAULT 1,
		file_logging_enabled INTEGER NOT NULL DEFAULT 1,
		scheduler_interval_minutes INTEGER NOT NULL DEFAULT 60,
		scheduler_fill_all_points_current INTEGER NOT NULL DEFAULT 0,
		log_level TEXT NOT NULL DEFAULT 'INFO',
		app_log_max_size_mb INTEGER NOT NULL DEFAULT 20,
		task_log_max_rows INTEGER NOT NULL DEFAULT 1000,
		inverter_logging_enabled INTEGER NOT NULL DEFAULT 0,
		inverter_logging_interval_seconds INTEGER NOT NULL DEFAULT 60,
		inverter_log_max_size_mb INTEGER NOT NULL DEFAULT 20
	);
	`

	templateTable := `
	CREATE TABLE IF NOT EXISTS schedule_templates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL DEFAULT '',
		view_mode TEXT NOT NULL DEFAULT 'list',
		schedule_json TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`

	taskRunsTable := `
	CREATE TABLE IF NOT EXISTS task_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		executed_at_utc DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		inverter_id INTEGER,
		template_id INTEGER,
		status TEXT NOT NULL DEFAULT 'info',
		message TEXT NOT NULL DEFAULT '',
		payload_json TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (inverter_id) REFERENCES inverters(id) ON DELETE SET NULL,
		FOREIGN KEY (template_id) REFERENCES schedule_templates(id) ON DELETE SET NULL
	);
	`

	logDeliverySettingsTable := `
	CREATE TABLE IF NOT EXISTS log_delivery_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		location_name TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 0,
		interval_hours INTEGER NOT NULL DEFAULT 24,
		lookback_hours INTEGER NOT NULL DEFAULT 24,
		lookback_period TEXT NOT NULL DEFAULT 'day',
		lookback_custom_days INTEGER NOT NULL DEFAULT 2,
		smtp_enabled INTEGER NOT NULL DEFAULT 0,
		smtp_host TEXT NOT NULL DEFAULT '',
		smtp_port INTEGER NOT NULL DEFAULT 587,
		smtp_tls_mode TEXT NOT NULL DEFAULT 'starttls',
		smtp_username TEXT NOT NULL DEFAULT '',
		smtp_password TEXT NOT NULL DEFAULT '',
		smtp_from TEXT NOT NULL DEFAULT '',
		smtp_recipients TEXT NOT NULL DEFAULT '',
		smtp_subject TEXT NOT NULL DEFAULT 'Deye inverter log',
		telegram_enabled INTEGER NOT NULL DEFAULT 0,
		telegram_bot_token TEXT NOT NULL DEFAULT '',
		telegram_chat_ids TEXT NOT NULL DEFAULT '',
		telegram_caption TEXT NOT NULL DEFAULT 'Deye inverter XLSX log',
		storage_enabled INTEGER NOT NULL DEFAULT 0,
		storage_provider TEXT NOT NULL DEFAULT 'webdav',
		storage_url TEXT NOT NULL DEFAULT '',
		storage_username TEXT NOT NULL DEFAULT '',
		storage_password TEXT NOT NULL DEFAULT '',
		storage_remote_path TEXT NOT NULL DEFAULT 'inverter-logs',
		google_client_id TEXT NOT NULL DEFAULT '',
		google_client_secret TEXT NOT NULL DEFAULT '',
		google_refresh_token TEXT NOT NULL DEFAULT '',
		google_folder_id TEXT NOT NULL DEFAULT '',
		last_run_utc TEXT NOT NULL DEFAULT '',
		last_status TEXT NOT NULL DEFAULT '',
		last_message TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`

	logDeliveryRunsTable := `
	CREATE TABLE IF NOT EXISTS log_delivery_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		executed_at_utc DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		filename TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0
	);
	`

	scheduleTable := `
	CREATE TABLE IF NOT EXISTS schedules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		inverter_id INTEGER NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		is_enabled INTEGER NOT NULL DEFAULT 1,
		view_mode TEXT NOT NULL DEFAULT 'list',
		schedule_json TEXT NOT NULL,
		applied_template_id INTEGER,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (inverter_id) REFERENCES inverters(id) ON DELETE CASCADE,
		FOREIGN KEY (applied_template_id) REFERENCES schedule_templates(id) ON DELETE SET NULL
	);
	`

	if _, err := db.Exec(inverterTable); err != nil {
		return fmt.Errorf("failed to create inverters table: %w", err)
	}
	if _, err := db.Exec(settingsTable); err != nil {
		return fmt.Errorf("failed to create settings table: %w", err)
	}
	if _, err := db.Exec(templateTable); err != nil {
		return fmt.Errorf("failed to create schedule_templates table: %w", err)
	}
	if _, err := db.Exec(taskRunsTable); err != nil {
		return fmt.Errorf("failed to create task_runs table: %w", err)
	}
	if _, err := db.Exec(scheduleTable); err != nil {
		return fmt.Errorf("failed to create schedules table: %w", err)
	}
	if _, err := db.Exec(logDeliverySettingsTable); err != nil {
		return fmt.Errorf("failed to create log_delivery_settings table: %w", err)
	}
	if _, err := db.Exec(logDeliveryRunsTable); err != nil {
		return fmt.Errorf("failed to create log_delivery_runs table: %w", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO log_delivery_settings (id) VALUES (1)`); err != nil {
		return fmt.Errorf("failed to initialize log_delivery_settings: %w", err)
	}

	return nil
}

func createIndexes(db *sql.DB) error {
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_inverters_name ON inverters(name);`,
		`CREATE INDEX IF NOT EXISTS idx_inverters_ip ON inverters(ip);`,
		`CREATE INDEX IF NOT EXISTS idx_inverters_model_key ON inverters(model_key);`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_inverter ON schedules(inverter_id);`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_applied_template ON schedules(applied_template_id);`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_updated_at ON schedules(updated_at);`,
		`CREATE INDEX IF NOT EXISTS idx_schedule_templates_name ON schedule_templates(name);`,
		`CREATE INDEX IF NOT EXISTS idx_task_runs_inverter ON task_runs(inverter_id);`,
		`CREATE INDEX IF NOT EXISTS idx_task_runs_executed_at ON task_runs(executed_at_utc);`,
		`CREATE INDEX IF NOT EXISTS idx_log_delivery_runs_executed_at ON log_delivery_runs(executed_at_utc);`,
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}
	return nil
}

func ensureDefaultSettings(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		return fmt.Errorf("failed to count settings rows: %w", err)
	}

	if count == 0 {
		if _, err := db.Exec(`INSERT INTO settings (timezone) VALUES ('UTC')`); err != nil {
			return fmt.Errorf("failed to insert default settings: %w", err)
		}
	}

	return nil
}

func migrateInvertersTable(db *sql.DB) error {
	var hasName bool
	var hasPort bool

	rows, err := db.Query(`PRAGMA table_info(inverters);`)
	if err != nil {
		return fmt.Errorf("failed to inspect inverters table: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue any
		var pk int

		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to scan table info: %w", err)
		}

		if name == "name" {
			hasName = true
		}
		if name == "port" {
			hasPort = true
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if hasName && hasPort {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS inverters_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL,
			port INTEGER NOT NULL DEFAULT 8899,
			UNIQUE(ip, port)
		);
	`); err != nil {
		return fmt.Errorf("failed to create temporary inverters table: %w", err)
	}

	switch {
	case hasName && hasPort:
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO inverters_new (id, name, ip, port)
			SELECT
				id,
				CASE
					WHEN TRIM(COALESCE(name, '')) = '' THEN 'Инвертор ' || id
					ELSE name
				END,
				ip,
				port
			FROM inverters;
		`); err != nil {
			return fmt.Errorf("failed to migrate inverters data: %w", err)
		}
	case hasPort:
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO inverters_new (id, name, ip, port)
			SELECT
				id,
				'Инвертор ' || id,
				ip,
				port
			FROM inverters;
		`); err != nil {
			return fmt.Errorf("failed to migrate old inverters data: %w", err)
		}
	default:
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO inverters_new (id, name, ip, port)
			SELECT
				id,
				'Инвертор ' || id,
				ip,
				8899
			FROM inverters;
		`); err != nil {
			return fmt.Errorf("failed to migrate legacy inverters data: %w", err)
		}
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS inverters;`); err != nil {
		return fmt.Errorf("failed to drop old inverters table: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE inverters_new RENAME TO inverters;`); err != nil {
		return fmt.Errorf("failed to rename new inverters table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration: %w", err)
	}

	return nil
}

func ensureInverterModelColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(inverters);`)
	if err != nil {
		return fmt.Errorf("failed to inspect inverters table for model_key: %w", err)
	}
	defer rows.Close()
	hasColumn := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("failed to scan inverters table info: %w", err)
		}
		if name == "model_key" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasColumn {
		if _, err := db.Exec(`ALTER TABLE inverters ADD COLUMN model_key TEXT NOT NULL DEFAULT 'deye_hybrid_60kw_legacy';`); err != nil {
			return fmt.Errorf("failed to add inverters.model_key: %w", err)
		}
	}
	if _, err = db.Exec(`UPDATE inverters SET model_key = 'deye_hybrid_60kw_legacy' WHERE TRIM(COALESCE(model_key, '')) = '';`); err != nil {
		return err
	}
	// Миграция ключей первой multimodel-версии на явные варианты протокола.
	if _, err = db.Exec(`UPDATE inverters SET model_key = 'deye_sun_25k_sg01hp3_eu_am2_v104' WHERE model_key = 'deye_sun_25k_sg01hp3_eu_am2';`); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE inverters SET model_key = 'deye_sun_30k_sg02hp3_eu_am3_v1054' WHERE model_key = 'deye_sun_30k_sg02hp3_eu_am3';`)
	return err
}

func ensureInverterProfileWriteConfirmationColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(inverters);`)
	if err != nil {
		return fmt.Errorf("failed to inspect inverters table for profile_write_confirmed: %w", err)
	}
	defer rows.Close()
	hasColumn := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "profile_write_confirmed" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasColumn {
		if _, err := db.Exec(`ALTER TABLE inverters ADD COLUMN profile_write_confirmed INTEGER NOT NULL DEFAULT 0;`); err != nil {
			return fmt.Errorf("failed to add inverters.profile_write_confirmed: %w", err)
		}
	}
	// Исторический профиль не требует подтверждения; для остальных подтверждение даётся пользователем явно.
	_, err = db.Exec(`UPDATE inverters SET profile_write_confirmed = 1 WHERE model_key = 'deye_hybrid_60kw_legacy';`)
	return err
}

func ensureLogDeliveryPeriodColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(log_delivery_settings);`)
	if err != nil {
		return fmt.Errorf("failed to inspect log_delivery_settings table: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	addedPeriod := false
	if !existing["lookback_period"] {
		if _, err := db.Exec(`ALTER TABLE log_delivery_settings ADD COLUMN lookback_period TEXT NOT NULL DEFAULT 'day';`); err != nil {
			return fmt.Errorf("failed to add log_delivery_settings.lookback_period: %w", err)
		}
		addedPeriod = true
	}
	addedCustomDays := false
	if !existing["lookback_custom_days"] {
		if _, err := db.Exec(`ALTER TABLE log_delivery_settings ADD COLUMN lookback_custom_days INTEGER NOT NULL DEFAULT 2;`); err != nil {
			return fmt.Errorf("failed to add log_delivery_settings.lookback_custom_days: %w", err)
		}
		addedCustomDays = true
	}

	// Preserve the closest semantic preset from the previous lookback_hours-only schema.
	if addedPeriod {
		if _, err := db.Exec(`
			UPDATE log_delivery_settings SET lookback_period = CASE
				WHEN lookback_hours = 6 THEN '6_hours'
				WHEN lookback_hours = 12 THEN '12_hours'
				WHEN lookback_hours = 168 THEN 'week'
				WHEN lookback_hours = 720 THEN 'month'
				WHEN lookback_hours > 24 AND lookback_hours % 24 = 0 THEN 'custom_days'
				ELSE 'day'
			END
			WHERE id = 1;
		`); err != nil {
			return fmt.Errorf("failed to migrate log delivery period: %w", err)
		}
	}
	if addedCustomDays || addedPeriod {
		if _, err := db.Exec(`
			UPDATE log_delivery_settings
			SET lookback_custom_days = CASE
				WHEN lookback_hours >= 48 AND lookback_hours <= 8760 AND lookback_hours % 24 = 0 THEN lookback_hours / 24
				ELSE 2
			END
			WHERE id = 1;
		`); err != nil {
			return fmt.Errorf("failed to migrate log delivery custom days: %w", err)
		}
	}
	return nil
}

func ensureLogDeliveryLocationColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(log_delivery_settings);`)
	if err != nil {
		return fmt.Errorf("failed to inspect log_delivery_settings table: %w", err)
	}
	hasLocation := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "location_name" {
			hasLocation = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasLocation {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE log_delivery_settings ADD COLUMN location_name TEXT NOT NULL DEFAULT '';`); err != nil {
		return fmt.Errorf("failed to add log_delivery_settings.location_name: %w", err)
	}
	return nil
}

func ensureSchedulesAppliedTemplateColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(schedules);`)
	if err != nil {
		return fmt.Errorf("failed to inspect schedules table: %w", err)
	}
	defer rows.Close()

	hasColumn := false
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue any
		var pk int

		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("failed to scan schedules table info: %w", err)
		}
		if name == "applied_template_id" {
			hasColumn = true
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if !hasColumn {
		if _, err := db.Exec(`ALTER TABLE schedules ADD COLUMN applied_template_id INTEGER;`); err != nil {
			return fmt.Errorf("failed to add applied_template_id column: %w", err)
		}
	}

	return nil
}

func ensureSettingsColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(settings);`)
	if err != nil {
		return fmt.Errorf("failed to inspect settings table: %w", err)
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("failed to scan settings table info: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	stmts := []struct{ name, sql string }{
		{"application_name", `ALTER TABLE settings ADD COLUMN application_name TEXT NOT NULL DEFAULT '';`},
		{"scheduler_enabled", `ALTER TABLE settings ADD COLUMN scheduler_enabled INTEGER NOT NULL DEFAULT 1;`},
		{"file_logging_enabled", `ALTER TABLE settings ADD COLUMN file_logging_enabled INTEGER NOT NULL DEFAULT 1;`},
		{"scheduler_interval_minutes", `ALTER TABLE settings ADD COLUMN scheduler_interval_minutes INTEGER NOT NULL DEFAULT 60;`},
		{"scheduler_fill_all_points_current", `ALTER TABLE settings ADD COLUMN scheduler_fill_all_points_current INTEGER NOT NULL DEFAULT 0;`},
		{"log_level", `ALTER TABLE settings ADD COLUMN log_level TEXT NOT NULL DEFAULT 'INFO';`},
		{"app_log_max_size_mb", `ALTER TABLE settings ADD COLUMN app_log_max_size_mb INTEGER NOT NULL DEFAULT 20;`},
		{"task_log_max_rows", `ALTER TABLE settings ADD COLUMN task_log_max_rows INTEGER NOT NULL DEFAULT 1000;`},
		{"inverter_logging_enabled", `ALTER TABLE settings ADD COLUMN inverter_logging_enabled INTEGER NOT NULL DEFAULT 0;`},
		{"inverter_logging_interval_seconds", `ALTER TABLE settings ADD COLUMN inverter_logging_interval_seconds INTEGER NOT NULL DEFAULT 60;`},
		{"inverter_log_max_size_mb", `ALTER TABLE settings ADD COLUMN inverter_log_max_size_mb INTEGER NOT NULL DEFAULT 20;`},
		{"integration_url", `ALTER TABLE settings ADD COLUMN integration_url TEXT NOT NULL DEFAULT '';`},
		{"integration_auth_enabled", `ALTER TABLE settings ADD COLUMN integration_auth_enabled INTEGER NOT NULL DEFAULT 0;`},
		{"integration_auth_scheme", `ALTER TABLE settings ADD COLUMN integration_auth_scheme TEXT NOT NULL DEFAULT '';`},
		{"integration_auth_token", `ALTER TABLE settings ADD COLUMN integration_auth_token TEXT NOT NULL DEFAULT '';`},
		{"integration_auth_username", `ALTER TABLE settings ADD COLUMN integration_auth_username TEXT NOT NULL DEFAULT '';`},
		{"integration_auth_password", `ALTER TABLE settings ADD COLUMN integration_auth_password TEXT NOT NULL DEFAULT '';`},
		{"integration_push_interval_seconds", `ALTER TABLE settings ADD COLUMN integration_push_interval_seconds INTEGER NOT NULL DEFAULT 300;`},
		{"integration_server_name", `ALTER TABLE settings ADD COLUMN integration_server_name TEXT NOT NULL DEFAULT '';`},
		{"integration_server_ip", `ALTER TABLE settings ADD COLUMN integration_server_ip TEXT NOT NULL DEFAULT '';`},
	}
	for _, st := range stmts {
		if !existing[st.name] {
			if _, err := db.Exec(st.sql); err != nil {
				return fmt.Errorf("failed to add %s: %w", st.name, err)
			}
		}
	}
	return nil
}
