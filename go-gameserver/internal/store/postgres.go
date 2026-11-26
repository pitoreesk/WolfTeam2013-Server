package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wtserver-go/internal/lootbox"
)

type Backend interface {
	GameRepository
	ChannelSource
}

type PostgresStore struct {
	pool       *pgxpool.Pool
	now        func() time.Time
	luckyBoxes *lootbox.Catalog
}

func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL URL: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &PostgresStore{pool: pool, now: time.Now}, nil
}

func (s *PostgresStore) Close() { s.pool.Close() }

// ConfigureLuckyBoxCatalog installs the immutable reward table before the
// server begins accepting lucky-box requests.
func (s *PostgresStore) ConfigureLuckyBoxCatalog(catalog *lootbox.Catalog) {
	s.luckyBoxes = catalog
}

func (s *PostgresStore) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, port, is_open, channel_type, f1, f2, f3, f4
		FROM channels
		WHERE enabled
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	defer rows.Close()
	channels := make([]Channel, 0, 4)
	for rows.Next() {
		var channel Channel
		var port int
		var channelType int
		if err := rows.Scan(
			&channel.Name, &port, &channel.Open, &channelType,
			&channel.ServerKills, &channel.ServerDeaths,
			&channel.GPBonusPercent, &channel.GoldBonusPercent,
		); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("channel %q has invalid port %d", channel.Name, port)
		}
		channel.Type = ChannelType(channelType)
		if !channel.Type.Valid() {
			return nil, fmt.Errorf("channel %q has invalid type %d", channel.Name, channelType)
		}
		channel.Port = uint16(port)
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	return channels, nil
}

func (s *PostgresStore) withSerializable(ctx context.Context, operation func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, operation)
}

func withSerializableResult[T any](ctx context.Context, s *PostgresStore, name string, operation func(pgx.Tx) (T, error)) (result T, err error) {
	err = s.withSerializable(ctx, func(tx pgx.Tx) error {
		result, err = operation(tx)
		return err
	})
	if err != nil {
		return result, fmt.Errorf("%s transaction: %w", name, err)
	}
	return result, nil
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
