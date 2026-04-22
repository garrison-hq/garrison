-- name: GetDepartmentByID :one
SELECT * FROM departments WHERE id = $1;

-- name: InsertDepartment :one
INSERT INTO departments (slug, name, concurrency_cap)
VALUES ($1, $2, $3)
RETURNING *;
