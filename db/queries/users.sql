-- name: CreateUser :one
-- Returns no row when the email is already taken, so the caller can report a
-- conflict without a separate existence check that would race.
INSERT INTO users (email, password_hash)
VALUES ($1, $2)
ON CONFLICT (lower(email)) DO NOTHING
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;
