/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// Database drivers — register with database/sql
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// SQLTool executes read-only SQL queries against databases.
// Enforces read-only at the driver level (not just prompt-level).
type SQLTool struct {
	databases map[string]*SQLDatabase
}

// SQLDatabase describes a database the agent can query.
type SQLDatabase struct {
	// Driver is the database driver ("postgres", "mysql").
	Driver string

	// DSN is the data source name (connection string).
	// Credentials should be injected by the credential manager.
	DSN string

	// AllowedQueries is an optional regex allowlist. Empty = default (SELECT/SHOW/DESCRIBE/EXPLAIN only).
	AllowedQueries []string

	// MaxRows caps result rows (default 1000).
	MaxRows int

	// MaxBytes caps total response bytes (default 8192).
	MaxBytes int

	// Timeout per query (default 30s).
	Timeout time.Duration
}

// NewSQLTool creates a SQL tool with configured database connections.
func NewSQLTool(databases map[string]*SQLDatabase) *SQLTool {
	for _, db := range databases {
		if db.MaxRows == 0 {
			db.MaxRows = 1000
		}
		if db.MaxBytes == 0 {
			db.MaxBytes = 8192
		}
		if db.Timeout == 0 {
			db.Timeout = 30 * time.Second
		}
	}
	return &SQLTool{databases: databases}
}

func (t *SQLTool) Name() string { return "sql.query" }

func (t *SQLTool) Description() string {
	dbs := make([]string, 0, len(t.databases))
	for name := range t.databases {
		dbs = append(dbs, name)
	}
	return fmt.Sprintf("Execute read-only SQL queries against databases. Available: %s", strings.Join(dbs, ", "))
}

func (t *SQLTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"database": map[string]interface{}{
			"type":        "string",
			"description": "Database name to query",
			"required":    true,
		},
		"query": map[string]interface{}{
			"type":        "string",
			"description": "SQL query to execute (read-only: SELECT, SHOW, DESCRIBE, EXPLAIN)",
			"required":    true,
		},
	}
}

func (t *SQLTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	dbName, _ := args["database"].(string)
	query, _ := args["query"].(string)

	if dbName == "" {
		return "", fmt.Errorf("database name is required")
	}
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	db, ok := t.databases[dbName]
	if !ok {
		available := make([]string, 0, len(t.databases))
		for name := range t.databases {
			available = append(available, name)
		}
		return "", fmt.Errorf("unknown database %q, available: %s", dbName, strings.Join(available, ", "))
	}

	// Classify the query BEFORE executing
	tier := classifySQLQuery(query)
	if tier != TierRead {
		return "", fmt.Errorf("BLOCKED: only read-only queries are allowed (SELECT, SHOW, DESCRIBE, EXPLAIN). Got tier: %s", tier)
	}

	// Check for SQL injection patterns
	if containsSQLInjection(query) {
		return "", fmt.Errorf("BLOCKED: query contains suspicious patterns (multiple statements, comments)")
	}

	// Execute with timeout
	queryCtx, cancel := context.WithTimeout(ctx, db.Timeout)
	defer cancel()

	// Map driver names to database/sql registered names
	driverName := db.Driver
	if driverName == "postgres" || driverName == "postgresql" {
		driverName = "pgx" // pgx/v5/stdlib registers as "pgx"
	}

	conn, err := sql.Open(driverName, db.DSN)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", dbName, err)
	}
	defer conn.Close()

	// Force read-only transaction
	tx, err := conn.BeginTx(queryCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(queryCtx, query)
	if err != nil {
		return "", fmt.Errorf("execute query: %w", err)
	}
	defer rows.Close()

	// Format results
	result, err := formatSQLResults(rows, db.MaxRows, db.MaxBytes)
	if err != nil {
		return "", fmt.Errorf("format results: %w", err)
	}

	return result, nil
}

// Capability implements ClassifiableTool.
func (t *SQLTool) Capability() ToolCapability {
	return ToolCapability{
		Domain:              "sql",
		SupportedTiers:      []ActionTier{TierRead},
		RequiresCredentials: true,
		RequiresConnection:  true,
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *SQLTool) ClassifyAction(action string, args map[string]interface{}) ActionClassification {
	query, _ := args["query"].(string)
	tier := classifySQLQuery(query)

	classification := ActionClassification{
		Tier:        tier,
		Target:      args["database"].(string),
		Description: truncateQuery(query, 100),
	}

	// Block anything that isn't a read
	if tier != TierRead {
		classification.Blocked = true
		classification.BlockReason = fmt.Sprintf("SQL tool only allows read-only queries; got %s-tier query", tier)
	}

	return classification
}

// classifySQLQuery determines the action tier of a SQL query.
func classifySQLQuery(query string) ActionTier {
	normalized := strings.TrimSpace(strings.ToUpper(query))

	// Read operations
	readPrefixes := []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "\\D", "\\DT", "\\L"}
	for _, prefix := range readPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return TierRead
		}
	}

	// Service mutations (schema changes, index management)
	serviceMutationPrefixes := []string{
		"CREATE INDEX", "DROP INDEX", "ALTER INDEX",
		"CREATE VIEW", "DROP VIEW",
		"ANALYZE", "VACUUM", "REINDEX",
		"GRANT", "REVOKE",
		"CREATE ROLE", "ALTER ROLE",
		"SET ",
	}
	for _, prefix := range serviceMutationPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return TierServiceMutation
		}
	}

	// Data mutations (ALWAYS blocked)
	dataMutationPrefixes := []string{
		"INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT",
		"COPY", "LOAD DATA",
	}
	for _, prefix := range dataMutationPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return TierDataMutation
		}
	}

	// Destructive mutations
	destructivePrefixes := []string{
		"DROP TABLE", "DROP DATABASE", "DROP SCHEMA",
		"TRUNCATE", "ALTER TABLE",
		"CREATE TABLE", "CREATE DATABASE", "CREATE SCHEMA",
	}
	for _, prefix := range destructivePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return TierDestructiveMutation
		}
	}

	// Unknown — treat as destructive (fail-closed)
	return TierDestructiveMutation
}

// containsSQLInjection checks for common SQL injection patterns.
func containsSQLInjection(query string) bool {
	normalized := strings.ToUpper(query)

	// Multiple statements (semicolons not at end)
	trimmed := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(query), ";"))
	if strings.Contains(trimmed, ";") {
		return true
	}

	// SQL comment injection
	if strings.Contains(normalized, "--") || strings.Contains(normalized, "/*") {
		return true
	}

	// UNION-based injection (suspicious in LLM-generated queries)
	if strings.Contains(normalized, "UNION") && strings.Contains(normalized, "SELECT") {
		// Allow legitimate UNION queries by checking if UNION is at the start of a word
		// but flag UNION inside string literals or suspicious positions
		// Simple heuristic: flag any UNION that follows a quote
		if strings.Contains(normalized, "'") && strings.Contains(normalized, "UNION") {
			return true
		}
	}

	return false
}

// formatSQLResults converts query results to a formatted string.
func formatSQLResults(rows *sql.Rows, maxRows int, maxBytes int) (string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var sb strings.Builder

	// Header
	sb.WriteString(strings.Join(columns, "\t"))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", len(strings.Join(columns, "\t"))))
	sb.WriteString("\n")

	// Data
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	rowCount := 0
	for rows.Next() {
		if rowCount >= maxRows {
			sb.WriteString(fmt.Sprintf("\n... truncated at %d rows", maxRows))
			break
		}
		if sb.Len() >= maxBytes {
			sb.WriteString(fmt.Sprintf("\n... truncated at %d bytes", maxBytes))
			break
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return sb.String(), fmt.Errorf("scan row %d: %w", rowCount, err)
		}

		for i, v := range values {
			if i > 0 {
				sb.WriteString("\t")
			}
			switch val := v.(type) {
			case nil:
				sb.WriteString("NULL")
			case []byte:
				sb.WriteString(string(val))
			default:
				sb.WriteString(fmt.Sprintf("%v", val))
			}
		}
		sb.WriteString("\n")
		rowCount++
	}

	if rowCount == 0 {
		sb.WriteString("(0 rows)\n")
	} else {
		sb.WriteString(fmt.Sprintf("\n(%d rows)\n", rowCount))
	}

	return sb.String(), rows.Err()
}

func truncateQuery(q string, max int) string {
	if len(q) <= max {
		return q
	}
	return q[:max] + "..."
}

// DefaultSQLProtectionClass returns protection rules for SQL databases.
func DefaultSQLProtectionClass() ProtectionClass {
	return ProtectionClass{
		Name:        "sql-defaults",
		Description: "Prevents destructive SQL operations — data mutations are never automated",
		Rules: []ProtectionRule{
			{Domain: "sql", Pattern: "DROP DATABASE*", Action: ProtectionBlock, Description: "Database deletion is never automated"},
			{Domain: "sql", Pattern: "DROP TABLE*", Action: ProtectionBlock, Description: "Table deletion is never automated"},
			{Domain: "sql", Pattern: "TRUNCATE*", Action: ProtectionBlock, Description: "Table truncation is never automated"},
			{Domain: "sql", Pattern: "DELETE*", Action: ProtectionBlock, Description: "Data deletion is never automated"},
			{Domain: "sql", Pattern: "INSERT*", Action: ProtectionBlock, Description: "Data insertion is never automated"},
			{Domain: "sql", Pattern: "UPDATE*", Action: ProtectionBlock, Description: "Data updates are never automated"},
			{Domain: "sql", Pattern: "DROP SCHEMA*", Action: ProtectionBlock, Description: "Schema deletion is never automated"},
			{Domain: "sql", Pattern: "ALTER TABLE*DROP*", Action: ProtectionBlock, Description: "Column drops are never automated"},
		},
	}
}
