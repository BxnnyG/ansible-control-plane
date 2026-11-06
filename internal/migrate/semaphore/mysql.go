package semaphore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// MySQLSource reads a Semaphore MySQL database with SELECT statements inside
// a read-only transaction.
type MySQLSource struct {
	DB *sql.DB
}

// OpenMySQL opens a source from a DSN. The DSN is never included in errors.
func OpenMySQL(dsn string) (*MySQLSource, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid source DSN")
	}
	cfg.ParseTime = false
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("invalid source DSN")
	}
	return &MySQLSource{DB: sql.OpenDB(conn)}, nil
}

// Close releases the connection pool.
func (m *MySQLSource) Close() error { return m.DB.Close() }

const mysqlNoSuchTable = 1146

// Load implements Source. Missing tables become warnings, not errors, because
// the schema differs between Semaphore versions.
func (m *MySQLSource) Load(ctx context.Context) (*Snapshot, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin read-only transaction: %w", redactErr(err))
	}
	defer func() { _ = tx.Rollback() }()

	var t tables
	targets := []struct {
		name string
		dst  *[]row
		opt  bool
	}{
		{"project", &t.project, false},
		{"access_key", &t.accessKey, false},
		{"project__repository", &t.repository, false},
		{"project__inventory", &t.inventory, false},
		{"project__environment", &t.environment, false},
		{"project__template", &t.template, false},
		{"project__template_vault", &t.templateVault, true},
		{"project__schedule", &t.schedule, true},
		{"user", &t.user, true},
		{"project__user", &t.projectUser, true},
	}
	for _, tg := range targets {
		rows, err := selectAll(ctx, tx, tg.name)
		if err != nil {
			if isNoSuchTable(err) {
				if !tg.opt {
					return nil, fmt.Errorf("required table %s not found: not a Semaphore database?", tg.name)
				}
				t.warnings = append(t.warnings, "table "+tg.name+" not found")
				continue
			}
			return nil, fmt.Errorf("read %s: %w", tg.name, redactErr(err))
		}
		*tg.dst = rows
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task`").Scan(&t.taskCount); err != nil {
		if !isNoSuchTable(err) {
			return nil, fmt.Errorf("count task: %w", redactErr(err))
		}
		t.warnings = append(t.warnings, "table task not found")
	}
	return buildSnapshot(t), nil
}

// selectAll reads every row of a table. Table names are a fixed internal list,
// never user input.
func selectAll(ctx context.Context, tx *sql.Tx, table string) ([]row, error) {
	rs, err := tx.QueryContext(ctx, "SELECT * FROM `"+table+"` ORDER BY 1") //nolint:gosec // table is from a fixed internal list
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rs.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(row, len(cols))
		for i, c := range cols {
			r[strings.ToLower(c)] = vals[i]
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func isNoSuchTable(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlNoSuchTable
}

// redactErr strips driver details that could echo connection parameters.
func redactErr(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return fmt.Errorf("mysql error %d", me.Number)
	}
	return errors.New("connection or query failed")
}
