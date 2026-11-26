package store

import (
	"context"
	"fmt"
	"strings"
)

const maxBuddyMailboxBodySize = 51932

// BuddyMailboxMessage is a server-relayed P2P message waiting for an offline
// account. Sender details are resolved at delivery time.
type BuddyMailboxMessage struct {
	ID        int64
	Sender    BuddyAccount
	MessageID uint16
	Body      []byte
}

// BuddyMailboxStore is optional. Servers backed by it can preserve P2P
// messages for offline users and deliver them after their next Buddy login.
type BuddyMailboxStore interface {
	QueueBuddyMessage(context.Context, int, string, uint16, []byte) (int64, bool, error)
	PendingBuddyMessages(context.Context, int) ([]BuddyMailboxMessage, error)
	DeleteBuddyMessage(context.Context, int, int64) error
}

func (s *PostgresStore) QueueBuddyMessage(ctx context.Context, senderUserID int, targetUsername string, messageID uint16, body []byte) (int64, bool, error) {
	targetUsername = strings.TrimSpace(targetUsername)
	if senderUserID < 1 || targetUsername == "" || len(body) > maxBuddyMailboxBodySize {
		return 0, false, nil
	}
	var messageRowID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO buddy_mailbox (recipient_user_id, sender_user_id, message_id, body)
		SELECT recipient.id, $1, $3, $4
		FROM users recipient
		WHERE lower(recipient.username) = lower($2)
		  AND recipient.id <> $1
		RETURNING id`, senderUserID, targetUsername, int(messageID), body).Scan(&messageRowID)
	if noRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("queue buddy message for %q: %w", targetUsername, err)
	}
	return messageRowID, true, nil
}

func (s *PostgresStore) PendingBuddyMessages(ctx context.Context, recipientUserID int) ([]BuddyMailboxMessage, error) {
	if recipientUserID < 1 {
		return nil, fmt.Errorf("invalid buddy mailbox recipient user ID %d", recipientUserID)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT message.id, sender.id, sender.username, sender.callsign, message.message_id, message.body
		FROM buddy_mailbox message
		JOIN users sender ON sender.id = message.sender_user_id
		WHERE message.recipient_user_id = $1
		ORDER BY message.id
		LIMIT 100`, recipientUserID)
	if err != nil {
		return nil, fmt.Errorf("list buddy mailbox for user %d: %w", recipientUserID, err)
	}
	defer rows.Close()

	messages := make([]BuddyMailboxMessage, 0, 8)
	for rows.Next() {
		var message BuddyMailboxMessage
		var messageID int
		if err := rows.Scan(&message.ID, &message.Sender.UserID, &message.Sender.Username,
			&message.Sender.Callsign, &messageID, &message.Body); err != nil {
			return nil, fmt.Errorf("scan buddy mailbox for user %d: %w", recipientUserID, err)
		}
		if messageID < 0 || messageID > 65535 || len(message.Body) > maxBuddyMailboxBodySize {
			return nil, fmt.Errorf("buddy mailbox message %d has invalid ID/body size %d/%d",
				message.ID, messageID, len(message.Body))
		}
		message.MessageID = uint16(messageID)
		message.Body = append([]byte(nil), message.Body...)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate buddy mailbox for user %d: %w", recipientUserID, err)
	}
	return messages, nil
}

func (s *PostgresStore) DeleteBuddyMessage(ctx context.Context, recipientUserID int, messageRowID int64) error {
	if recipientUserID < 1 || messageRowID < 1 {
		return fmt.Errorf("invalid buddy mailbox delete recipient/message %d/%d", recipientUserID, messageRowID)
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM buddy_mailbox
		WHERE id = $1 AND recipient_user_id = $2`, messageRowID, recipientUserID); err != nil {
		return fmt.Errorf("delete buddy mailbox message %d for user %d: %w", messageRowID, recipientUserID, err)
	}
	return nil
}
