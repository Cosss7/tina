package project

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
create table if not exists project (id text primary key, name text not null, body text not null);
create table if not exists agent (
 id text primary key, project_id text not null references project(id), role text not null,
 body text not null);
create index if not exists agent_project_role on agent(project_id,role);
create table if not exists task (
 id text primary key, project_id text not null references project(id), body text not null);
create index if not exists task_project on task(project_id);
create table if not exists run (
 id text primary key, project_id text not null references project(id), task_id text not null references task(id),
 state text not null, body text not null);
create index if not exists run_task on run(task_id);
create unique index if not exists one_active_run on run(task_id) where state in ('queued','dispatched','running');
create table if not exists report (
 id text primary key, run_id text not null unique references run(id), body text not null);
create table if not exists approval (
 id text primary key, project_id text not null references project(id), task_id text not null references task(id),
 kind text not null, state text not null, body text not null);
create unique index if not exists one_pending_plan on approval(task_id) where kind='plan' and state='pending';
create table if not exists event (
 id text primary key, project_id text not null references project(id), seq integer not null,
 body text not null, unique(project_id,seq));
create table if not exists execution_request (
 command_id text primary key, run_id text not null unique references run(id),
 project_id text not null references project(id), state text not null, body text not null);
create table if not exists node_identity (id text primary key,name text not null,token_hash blob not null,registration text);
create table if not exists execution_fact (
 node_id text not null, message_id text not null, run_id text,instance_id text,seq integer not null default 0,frame blob not null,
 primary key(node_id,message_id));
create unique index if not exists execution_sequence on execution_fact(node_id,instance_id,seq) where seq>0;
create table if not exists cancel_request(id text primary key,run_id text not null unique references run(id),state text not null,body text not null);
create table if not exists migration (fingerprint text primary key,body text not null);
`

type Service struct {
	db       *sql.DB
	filename string
}

func Open(filename string) (*Service, error) {
	if filename == "" {
		return nil, fmt.Errorf("database path is required")
	}
	dsn := ":memory:?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if filename != ":memory:" {
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
		// Reject H0 databases rather than adding a second writable schema to them.
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
		if len(u.Path) > 1 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		u.RawQuery = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
		dsn = u.String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Service{db: db, filename: filename}
	fail := func(err error) (*Service, error) { db.Close(); return nil, err }
	var format int
	if err := db.QueryRow("pragma user_version").Scan(&format); err != nil {
		return fail(err)
	}
	if format != 0 && format != 1 {
		return fail(fmt.Errorf("unsupported project database version %d", format))
	}
	const applicationID = 0x54494e41 // TINA
	var application int
	if err := db.QueryRow("pragma application_id").Scan(&application); err != nil {
		return fail(err)
	}
	if (format == 1 && application != applicationID) || (format == 0 && application != 0) {
		return fail(fmt.Errorf("database does not belong to the Tina control plane; preserve it and use a new path"))
	}
	if format == 0 {
		var tables int
		if err := db.QueryRow("select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'").Scan(&tables); err != nil {
			return fail(err)
		}
		if tables != 0 {
			return fail(fmt.Errorf("unrecognized database (possibly H0); use a new control-plane path and preserve the old file for migration"))
		}
	}
	if _, err := db.Exec("pragma journal_mode=WAL"); err != nil {
		return fail(err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec("pragma user_version=1"); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec("pragma application_id=1414090305"); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Service) Close() error { return s.db.Close() }
func id(prefix string) string   { return prefix + "_" + rand.Text() }
func now() string               { return time.Now().UTC().Format(time.RFC3339Nano) }

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func one[T any](ctx context.Context, q queryer, query string, args ...any) (T, error) {
	var value T
	var body []byte
	if err := q.QueryRowContext(ctx, query, args...).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = problem("not_found", "Record not found")
		}
		return value, err
	}
	err := json.Unmarshal(body, &value)
	return value, err
}

func many[T any](ctx context.Context, q queryer, query string, args ...any) ([]T, error) {
	values := []T{}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var value T
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// save binds relational fields first and the JSON record as the final parameter.
func save(ctx context.Context, tx *sql.Tx, query string, value any, args ...any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, query, append(args, string(body))...)
	return err
}

func (s *Service) change(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func emit(ctx context.Context, tx *sql.Tx, projectID, kind, actor string, payload any) error {
	var seq int64
	if err := tx.QueryRowContext(ctx, "select coalesce(max(seq),0)+1 from event where project_id=?", projectID).Scan(&seq); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := Event{ID: id("event"), ProjectID: projectID, Seq: seq, Type: kind, Actor: actor, Payload: body, CreatedAt: now()}
	return save(ctx, tx, "insert into event(id,project_id,seq,body) values(?,?,?,?)", e, e.ID, projectID, seq)
}

func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	return many[Project](ctx, s.db, "select body from project order by rowid")
}
func (s *Service) Agents(ctx context.Context, projectID string) ([]Agent, error) {
	if _, err := one[Project](ctx, s.db, "select body from project where id=?", projectID); err != nil {
		return nil, err
	}
	return many[Agent](ctx, s.db, "select body from agent where project_id=? order by rowid", projectID)
}
func (s *Service) Events(ctx context.Context, projectID string) ([]Event, error) {
	if _, err := one[Project](ctx, s.db, "select body from project where id=?", projectID); err != nil {
		return nil, err
	}
	return many[Event](ctx, s.db, "select body from event where project_id=? order by seq", projectID)
}
func (s *Service) Queue(ctx context.Context, projectID string) ([]Job, error) {
	if _, err := one[Project](ctx, s.db, "select body from project where id=?", projectID); err != nil {
		return nil, err
	}
	return many[Job](ctx, s.db, "select body from execution_request where project_id=? and state='queued' order by rowid", projectID)
}
