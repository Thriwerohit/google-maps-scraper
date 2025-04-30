package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"

	"github.com/gosom/google-maps-scraper/web"
)

type repo struct {
	db *sql.DB
}

func New(path string) (web.JobRepository, error) {
	db, err := initDatabase("postgresql://postgres:Z5Cq26NnxmUXGL3kAhcf4wPVSv9Q@127.0.0.1/discovery")
	if err != nil {
		return nil, err
	}

	return &repo{db: db}, nil
}

func (repo *repo) Get(ctx context.Context, id string) (web.Job, error) {
	const q = `SELECT * FROM jobs WHERE id = $1`

	row := repo.db.QueryRowContext(ctx, q, id)

	return rowToJob(row)
}

func (repo *repo) Create(ctx context.Context, job *web.Job) error {
	item, err := jobToRow(job)
	if err != nil {
		return err
	}

	const q = `
		INSERT INTO jobs (id, name, status, data, created_at, updated_at,facility_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err = repo.db.ExecContext(ctx, q, item.ID, item.Name, item.Status, item.Data, item.CreatedAt, item.UpdatedAt, item.FacilityID)
	if err != nil {
		return err
	}

	return nil
}

func (repo *repo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM jobs WHERE id = $1`

	_, err := repo.db.ExecContext(ctx, q, id)

	return err
}

func (repo *repo) Select(ctx context.Context, params web.SelectParams) ([]web.Job, error) {
	q := `SELECT * FROM jobs`
	var args []any
	argIdx := 1 // PostgreSQL uses $1, $2, ...

	if params.Status != "" {
		q += fmt.Sprintf(" WHERE status = $%d", argIdx)
		args = append(args, params.Status)
		argIdx++
	}

	q += " ORDER BY created_at DESC"

	if params.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, params.Limit)
		argIdx++
	}

	rows, err := repo.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ans []web.Job
	for rows.Next() {
		job, err := rowToJob(rows)
		if err != nil {
			return nil, err
		}
		ans = append(ans, job)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return ans, nil
}

func (repo *repo) Update(ctx context.Context, job *web.Job) error {
	item, err := jobToRow(job)
	if err != nil {
		return err
	}

	const q = `
	UPDATE jobs
	SET name = $1, status = $2, data = $3, updated_at = $4
	WHERE id = $5
`

	_, err = repo.db.ExecContext(ctx, q, item.Name, item.Status, item.Data, item.UpdatedAt, item.ID)

	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func rowToJob(row scannable) (web.Job, error) {
	var j job

	err := row.Scan(&j.ID, &j.Name, &j.FacilityID, &j.Status, &j.Data, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return web.Job{}, err
	}

	ans := web.Job{
		ID:     j.ID,
		Name:   j.Name,
		Status: j.Status,
		Date:   time.Unix(j.CreatedAt, 0).UTC(),
		Data:   web.JobData{FacilityId: j.FacilityID}, // default fallback
	}

	if err := json.Unmarshal([]byte(j.Data), &ans.Data); err != nil {
		fmt.Printf("Failed to unmarshal job data: %s\nRaw: %s\n", err, j.Data)
		return web.Job{}, err
	}

	return ans, nil
}

func jobToRow(item *web.Job) (job, error) {
	data, err := json.Marshal(item.Data)
	if err != nil {
		return job{}, err
	}

	return job{
		ID:         item.ID,
		Name:       item.Name,
		Status:     item.Status,
		Data:       string(data),
		CreatedAt:  item.Date.Unix(),
		UpdatedAt:  time.Now().UTC().Unix(),
		FacilityID: item.Data.FacilityId,
	}, nil
}

type job struct {
	ID         string
	Name       string
	Status     string
	Data       string
	CreatedAt  int64
	UpdatedAt  int64
	FacilityID string
}

type Job struct {
	ID     string
	Name   string
	Status string
	Date   time.Time
	Data   JobData
}
type JobData struct {
	FacilityId string `json:"facility_id"`
}

func initDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("postgres", path)
	if err != nil {
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		return nil, err
	}

	return db, createSchema(db)
}

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			facility_id TEXT NOT NULL,
			status TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at INT NOT NULL,
			updated_at INT NOT NULL
		)
	`)

	return err
}
