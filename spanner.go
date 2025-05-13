package testfixtures

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type spanner struct {
	baseHelper

	cleanTableFn func(string) string
	constraints  map[string][]SpannerConstraint
}

type SpannerConstraint struct {
	TableName   string
	ConstraintName string
	ColumnName string
	Position  int
	ReferencedTable   string
	ReferencedColumn  string
}

func (h *spanner) init(db *sql.DB) error {
	if h.cleanTableFn == nil {
		h.cleanTableFn = func(tableName string) string {
			return fmt.Sprintf("DELETE FROM %s WHERE true;", tableName)
		}
	}

	var err error
	h.constraints, err = h.getConstraints(db)
	if err != nil {
		return err
	}

	return nil
}

func (*spanner) paramType() int {
	return paramTypeAtSign
}

func (*spanner) quoteKeyword(str string) string {
	return str
}

func (*spanner) databaseName(q queryable) (string, error) {
	return "", errors.New("could not determine database name. Please skip the test database check")
}

func (h *spanner) tableNames(q queryable) ([]string, error) {
	query := `
		SELECT TABLE_NAME
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = '';
	`

	rows, err := q.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func (h *spanner) disableReferentialIntegrity(db *sql.DB, loadFn loadFunction) (err error) {
	return h.dropAndRecreateConstraints(db, loadFn)
}

func (h *spanner) cleanTableQuery(tableName string) string {
	if h.cleanTableFn == nil {
		return h.baseHelper.cleanTableQuery(tableName)
	}

	return h.cleanTableFn(tableName)
}

const SpannerConstraintsQuery = `
	SELECT 
			tc.TABLE_NAME AS table_name,
			tc.CONSTRAINT_NAME AS constraint_name,
			kcu.COLUMN_NAME AS column_name,
			kcu.ORDINAL_POSITION AS position,
			kcu2.TABLE_NAME AS referenced_table,
			kcu2.COLUMN_NAME AS referenced_column
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.KEY_COLUMN_USAGE kcu
			ON tc.CONSTRAINT_SCHEMA = kcu.CONSTRAINT_SCHEMA
			AND tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
		JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
			ON tc.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
			AND tc.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
		JOIN information_schema.KEY_COLUMN_USAGE kcu2
			ON rc.UNIQUE_CONSTRAINT_SCHEMA = kcu2.CONSTRAINT_SCHEMA
			AND rc.UNIQUE_CONSTRAINT_NAME = kcu2.CONSTRAINT_NAME
			AND kcu.ORDINAL_POSITION = kcu2.ORDINAL_POSITION
		WHERE tc.CONSTRAINT_TYPE = 'FOREIGN KEY'
		ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME, kcu.ORDINAL_POSITION;
`

func (h *spanner) getConstraints(q queryable) (map[string][]SpannerConstraint, error) {
	var constraints = make(map[string][]SpannerConstraint)

	rows, err := q.Query(SpannerConstraintsQuery)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var constraint SpannerConstraint
		if err = rows.Scan(
			&constraint.TableName,
			&constraint.ConstraintName,
			&constraint.ColumnName,
			&constraint.Position,
			&constraint.ReferencedTable,
			&constraint.ReferencedColumn,
		); err != nil {
			return nil, err
		}

		if constraints[constraint.ConstraintName] == nil {
			constraints[constraint.ConstraintName] = []SpannerConstraint{}
		}
		constraints[constraint.ConstraintName] = append(constraints[constraint.ConstraintName], constraint)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return constraints, nil
}


func (h *spanner) dropAndRecreateConstraints(db *sql.DB, loadFn loadFunction) (err error) {
	defer func() {
		// Re-create constraints again after load
		for key := range h.constraints {
			var lengthConstraints = len(h.constraints[key])
			var orderedConstraints = make([]SpannerConstraint, lengthConstraints)

			for _, constraint := range h.constraints[key] {
				orderedConstraints[constraint.Position-1] = constraint
			}

			var columnName = orderedConstraints[0].ColumnName
			for i := 1; i < lengthConstraints; i++ {
				columnName = strings.Join([]string{columnName, orderedConstraints[i].ColumnName}, ", ")
			}

			var referencedColumn = orderedConstraints[0].ReferencedColumn
			for i := 1; i < lengthConstraints; i++ {
				referencedColumn = strings.Join([]string{referencedColumn, orderedConstraints[i].ReferencedColumn}, ", ")
			}

			cmd := fmt.Sprintf(
				`ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)`,
				orderedConstraints[0].TableName,
				orderedConstraints[0].ConstraintName,
				columnName,
				orderedConstraints[0].ReferencedTable,
				referencedColumn,
			)

			if _, err2 := db.Exec(cmd); err2 != nil && err == nil {
				err = err2
			}
		}
	}()

	for key := range h.constraints {
		constraints := h.constraints[key]
		cmd := fmt.Sprintf(
			`ALTER TABLE %s DROP CONSTRAINT %s`,
			constraints[0].TableName,
			constraints[0].ConstraintName,
		)
		if _, err := db.Exec(cmd); err != nil {
			fmt.Println("error dropping constraint", err)
			return err
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err = loadFn(tx); err != nil {
		return err
	}

	return tx.Commit()
}