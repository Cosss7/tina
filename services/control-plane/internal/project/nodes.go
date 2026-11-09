package project

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	p "github.com/Cosss7/tina/packages/protocol"
)

func (s *Service) EnrollNode(ctx context.Context, nodeID, name, token string) error {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(name) == "" || len(token) < 32 {
		return problem("invalid_argument", "nodeId, name and a token of at least 32 characters are required")
	}
	digest := sha256.Sum256([]byte(token))
	return s.change(ctx, func(tx *sql.Tx) error {
		var existing []byte
		err := tx.QueryRowContext(ctx, "select token_hash from node_identity where id=?", nodeID).Scan(&existing)
		if err == nil {
			if subtle.ConstantTimeCompare(existing, digest[:]) != 1 {
				return problem("conflict", "Node identity is already enrolled with another token")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "insert into node_identity(id,name,token_hash) values(?,?,?)", nodeID, name, digest[:])
		return err
	})
}
func (s *Service) AuthenticateNode(ctx context.Context, nodeID, token string) bool {
	var expected []byte
	if err := s.db.QueryRowContext(ctx, "select token_hash from node_identity where id=?", nodeID).Scan(&expected); err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(expected, digest[:]) == 1
}
func (s *Service) RegisterNode(ctx context.Context, registration p.Register) error {
	if err := registration.Validate(); err != nil {
		return err
	}
	return s.change(ctx, func(tx *sql.Tx) error {
		for _, local := range registration.Agents {
			agent, err := one[Agent](ctx, tx, "select body from agent where id=? and project_id=?", local.ID, local.ProjectID)
			if err != nil {
				return err
			}
			if agent.NodeID != registration.NodeID || agent.Role != local.Role || agent.EngineID != local.EngineID {
				return problem("conflict", "Local registration does not match the control-plane Agent binding")
			}
		}
		body, err := json.Marshal(registration)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "update node_identity set name=?,registration=? where id=?", registration.Name, string(body), registration.NodeID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return problem("not_found", "Node is not enrolled")
		}
		return nil
	})
}
func (s *Service) Registrations(ctx context.Context) ([]p.Register, error) {
	return many[p.Register](ctx, s.db, "select registration from node_identity where registration is not null order by rowid")
}

func (s *Service) SetRouterMemory(ctx context.Context, projectID, text, actor string) (Project, error) {
	var project Project
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		project, err = one[Project](ctx, tx, "select body from project where id=?", projectID)
		if err != nil {
			return err
		}
		project.RouterMemory = text
		if err := save(ctx, tx, "update project set body=?2 where id=?1", project, projectID); err != nil {
			return err
		}
		return emit(ctx, tx, projectID, "router.memory.updated", actor, map[string]any{"projectId": projectID})
	})
	return project, err
}
