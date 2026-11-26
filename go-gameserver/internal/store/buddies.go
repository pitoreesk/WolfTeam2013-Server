package store

import (
	"context"
	"fmt"
	"strings"
)

// BuddyAccount identifies the authenticated owner of a buddy connection.
type BuddyAccount struct {
	UserID   int
	Username string
	Callsign string
}

// BuddyRelation is one directional entry in an account's persisted buddy list.
// Group membership is stored independently for each side of a friendship.
type BuddyRelation struct {
	Username string
	Callsign string
	Guild    string
	GroupID  uint16
}

// BuddyStore is optional so lightweight server backends do not need social
// persistence. The production PostgreSQL backend implements it.
type BuddyStore interface {
	BuddyAccountByUsername(context.Context, string) (BuddyAccount, bool, error)
	Buddies(context.Context, int) ([]BuddyRelation, error)
	AddBuddy(context.Context, int, string, uint16) (BuddyRelation, bool, error)
	RemoveBuddy(context.Context, int, string) (bool, error)
}

func (s *PostgresStore) BuddyAccountByUsername(ctx context.Context, username string) (BuddyAccount, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return BuddyAccount{}, false, nil
	}

	var account BuddyAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, callsign
		FROM users
		WHERE lower(username) = lower($1)`, username).Scan(
		&account.UserID, &account.Username, &account.Callsign)
	if noRows(err) {
		return BuddyAccount{}, false, nil
	}
	if err != nil {
		return BuddyAccount{}, false, fmt.Errorf("look up buddy account %q: %w", username, err)
	}
	return account, true, nil
}

func (s *PostgresStore) Buddies(ctx context.Context, userID int) ([]BuddyRelation, error) {
	if userID < 1 {
		return nil, fmt.Errorf("invalid buddy owner user ID %d", userID)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT buddy.username, buddy.callsign, COALESCE(pride.name, ''), relation.group_id
		FROM buddy_relations relation
		JOIN users buddy ON buddy.id = relation.buddy_user_id
		LEFT JOIN pride_members membership ON membership.user_id = buddy.id
		LEFT JOIN prides pride ON pride.id = membership.pride_id
		WHERE relation.owner_user_id = $1
		ORDER BY relation.created_at, relation.buddy_user_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list buddies for user %d: %w", userID, err)
	}
	defer rows.Close()

	relations := make([]BuddyRelation, 0, 16)
	for rows.Next() {
		var relation BuddyRelation
		var groupID int
		if err := rows.Scan(&relation.Username, &relation.Callsign, &relation.Guild, &groupID); err != nil {
			return nil, fmt.Errorf("scan buddy for user %d: %w", userID, err)
		}
		if groupID < 0 || groupID > 65535 {
			return nil, fmt.Errorf("buddy %q has invalid group ID %d", relation.Username, groupID)
		}
		relation.GroupID = uint16(groupID)
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate buddies for user %d: %w", userID, err)
	}
	return relations, nil
}

func (s *PostgresStore) AddBuddy(ctx context.Context, userID int, username string, groupID uint16) (BuddyRelation, bool, error) {
	username = strings.TrimSpace(username)
	if userID < 1 || username == "" {
		return BuddyRelation{}, false, nil
	}

	var relation BuddyRelation
	var storedGroupID int
	err := s.pool.QueryRow(ctx, `
		WITH target AS (
			SELECT id FROM users WHERE lower(username) = lower($2) AND id <> $1
		), stored AS (
			INSERT INTO buddy_relations (owner_user_id, buddy_user_id, group_id)
			SELECT $1, id, $3 FROM target
			ON CONFLICT (owner_user_id, buddy_user_id)
			DO UPDATE SET group_id = EXCLUDED.group_id
			RETURNING buddy_user_id, group_id
		)
		SELECT buddy.username, buddy.callsign, COALESCE(pride.name, ''), stored.group_id
		FROM stored
		JOIN users buddy ON buddy.id = stored.buddy_user_id
		LEFT JOIN pride_members membership ON membership.user_id = buddy.id
		LEFT JOIN prides pride ON pride.id = membership.pride_id`, userID, username, int(groupID)).Scan(
		&relation.Username, &relation.Callsign, &relation.Guild, &storedGroupID)
	if noRows(err) {
		return BuddyRelation{}, false, nil
	}
	if err != nil {
		return BuddyRelation{}, false, fmt.Errorf("add buddy %q for user %d: %w", username, userID, err)
	}
	relation.GroupID = uint16(storedGroupID)
	return relation, true, nil
}

func (s *PostgresStore) RemoveBuddy(ctx context.Context, userID int, username string) (bool, error) {
	username = strings.TrimSpace(username)
	if userID < 1 || username == "" {
		return false, nil
	}
	command, err := s.pool.Exec(ctx, `
		DELETE FROM buddy_relations relation
		USING users buddy
		WHERE relation.owner_user_id = $1
		  AND relation.buddy_user_id = buddy.id
		  AND lower(buddy.username) = lower($2)`, userID, username)
	if err != nil {
		return false, fmt.Errorf("remove buddy %q for user %d: %w", username, userID, err)
	}
	return command.RowsAffected() > 0, nil
}
