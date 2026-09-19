package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/P0me1oo/CDT-Monitor/internal/domain"
)

func (s *Store) GetRotation(ctx context.Context) (domain.RotationConfig, error) {
	r := domain.RotationConfig{Slots: []domain.RotationSlot{}}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='rotation_config'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	if r.Slots == nil {
		r.Slots = []domain.RotationSlot{}
	}
	r.Token, err = s.Decrypt(r.Token)
	r.TokenConfigured = r.Token != ""
	return r, err
}

func (s *Store) SaveRotation(ctx context.Context, r domain.RotationConfig) error {
	old, err := s.GetRotation(ctx)
	if err != nil {
		return err
	}
	if r.Token == "" {
		r.Token = old.Token
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if err = r.Validate(accounts); err != nil {
		return err
	}
	state, err := s.GetRotationState(ctx)
	if err != nil {
		return err
	}
	if old.Hostname != r.Hostname {
		state.ActiveID = 0
		state.IP = ""
	}
	state.Message = "配置已保存，等待调度"
	state.Error = ""
	r.Token, err = s.Encrypt(r.Token)
	if err != nil {
		return err
	}
	r.TokenConfigured = false
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = putSettingTx(ctx, tx, "rotation_config", string(raw)); err != nil {
			return err
		}
		// 保留尚未完成的 DNS 缓存交接时间，停用再启用不会缩短等待窗口。
		return putRotationState(ctx, tx, state)
	})
}

func (s *Store) GetRotationState(ctx context.Context) (domain.RotationState, error) {
	var state domain.RotationState
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='rotation_state'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	return state, err
}

func putRotationState(ctx context.Context, tx *sql.Tx, state domain.RotationState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return putSettingTx(ctx, tx, "rotation_state", string(raw))
}

func (s *Store) SaveRotationState(ctx context.Context, state domain.RotationState) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return putRotationState(ctx, tx, state) })
}

func (s *Store) ReleaseLease(ctx context.Context, name, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM scheduler_leases WHERE name=? AND owner=?`, name, owner)
	return err
}
