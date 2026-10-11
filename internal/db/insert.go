package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxParams is Postgres's limit on bind parameters per statement.
const maxParams = 65535

// InsertRows inserts n rows into table in as few multi-row INSERT
// statements as Postgres's 65,535-parameter limit allows. row(i) returns
// row i's values in columns' order.
//
// The bulk inserts use this rather than COPY FROM, which Postgres refuses
// for a table whose row-level security policies apply to the current
// role, and one INSERT per row, which costs a network round trip each
// (~45 ms per row against Neon). Values are sent as ordinary parameters,
// so a column that needs a cast (such as pgvector's) must build its own
// statement instead.
func InsertRows(ctx context.Context, tx pgx.Tx, table string, columns []string, n int, row func(i int) ([]any, error)) error {
	if n == 0 {
		return nil
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	prefix := "INSERT INTO " + pgx.Identifier{table}.Sanitize() + " (" + strings.Join(quoted, ", ") + ") VALUES "
	rowsPerStatement := maxParams / len(columns)

	for start := 0; start < n; start += rowsPerStatement {
		end := min(start+rowsPerStatement, n)
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*len(columns))
		for i := start; i < end; i++ {
			values, err := row(i)
			if err != nil {
				return fmt.Errorf("insert into %s: row %d: %w", table, i, err)
			}
			if len(values) != len(columns) {
				return fmt.Errorf("insert into %s: row %d has %d values, want %d", table, i, len(values), len(columns))
			}
			marks := make([]string, len(values))
			for j := range values {
				marks[j] = fmt.Sprintf("$%d", len(args)+j+1)
			}
			placeholders = append(placeholders, "("+strings.Join(marks, ",")+")")
			args = append(args, values...)
		}
		if _, err := tx.Exec(ctx, prefix+strings.Join(placeholders, ","), args...); err != nil {
			return fmt.Errorf("insert into %s: %w", table, err)
		}
	}
	return nil
}
