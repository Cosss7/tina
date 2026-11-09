package runtime

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	p "github.com/Cosss7/tina/packages/protocol"
	_ "modernc.org/sqlite"
)

type record struct {
	Kind            string          `json:"kind,omitempty"`
	Stage           string          `json:"stage,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	CommandID       string          `json:"commandId"`
	Execution       p.Execution     `json:"execution"`
	Fingerprint     string          `json:"fingerprint"`
	Receipt         p.Receipt       `json:"receipt"`
	LastSeq         uint64          `json:"lastSeq"`
	Started         bool            `json:"started"`
	Result          *p.Result       `json:"result,omitempty"`
	Cleanup         *p.StopEvidence `json:"cleanup,omitempty"`
}
type journal struct {
	mu sync.Mutex
	db *sql.DB
}

func openJournal(filename string) (*journal, error) {
	dsn := filename + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if filename != ":memory:" {
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}
		if len(u.Path) > 1 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		dsn = u.String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var application, version, tables int
	if err := db.QueryRow("pragma application_id").Scan(&application); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow("pragma user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow("select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'").Scan(&tables); err != nil {
		db.Close()
		return nil, err
	}
	if !((application == 0 && version == 0 && tables == 0) || (application == 0x54494e4a && version == 1)) {
		db.Close()
		return nil, fmt.Errorf("database does not belong to tinad")
	}
	if _, err := db.Exec("pragma journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := tx.Exec(`
create table if not exists command (id text primary key,body text not null);
create table if not exists outbox (id text primary key,frame blob not null);
pragma application_id=1414090314;
pragma user_version=1;`); err != nil {
		tx.Rollback()
		db.Close()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	return &journal{db: db}, nil
}
func (j *journal) get(commandID string) (record, bool, error) {
	var r record
	var body []byte
	err := j.db.QueryRow("select body from command where id=?", commandID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	err = json.Unmarshal(body, &r)
	return r, true, err
}
func (j *journal) all() ([]record, error) {
	rows, err := j.db.Query("select body from command order by rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []record{}
	for rows.Next() {
		var body []byte
		var r record
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (j *journal) put(r record, message p.Message) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	frame, err := p.Encode(newID(), message)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("insert into command(id,body) values(?,?) on conflict(id) do update set body=excluded.body", r.CommandID, body); err != nil {
		return err
	}
	if err := insertFrame(tx, frame); err != nil {
		return err
	}
	return tx.Commit()
}
func insertFrame(tx *sql.Tx, frame []byte) error {
	f, err := p.Decode(frame)
	if err != nil {
		return err
	}
	_, err = tx.Exec("insert into outbox(id,frame) values(?,?)", f.ID, frame)
	return err
}
func (j *journal) enqueue(message p.Message) error {
	frame, err := p.Encode(newID(), message)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertFrame(tx, frame); err != nil {
		return err
	}
	return tx.Commit()
}
func (j *journal) pending() ([][]byte, error) {
	rows, err := j.db.Query("select frame from outbox order by rowid limit 256")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := [][]byte{}
	for rows.Next() {
		var frame []byte
		if err := rows.Scan(&frame); err != nil {
			return nil, err
		}
		result = append(result, frame)
	}
	return result, rows.Err()
}
func (j *journal) ack(messageID string) error {
	_, err := j.db.Exec("delete from outbox where id=?", messageID)
	return err
}
func (j *journal) recover() error {
	records, err := j.all()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Kind == "cancel" && r.Receipt.Admission == p.Accepted {
			continue
		}
		if r.Result != nil || r.Receipt.Admission == p.Rejected {
			continue
		}
		r.Receipt.Admission = p.Unknown
		r.Receipt.Failure = &p.Failure{Code: "runtime_restarted", Message: "prior execution acceptance or stop requires reconciliation; not replayed"}
		if err := j.put(r, r.Receipt); err != nil {
			return fmt.Errorf("recover command: %w", err)
		}
	}
	return nil
}
