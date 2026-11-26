package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Pride struct {
	ID           uint32
	Name         string
	Master       string
	MasterUserID int
	RankPoints   int
	MemberCount  int
	Intro        string
	Notice       string
	CloseStatus  byte
	CloseAt      *time.Time
	Wins         int
	Losses       int
	CreatedAt    time.Time
}

type PridePage struct {
	Prides     []Pride
	Total      int
	TotalPages int
}

type PrideMember struct {
	UserID       int
	Username     string
	Callsign     string
	Role         byte
	Level        int
	GP           int
	Kills        int
	Deaths       int
	Wins         int
	Losses       int
	Draws        int
	Contribution int
	JoinedAt     time.Time
	LastLogin    time.Time
}

type PrideApplication struct {
	PrideMember
	AppliedAt time.Time
}

type PrideMemo struct {
	ID        uint32
	Author    string
	Message   string
	CreatedAt time.Time
}

type PrideBattleMemberEntry struct {
	UserID int
	Kills  int
	Deaths int
	Score  int
	IsAce  bool
}

type PrideBattleEntry struct {
	ID              uint64
	PrideID         uint32
	OpponentPrideID uint32
	OpponentName    string
	OpponentMaster  string
	Won             bool
	Mode            int
	Map             int
	ScoreA          int
	ScoreB          int
	EarnPoints      int
	PlayedAt        time.Time
	AceCallsign     string // this side's ace (hero) of the battle
}

type PrideBattleSummary struct {
	PrideID     uint32
	Wins        int
	Losses      int
	RankPoints  int
	WinRate     int
	CurrentRank int
	PeakRank    int
}

type PrideClosure struct {
	PrideID       uint32
	Name          string
	MemberUserIDs []int
}

// PrideChatStore is the narrow membership view needed to route private Pride
// chat. Keeping it separate lets game-server backends support chat without
// implementing every Pride administration operation.
type PrideChatStore interface {
	PrideForUser(context.Context, int) (Pride, PrideMember, bool, error)
	PrideMembers(context.Context, uint32) ([]PrideMember, error)
}

type PrideStore interface {
	PrideForUser(context.Context, int) (Pride, PrideMember, bool, error)
	CreatePride(context.Context, int, string, string) (Pride, PrideMember, bool, error)
	PrideList(context.Context, string, string, int, int) (PridePage, error)
	PrideByID(context.Context, uint32) (Pride, bool, error)
	ApplyPride(context.Context, int, uint32) (Pride, bool, error)
	CancelPrideApplication(context.Context, int, uint32) (Pride, bool, error)
	PrideApplications(context.Context, int) ([]PrideApplication, error)
	AcceptPrideApplication(context.Context, int, string) (Pride, PrideMember, bool, error)
	RejectPrideApplication(context.Context, int, string) (Pride, PrideMember, bool, error)
	LeavePride(context.Context, int) (Pride, PrideMember, bool, error)
	PrideMembers(context.Context, uint32) ([]PrideMember, error)
	KickPrideMember(context.Context, int, string) (Pride, PrideMember, bool, error)
	ChangePrideMaster(context.Context, int, string) (Pride, PrideMember, bool, error)
	SchedulePrideClose(context.Context, int) (Pride, bool, error)
	CancelPrideClose(context.Context, int) (Pride, bool, error)
	CompletePrideClosures(context.Context, time.Time) ([]PrideClosure, error)
	PrideMemos(context.Context, uint32) ([]PrideMemo, error)
	AddPrideMemo(context.Context, int, uint32, string) ([]PrideMemo, bool, error)
	DeletePrideMemo(context.Context, int, uint32, uint32) (bool, error)
	SetPrideMessages(context.Context, int, string, string) (Pride, bool, error)
	PrideRanks(context.Context, int) ([]Pride, error)
	PrideBattleResult(context.Context, uint32) (PrideBattleSummary, error)
	PrideBattleHistory(context.Context, uint32, int, bool) ([]PrideBattleEntry, int, error)
	RegisterPrideMatch(context.Context, []int, []int) error
	SavePrideBattle(context.Context, uint32, uint32, int, int, bool, int, int, int, []PrideBattleMemberEntry) error
	PrideMemberUsernames(context.Context, int) ([]string, error)
}

func (s *PostgresStore) PrideForUser(ctx context.Context, userID int) (Pride, PrideMember, bool, error) {
	var pride Pride
	var member PrideMember
	if userID < 1 {
		return pride, member, false, nil
	}
	var closeStatus int
	err := s.pool.QueryRow(ctx, `
		SELECT p.id, p.name, master.callsign, p.master_user_id, p.rank_points, p.intro, p.notice,
		       p.close_status, p.close_at, p.wins, p.losses, p.created_at,
		       u.id, u.username, u.callsign, pm.role, u.level, u.gp, u.kills, u.deaths,
		       u.wins, u.losses, u.draws, pm.contribution, pm.joined_at, COALESCE(u.last_login, pm.joined_at),
		       (SELECT count(*) FROM pride_members count_member WHERE count_member.pride_id = p.id)
		FROM pride_members pm
		JOIN prides p ON p.id = pm.pride_id
		JOIN users u ON u.id = pm.user_id
		JOIN users master ON master.id = p.master_user_id
		WHERE pm.user_id = $1`, userID).Scan(
		&pride.ID, &pride.Name, &pride.Master, &pride.MasterUserID, &pride.RankPoints, &pride.Intro, &pride.Notice,
		&closeStatus, &pride.CloseAt, &pride.Wins, &pride.Losses, &pride.CreatedAt,
		&member.UserID, &member.Username, &member.Callsign, &member.Role, &member.Level,
		&member.GP, &member.Kills, &member.Deaths, &member.Wins, &member.Losses, &member.Draws,
		&member.Contribution, &member.JoinedAt, &member.LastLogin, &pride.MemberCount)
	if noRows(err) {
		return pride, member, false, nil
	}
	if err != nil {
		return pride, member, false, fmt.Errorf("load user's pride: %w", err)
	}
	pride.CloseStatus = byte(closeStatus)
	return pride, member, true, nil
}

func (s *PostgresStore) CreatePride(ctx context.Context, userID int, name, intro string) (Pride, PrideMember, bool, error) {
	name = strings.TrimSpace(name)
	intro = strings.TrimSpace(intro)
	var pride Pride
	var member PrideMember
	if userID < 1 || name == "" || len([]rune(name)) > 16 || len([]rune(intro)) > 127 {
		return pride, member, false, nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return pride, member, false, fmt.Errorf("begin pride creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var unavailable bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pride_members WHERE user_id = $1)
		    OR EXISTS (SELECT 1 FROM prides WHERE lower(name) = lower($2))`, userID, name).Scan(&unavailable); err != nil {
		return pride, member, false, fmt.Errorf("check pride creation: %w", err)
	}
	if unavailable {
		return pride, member, false, nil
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO prides (name, master_user_id, intro)
		VALUES ($1, $2, $3) RETURNING id, created_at`, name, userID, intro).Scan(&pride.ID, &pride.CreatedAt); err != nil {
		return pride, member, false, fmt.Errorf("create pride: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pride_members (pride_id, user_id, role) VALUES ($1, $2, 8)`, pride.ID, userID); err != nil {
		return Pride{}, PrideMember{}, false, fmt.Errorf("create pride master: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT username, callsign, level, gp, kills, deaths, wins, losses, draws, COALESCE(last_login, CURRENT_TIMESTAMP)
		FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(
		&member.Username, &member.Callsign, &member.Level, &member.GP, &member.Kills, &member.Deaths,
		&member.Wins, &member.Losses, &member.Draws, &member.LastLogin); err != nil {
		return Pride{}, PrideMember{}, false, fmt.Errorf("load pride master: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Pride{}, PrideMember{}, false, fmt.Errorf("commit pride creation: %w", err)
	}
	pride.Name, pride.Master, pride.MasterUserID, pride.Intro, pride.MemberCount = name, member.Callsign, userID, intro, 1
	member.UserID, member.Role, member.JoinedAt = userID, 8, pride.CreatedAt
	return pride, member, true, nil
}

func (s *PostgresStore) PrideList(ctx context.Context, name, master string, page, pageSize int) (PridePage, error) {
	result := PridePage{}
	if page < 0 || pageSize < 1 || pageSize > 255 {
		return result, nil
	}
	name, master = strings.TrimSpace(name), strings.TrimSpace(master)
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM prides p JOIN users master_user ON master_user.id = p.master_user_id
		WHERE ($1 = '' OR p.name ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR master_user.callsign ILIKE '%' || $2 || '%')`, name, master).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("count prides: %w", err)
	}
	if result.Total > 0 {
		result.TotalPages = (result.Total + pageSize - 1) / pageSize
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, master_user.callsign, p.master_user_id, p.rank_points, p.intro, p.notice,
		       p.close_status, p.close_at, p.wins, p.losses, p.created_at,
		       (SELECT count(*) FROM pride_members pm WHERE pm.pride_id = p.id)
		FROM prides p JOIN users master_user ON master_user.id = p.master_user_id
		WHERE ($1 = '' OR p.name ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR master_user.callsign ILIKE '%' || $2 || '%')
		ORDER BY p.rank_points DESC, p.id
		LIMIT $3 OFFSET $4`, name, master, pageSize, page*pageSize)
	if err != nil {
		return PridePage{}, fmt.Errorf("list prides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pride Pride
		var closeStatus int
		if err := rows.Scan(&pride.ID, &pride.Name, &pride.Master, &pride.MasterUserID, &pride.RankPoints, &pride.Intro, &pride.Notice,
			&closeStatus, &pride.CloseAt, &pride.Wins, &pride.Losses, &pride.CreatedAt, &pride.MemberCount); err != nil {
			return PridePage{}, fmt.Errorf("scan pride: %w", err)
		}
		pride.CloseStatus = byte(closeStatus)
		result.Prides = append(result.Prides, pride)
	}
	return result, rows.Err()
}

func (s *PostgresStore) PrideByID(ctx context.Context, prideID uint32) (Pride, bool, error) {
	var pride Pride
	if prideID == 0 {
		return pride, false, nil
	}
	var closeStatus int
	err := s.pool.QueryRow(ctx, `
		SELECT p.id, p.name, u.callsign, p.master_user_id, p.rank_points, p.intro, p.notice, p.close_status,
		       p.close_at, p.wins, p.losses, p.created_at,
		       (SELECT count(*) FROM pride_members pm WHERE pm.pride_id = p.id)
		FROM prides p JOIN users u ON u.id = p.master_user_id WHERE p.id = $1`, prideID).Scan(
		&pride.ID, &pride.Name, &pride.Master, &pride.MasterUserID, &pride.RankPoints, &pride.Intro, &pride.Notice,
		&closeStatus, &pride.CloseAt, &pride.Wins, &pride.Losses, &pride.CreatedAt, &pride.MemberCount)
	if noRows(err) {
		return pride, false, nil
	}
	if err != nil {
		return pride, false, fmt.Errorf("load pride: %w", err)
	}
	pride.CloseStatus = byte(closeStatus)
	return pride, true, nil
}

func (s *PostgresStore) ApplyPride(ctx context.Context, userID int, prideID uint32) (Pride, bool, error) {
	pride, found, err := s.PrideByID(ctx, prideID)
	if err != nil || !found || userID < 1 || pride.CloseStatus != 0 {
		return pride, false, err
	}
	command, err := s.pool.Exec(ctx, `
		INSERT INTO pride_applications (pride_id, user_id)
		SELECT $1, $2 WHERE NOT EXISTS (SELECT 1 FROM pride_members WHERE user_id = $2)
		ON CONFLICT DO NOTHING`, prideID, userID)
	if err != nil {
		return pride, false, fmt.Errorf("apply to pride: %w", err)
	}
	return pride, command.RowsAffected() == 1, nil
}

func (s *PostgresStore) CancelPrideApplication(ctx context.Context, userID int, prideID uint32) (Pride, bool, error) {
	pride, _, err := s.PrideByID(ctx, prideID)
	if err != nil {
		return pride, false, err
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM pride_applications WHERE pride_id = $1 AND user_id = $2`, prideID, userID)
	if err != nil {
		return pride, false, fmt.Errorf("cancel pride application: %w", err)
	}
	return pride, command.RowsAffected() == 1, nil
}

func (s *PostgresStore) PrideApplications(ctx context.Context, masterUserID int) ([]PrideApplication, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.username, u.callsign, u.level, u.gp, u.kills, u.deaths, u.wins, u.losses, u.draws,
		       COALESCE(u.last_login, pa.applied_at), pa.applied_at
		FROM pride_applications pa
		JOIN prides p ON p.id = pa.pride_id
		JOIN users u ON u.id = pa.user_id
		WHERE p.master_user_id = $1 ORDER BY pa.applied_at`, masterUserID)
	if err != nil {
		return nil, fmt.Errorf("list pride applications: %w", err)
	}
	defer rows.Close()
	var result []PrideApplication
	for rows.Next() {
		var app PrideApplication
		if err := rows.Scan(&app.UserID, &app.Username, &app.Callsign, &app.Level, &app.GP, &app.Kills,
			&app.Deaths, &app.Wins, &app.Losses, &app.Draws, &app.LastLogin, &app.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan pride application: %w", err)
		}
		result = append(result, app)
	}
	return result, rows.Err()
}

func (s *PostgresStore) AcceptPrideApplication(ctx context.Context, masterUserID int, username string) (Pride, PrideMember, bool, error) {
	return s.changePrideApplicant(ctx, masterUserID, username, true)
}

func (s *PostgresStore) RejectPrideApplication(ctx context.Context, masterUserID int, username string) (Pride, PrideMember, bool, error) {
	return s.changePrideApplicant(ctx, masterUserID, username, false)
}

func (s *PostgresStore) changePrideApplicant(ctx context.Context, masterUserID int, username string, accept bool) (Pride, PrideMember, bool, error) {
	var pride Pride
	var member PrideMember
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return pride, member, false, fmt.Errorf("begin pride application decision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var prideID uint32
	err = tx.QueryRow(ctx, `
		SELECT p.id, p.name, master.callsign, applicant.id, applicant.username, applicant.callsign,
		       applicant.level, applicant.gp, applicant.kills, applicant.deaths,
		       applicant.wins, applicant.losses, applicant.draws, COALESCE(applicant.last_login, pa.applied_at)
		FROM prides p JOIN users master ON master.id = p.master_user_id
		JOIN pride_applications pa ON pa.pride_id = p.id
		JOIN users applicant ON applicant.id = pa.user_id
		WHERE p.master_user_id = $1 AND lower(applicant.username) = lower($2)
		FOR UPDATE OF p, pa`, masterUserID, username).Scan(
		&prideID, &pride.Name, &pride.Master, &member.UserID, &member.Username, &member.Callsign,
		&member.Level, &member.GP, &member.Kills, &member.Deaths, &member.Wins, &member.Losses, &member.Draws,
		&member.LastLogin)
	if noRows(err) {
		return pride, member, false, nil
	}
	if err != nil {
		return pride, member, false, fmt.Errorf("lock pride application: %w", err)
	}
	if accept {
		command, err := tx.Exec(ctx, `INSERT INTO pride_members (pride_id, user_id, role) VALUES ($1, $2, 0) ON CONFLICT DO NOTHING`, prideID, member.UserID)
		if err != nil {
			return pride, member, false, fmt.Errorf("accept pride member: %w", err)
		}
		if command.RowsAffected() != 1 {
			// pride_members.user_id is globally unique. A concurrent or earlier
			// membership in another Pride means the applicant was not accepted;
			// do not report success when ON CONFLICT skipped the insert.
			return pride, member, false, nil
		}
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pride_members WHERE pride_id = $1`, prideID).Scan(&pride.MemberCount); err != nil {
		return pride, member, false, fmt.Errorf("count pride members after application decision: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pride_applications WHERE pride_id = $1 AND user_id = $2`, prideID, member.UserID); err != nil {
		return pride, member, false, fmt.Errorf("remove pride application: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return pride, member, false, fmt.Errorf("commit pride application: %w", err)
	}
	pride.ID, pride.MasterUserID, member.Role, member.JoinedAt = prideID, masterUserID, 0, s.now().UTC()
	return pride, member, true, nil
}

func (s *PostgresStore) LeavePride(ctx context.Context, userID int) (Pride, PrideMember, bool, error) {
	pride, member, found, err := s.PrideForUser(ctx, userID)
	if err != nil || !found {
		return pride, member, false, err
	}
	if member.Role == 8 && pride.MemberCount > 1 {
		return pride, member, false, nil
	}
	if member.Role == 8 {
		_, err = s.pool.Exec(ctx, `DELETE FROM prides WHERE id = $1 AND master_user_id = $2`, pride.ID, userID)
	} else {
		_, err = s.pool.Exec(ctx, `DELETE FROM pride_members WHERE pride_id = $1 AND user_id = $2`, pride.ID, userID)
	}
	if err != nil {
		return pride, member, false, fmt.Errorf("leave pride: %w", err)
	}
	if member.Role == 8 {
		pride.MemberCount = 0
	} else if pride.MemberCount > 0 {
		pride.MemberCount--
	}
	return pride, member, true, nil
}

func (s *PostgresStore) PrideMembers(ctx context.Context, prideID uint32) ([]PrideMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.username, u.callsign, pm.role, u.level, u.gp, u.kills, u.deaths,
		       u.wins, u.losses, u.draws, pm.contribution, pm.joined_at, COALESCE(u.last_login, pm.joined_at)
		FROM pride_members pm JOIN users u ON u.id = pm.user_id
		WHERE pm.pride_id = $1 ORDER BY pm.role DESC, pm.joined_at`, prideID)
	if err != nil {
		return nil, fmt.Errorf("list pride members: %w", err)
	}
	defer rows.Close()
	var result []PrideMember
	for rows.Next() {
		var member PrideMember
		if err := rows.Scan(&member.UserID, &member.Username, &member.Callsign, &member.Role,
			&member.Level, &member.GP, &member.Kills, &member.Deaths, &member.Wins, &member.Losses, &member.Draws,
			&member.Contribution, &member.JoinedAt, &member.LastLogin); err != nil {
			return nil, fmt.Errorf("scan pride member: %w", err)
		}
		result = append(result, member)
	}
	return result, rows.Err()
}

func (s *PostgresStore) KickPrideMember(ctx context.Context, masterUserID int, username string) (Pride, PrideMember, bool, error) {
	pride, member, ok, err := s.prideMemberAdminTarget(ctx, masterUserID, username)
	if err != nil || !ok || member.Role == 8 {
		return pride, member, false, err
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM pride_members WHERE pride_id = $1 AND user_id = $2`, pride.ID, member.UserID)
	if err != nil {
		return pride, member, false, fmt.Errorf("kick pride member: %w", err)
	}
	success := command.RowsAffected() == 1
	if success && pride.MemberCount > 0 {
		pride.MemberCount--
	}
	return pride, member, success, nil
}

func (s *PostgresStore) ChangePrideMaster(ctx context.Context, masterUserID int, username string) (Pride, PrideMember, bool, error) {
	pride, member, ok, err := s.prideMemberAdminTarget(ctx, masterUserID, username)
	if err != nil || !ok || member.UserID == masterUserID {
		return pride, member, false, err
	}
	err = s.withSerializable(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pride_members SET role = 0 WHERE pride_id = $1 AND user_id = $2`, pride.ID, masterUserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pride_members SET role = 8 WHERE pride_id = $1 AND user_id = $2`, pride.ID, member.UserID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE prides SET master_user_id = $1 WHERE id = $2 AND master_user_id = $3`, member.UserID, pride.ID, masterUserID)
		return err
	})
	if err != nil {
		return pride, member, false, fmt.Errorf("change pride master: %w", err)
	}
	pride.Master, pride.MasterUserID, member.Role = member.Callsign, member.UserID, 8
	return pride, member, true, nil
}

func (s *PostgresStore) prideMemberAdminTarget(ctx context.Context, masterUserID int, username string) (Pride, PrideMember, bool, error) {
	var pride Pride
	var member PrideMember
	err := s.pool.QueryRow(ctx, `
		SELECT p.id, p.name, master.callsign, p.master_user_id,
		       (SELECT count(*) FROM pride_members count_member WHERE count_member.pride_id = p.id),
		       u.id, u.username, u.callsign, pm.role,
		       u.level, u.gp, u.kills, u.deaths, u.wins, u.losses, u.draws, pm.contribution,
		       pm.joined_at, COALESCE(u.last_login, pm.joined_at)
		FROM prides p JOIN users master ON master.id = p.master_user_id
		JOIN pride_members pm ON pm.pride_id = p.id
		JOIN users u ON u.id = pm.user_id
		WHERE p.master_user_id = $1 AND lower(u.username) = lower($2)`, masterUserID, username).Scan(
		&pride.ID, &pride.Name, &pride.Master, &pride.MasterUserID, &pride.MemberCount,
		&member.UserID, &member.Username, &member.Callsign,
		&member.Role, &member.Level, &member.GP, &member.Kills, &member.Deaths, &member.Wins,
		&member.Losses, &member.Draws, &member.Contribution, &member.JoinedAt, &member.LastLogin)
	if noRows(err) {
		return pride, member, false, nil
	}
	if err != nil {
		return pride, member, false, fmt.Errorf("load pride administration target: %w", err)
	}
	return pride, member, true, nil
}

func (s *PostgresStore) SchedulePrideClose(ctx context.Context, masterUserID int) (Pride, bool, error) {
	closeAt := s.now().UTC().AddDate(0, 0, 7)
	command, err := s.pool.Exec(ctx, `UPDATE prides SET close_status = 10, close_at = $1 WHERE master_user_id = $2`, closeAt, masterUserID)
	if err != nil {
		return Pride{}, false, fmt.Errorf("schedule pride close: %w", err)
	}
	pride, _, found, loadErr := s.PrideForUser(ctx, masterUserID)
	return pride, command.RowsAffected() == 1 && found, loadErr
}

func (s *PostgresStore) CancelPrideClose(ctx context.Context, masterUserID int) (Pride, bool, error) {
	command, err := s.pool.Exec(ctx, `UPDATE prides SET close_status = 0, close_at = NULL WHERE master_user_id = $1 AND close_status > 0`, masterUserID)
	if err != nil {
		return Pride{}, false, fmt.Errorf("cancel pride close: %w", err)
	}
	pride, _, found, loadErr := s.PrideForUser(ctx, masterUserID)
	return pride, command.RowsAffected() == 1 && found, loadErr
}

func (s *PostgresStore) CompletePrideClosures(ctx context.Context, now time.Time) ([]PrideClosure, error) {
	var closures []PrideClosure
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name FROM prides
			WHERE close_status > 0 AND close_at <= $1
			ORDER BY id FOR UPDATE`, now.UTC())
		if err != nil {
			return err
		}
		for rows.Next() {
			var closure PrideClosure
			if err := rows.Scan(&closure.PrideID, &closure.Name); err != nil {
				rows.Close()
				return err
			}
			closures = append(closures, closure)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for index := range closures {
			if err := tx.QueryRow(ctx, `SELECT array_agg(user_id ORDER BY user_id) FROM pride_members WHERE pride_id = $1`, closures[index].PrideID).
				Scan(&closures[index].MemberUserIDs); err != nil {
				return err
			}
			closure := closures[index]
			if _, err := tx.Exec(ctx, `DELETE FROM prides WHERE id = $1`, closure.PrideID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("complete pride closures: %w", err)
	}
	return closures, nil
}

func (s *PostgresStore) PrideMemos(ctx context.Context, prideID uint32) ([]PrideMemo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT memo.id, u.callsign, memo.message, memo.created_at
		FROM pride_memos memo JOIN users u ON u.id = memo.author_user_id
		WHERE memo.pride_id = $1 ORDER BY memo.created_at DESC, memo.id DESC LIMIT 100`, prideID)
	if err != nil {
		return nil, fmt.Errorf("list pride memos: %w", err)
	}
	defer rows.Close()
	var result []PrideMemo
	for rows.Next() {
		var memo PrideMemo
		if err := rows.Scan(&memo.ID, &memo.Author, &memo.Message, &memo.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pride memo: %w", err)
		}
		result = append(result, memo)
	}
	return result, rows.Err()
}

func (s *PostgresStore) AddPrideMemo(ctx context.Context, userID int, prideID uint32, message string) ([]PrideMemo, bool, error) {
	message = strings.TrimSpace(message)
	if message == "" || len([]rune(message)) > 127 {
		return nil, false, nil
	}
	command, err := s.pool.Exec(ctx, `
		INSERT INTO pride_memos (pride_id, author_user_id, message)
		SELECT $1, $2, $3 WHERE EXISTS (
			SELECT 1 FROM pride_members WHERE pride_id = $1 AND user_id = $2)`, prideID, userID, message)
	if err != nil {
		return nil, false, fmt.Errorf("add pride memo: %w", err)
	}
	memos, err := s.PrideMemos(ctx, prideID)
	return memos, command.RowsAffected() == 1, err
}

func (s *PostgresStore) DeletePrideMemo(ctx context.Context, userID int, prideID, memoID uint32) (bool, error) {
	command, err := s.pool.Exec(ctx, `
		DELETE FROM pride_memos memo USING prides p
		WHERE memo.id = $1 AND memo.pride_id = $2 AND p.id = memo.pride_id
		  AND (memo.author_user_id = $3 OR p.master_user_id = $3)`, memoID, prideID, userID)
	if err != nil {
		return false, fmt.Errorf("delete pride memo: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (s *PostgresStore) SetPrideMessages(ctx context.Context, masterUserID int, intro, notice string) (Pride, bool, error) {
	intro, notice = strings.TrimSpace(intro), strings.TrimSpace(notice)
	if len([]rune(intro)) > 127 || len([]rune(notice)) > 127 {
		return Pride{}, false, nil
	}
	command, err := s.pool.Exec(ctx, `UPDATE prides SET intro = $1, notice = $2 WHERE master_user_id = $3`, intro, notice, masterUserID)
	if err != nil {
		return Pride{}, false, fmt.Errorf("update pride messages: %w", err)
	}
	pride, _, found, loadErr := s.PrideForUser(ctx, masterUserID)
	return pride, command.RowsAffected() == 1 && found, loadErr
}

func (s *PostgresStore) PrideRanks(ctx context.Context, limit int) ([]Pride, error) {
	if limit < 1 || limit > 100 {
		limit = 7
	}
	page, err := s.PrideList(ctx, "", "", 0, limit)
	return page.Prides, err
}

func (s *PostgresStore) PrideBattleResult(ctx context.Context, prideID uint32) (PrideBattleSummary, error) {
	var result PrideBattleSummary
	if prideID == 0 {
		return result, nil
	}
	result.PrideID = prideID
	err := s.pool.QueryRow(ctx, `
		SELECT p.wins, p.losses, p.rank_points,
		       CASE WHEN p.wins + p.losses = 0 THEN 0
		            ELSE round(100.0 * p.wins / (p.wins + p.losses))::integer END,
		       CASE WHEN p.rank_points = 0 THEN 0 ELSE 1 + (
		           SELECT count(*) FROM prides ranked
		           WHERE ranked.rank_points > p.rank_points
		              OR (ranked.rank_points = p.rank_points AND ranked.id < p.id)) END
		FROM prides p WHERE p.id = $1`, prideID).Scan(
		&result.Wins, &result.Losses, &result.RankPoints, &result.WinRate, &result.CurrentRank)
	if noRows(err) {
		return PrideBattleSummary{}, nil
	}
	if err != nil {
		return PrideBattleSummary{}, fmt.Errorf("load pride battle result: %w", err)
	}
	// Peak rank: the best rank recorded after a battle (migration 047).
	var best int
	if err := s.pool.QueryRow(ctx, `SELECT best_rank FROM prides WHERE id = $1`, prideID).Scan(&best); err != nil && !noRows(err) {
		return PrideBattleSummary{}, fmt.Errorf("load pride best rank: %w", err)
	}
	result.PeakRank = result.CurrentRank
	if best > 0 && (result.PeakRank == 0 || best < result.PeakRank) {
		result.PeakRank = best
	}
	return result, nil
}

func (s *PostgresStore) RegisterPrideMatch(ctx context.Context, winners, losers []int) error {
	winnerPride, ok, err := s.singlePrideForUsers(ctx, winners)
	if err != nil || !ok {
		return err
	}
	loserPride, ok, err := s.singlePrideForUsers(ctx, losers)
	if err != nil || !ok || winnerPride == loserPride {
		return err
	}

	err = s.SavePrideBattle(ctx, winnerPride, loserPride, 0, 0, true, 0, 0, 3, nil)
	if err != nil {
		return err
	}

	// Update contribution points for participating members
	// Win = +3 points, Loss = +1 point
	if len(winners) > 0 {
		_, _ = s.pool.Exec(ctx, "UPDATE pride_members SET contribution = contribution + 3 WHERE pride_id = $1 AND user_id = ANY($2)", winnerPride, winners)
	}
	if len(losers) > 0 {
		_, _ = s.pool.Exec(ctx, "UPDATE pride_members SET contribution = contribution + 1 WHERE pride_id = $1 AND user_id = ANY($2)", loserPride, losers)
	}

	return nil
}

func (s *PostgresStore) SavePrideBattle(ctx context.Context, prideID, opponentPrideID uint32, mode, mapID int, won bool, scoreA, scoreB, earnPoints int, members []PrideBattleMemberEntry) error {
	if prideID == 0 || opponentPrideID == 0 || prideID == opponentPrideID {
		return nil
	}
	return s.withSerializable(ctx, func(tx pgx.Tx) error {
		var battleAID, battleBID uint64
		// Winner/loser perspective: prideID wins, opponentPrideID loses
		lossDelta := -earnPoints
		if lossDelta > 0 {
			lossDelta = -1
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO pride_battles (pride_id, opponent_pride_id, won, rank_delta, mode, map, score_a, score_b, earn_points)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			prideID, opponentPrideID, won, earnPoints, mode, mapID, scoreA, scoreB, earnPoints).Scan(&battleAID); err != nil {
			return fmt.Errorf("insert pride battle (winner): %w", err)
		}
		opponentWon := !won
		if err := tx.QueryRow(ctx, `
			INSERT INTO pride_battles (pride_id, opponent_pride_id, won, rank_delta, mode, map, score_a, score_b, earn_points)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			opponentPrideID, prideID, opponentWon, lossDelta, mode, mapID, scoreB, scoreA, lossDelta).Scan(&battleBID); err != nil {
			return fmt.Errorf("insert pride battle (loser): %w", err)
		}
		// Update pride rank points
		if won {
			if _, err := tx.Exec(ctx, `UPDATE prides SET wins = wins + 1, rank_points = rank_points + $2 WHERE id = $1`, prideID, earnPoints); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE prides SET losses = losses + 1, rank_points = GREATEST(0, rank_points + $2) WHERE id = $1`, opponentPrideID, lossDelta); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE prides SET losses = losses + 1 WHERE id = $1`, prideID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE prides SET wins = wins + 1 WHERE id = $1`, opponentPrideID); err != nil {
				return err
			}
		}
		// Insert per-member stats for both battle records
		for _, m := range members {
			if m.UserID < 1 {
				continue
			}
			for _, bid := range []uint64{battleAID, battleBID} {
				if _, err := tx.Exec(ctx, `
					INSERT INTO pride_battle_members (battle_id, user_id, kills, deaths, score, is_ace)
					VALUES ($1, $2, $3, $4, $5, $6)
					ON CONFLICT DO NOTHING`,
					bid, m.UserID, m.Kills, m.Deaths, m.Score, m.IsAce); err != nil {
					return fmt.Errorf("insert pride battle member: %w", err)
				}
			}
		}
		return nil
	})
}

func (s *PostgresStore) PrideBattleHistory(ctx context.Context, prideID uint32, page int, isClassic bool) ([]PrideBattleEntry, int, error) {
	if prideID == 0 {
		return nil, 0, nil
	}
	const pageSize = 10
	offset := page * pageSize
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pride_battles WHERE pride_id = $1`, prideID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count pride battles: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT pb.id, pb.pride_id, pb.opponent_pride_id,
		       opp.name, opp_master.callsign,
		       pb.won, pb.mode, pb.map, pb.score_a, pb.score_b, pb.earn_points, pb.played_at,
		       COALESCE((SELECT u.callsign FROM pride_battle_members m JOIN users u ON u.id = m.user_id
		                 WHERE m.battle_id = pb.id AND m.is_ace LIMIT 1), '')
		FROM pride_battles pb
		JOIN prides opp ON opp.id = pb.opponent_pride_id
		JOIN users opp_master ON opp_master.id = opp.master_user_id
		WHERE pb.pride_id = $1
		ORDER BY pb.played_at DESC, pb.id DESC
		LIMIT $2 OFFSET $3`, prideID, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list pride battles: %w", err)
	}
	defer rows.Close()
	var entries []PrideBattleEntry
	for rows.Next() {
		var e PrideBattleEntry
		if err := rows.Scan(
			&e.ID, &e.PrideID, &e.OpponentPrideID,
			&e.OpponentName, &e.OpponentMaster,
			&e.Won, &e.Mode, &e.Map, &e.ScoreA, &e.ScoreB, &e.EarnPoints, &e.PlayedAt, &e.AceCallsign,
		); err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func (s *PostgresStore) singlePrideForUsers(ctx context.Context, userIDs []int) (uint32, bool, error) {
	if len(userIDs) == 0 {
		return 0, false, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT pride_id FROM pride_members WHERE user_id = ANY($1)`, userIDs)
	if err != nil {
		return 0, false, fmt.Errorf("load team pride: %w", err)
	}
	defer rows.Close()
	var prideID uint32
	count := 0
	for rows.Next() {
		if err := rows.Scan(&prideID); err != nil {
			return 0, false, err
		}
		count++
	}
	return prideID, count == 1, rows.Err()
}

func (s *PostgresStore) PrideMemberUsernames(ctx context.Context, userID int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT member_user.username FROM pride_members self_member
		JOIN pride_members member ON member.pride_id = self_member.pride_id
		JOIN users member_user ON member_user.id = member.user_id
		WHERE self_member.user_id = $1 ORDER BY member.role DESC, member.joined_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list pride member usernames: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return nil, err
		}
		result = append(result, username)
	}
	return result, rows.Err()
}

var _ PrideStore = (*PostgresStore)(nil)
