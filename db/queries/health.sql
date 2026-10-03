-- name: Ping :one
-- Used by GET /health. Proves the database can actually serve a query,
-- which is a stronger signal than a connection-level ping: the pool can be
-- healthy while the database itself refuses to execute statements.
SELECT 1 AS ok;
