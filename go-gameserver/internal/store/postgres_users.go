package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) AccountSnapshot(ctx context.Context, userID int) (AccountSnapshot, error) {
	var info AccountSnapshot
	var authority int
	var playTimeSeconds int64
	var muteStartedAt, muteExpiresAt sql.NullTime
	err := s.pool.QueryRow(ctx, `
		SELECT callsign,
		       authority,
		       muted AND (mute_expires_at IS NULL OR mute_expires_at > now()),
		       mute_started_at,
		       mute_expires_at,
		       mute_reason,
		       level, gp, gold, wc, cash, kills, deaths, wins, losses,
		       draws, headshot_kills, special_weapon_kills, wolf_kills,
		       CASE WHEN daily_play_date = current_date THEN daily_play_seconds ELSE 0 END
		FROM users WHERE id = $1`, userID).Scan(
		&info.Callsign, &authority, &info.Muted, &muteStartedAt, &muteExpiresAt, &info.MuteReason,
		&info.Level, &info.GP, &info.Gold,
		&info.WC, &info.Cash, &info.Kills, &info.Deaths, &info.Wins, &info.Losses,
		&info.Draws, &info.HeadshotKills, &info.SpecialWeaponKills, &info.WolfKills, &playTimeSeconds,
	)
	if err != nil {
		return AccountSnapshot{}, fmt.Errorf("load user %d: %w", userID, err)
	}
	if authority < 0 || authority > math.MaxUint8 {
		return AccountSnapshot{}, fmt.Errorf("load user %d: authority %d exceeds uint8 range", userID, authority)
	}
	info.Authority = byte(authority)
	info.PlayTimeSeconds = uint32(playTimeSeconds)
	if info.Muted {
		if muteStartedAt.Valid {
			info.MuteStartedAt = muteStartedAt.Time.UTC()
		}
		if muteExpiresAt.Valid {
			info.MuteExpiresAt = muteExpiresAt.Time.UTC()
		}
	} else {
		info.MuteReason = ""
	}
	return info, nil
}

func (s *PostgresStore) Items(ctx context.Context, userID int) ([]Item, error) {
	// Entitlement tables are authoritative; this projection supplies slot-lock
	// state.
	_ = s.SyncSlotVouchers(ctx, userID)
	if err := s.purgeExpiredInventory(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT item_id, type, upgrade, expiry, quantity
		FROM inventory WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("load inventory: %w", err)
	}
	defer rows.Close()
	items := make([]Item, 0, 32)
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Type, &item.Upgrade, &item.Expiry, &item.Quantity); err != nil {
			return nil, fmt.Errorf("scan inventory: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inventory: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) HasActiveItem(ctx context.Context, userID, itemID int) (bool, error) {
	if userID < 1 || itemID < 1 {
		return false, fmt.Errorf("invalid active-item lookup")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT expiry FROM inventory
		WHERE user_id = $1 AND item_id = $2
		ORDER BY id`, userID, itemID)
	if err != nil {
		return false, fmt.Errorf("load active item %d: %w", itemID, err)
	}
	defer rows.Close()
	now := s.now().UTC()
	for rows.Next() {
		var expiry string
		if err := rows.Scan(&expiry); err != nil {
			return false, fmt.Errorf("scan active item %d: %w", itemID, err)
		}
		cleaned := strings.TrimSpace(expiry)
		if strings.EqualFold(cleaned, "permanent") || cleaned == permanentInventoryExpiry {
			return true, nil
		}
		until, timed := parseTimedInventoryExpiry(cleaned)
		if timed && until.After(now) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate active item %d: %w", itemID, err)
	}
	return false, nil
}

func (s *PostgresStore) SetEquippedTitle(ctx context.Context, userID int, slot byte, title string) error {
	if userID < 1 || slot >= 4 {
		return fmt.Errorf("invalid equipped title request")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO equipped_titles (user_id, slot, title, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id, slot) DO UPDATE
		SET title = EXCLUDED.title, updated_at = now()`, userID, slot, title)
	if err != nil {
		return fmt.Errorf("set equipped title: %w", err)
	}
	return nil
}

func (s *PostgresStore) AddItem(ctx context.Context, userID, itemID, itemType int) error {
	if userID < 1 || itemID < 1 {
		return fmt.Errorf("invalid inventory item")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO inventory (user_id, item_id, type, upgrade, expiry)
		VALUES ($1, $2, $3, 0, 'permanent')`, userID, itemID, itemType)
	if err != nil {
		return fmt.Errorf("add inventory item: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetEquipment(ctx context.Context, field string, value, userID int) error {
	slot, err := strconv.Atoi(field)
	if err != nil || userID < 1 || slot < 0 || slot >= 34 || value < 0 {
		return fmt.Errorf("invalid equipment slot %q", field)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO equipment (user_id, slot, item_id) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, slot) DO UPDATE SET item_id = excluded.item_id`, userID, slot, value)
	if err != nil {
		return fmt.Errorf("save equipment: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetEquipmentBatch(ctx context.Context, userID int, updates map[int]int) error {
	if userID < 1 {
		return fmt.Errorf("invalid equipment user %d", userID)
	}
	if len(updates) == 0 {
		return nil
	}
	sortedSlots := make([]int, 0, len(updates))
	for slot, value := range updates {
		if slot < 0 || slot >= 34 || value < 0 || value > math.MaxInt32 {
			return fmt.Errorf("invalid equipment update slot %d value %d", slot, value)
		}
		sortedSlots = append(sortedSlots, slot)
	}
	sort.Ints(sortedSlots)
	slots := make([]int32, len(sortedSlots))
	itemIDs := make([]int32, len(sortedSlots))
	for index, slot := range sortedSlots {
		slots[index] = int32(slot)
		itemIDs[index] = int32(updates[slot])
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO equipment (user_id, slot, item_id)
		SELECT $1, input.slot, input.item_id
		FROM unnest($2::integer[], $3::integer[]) AS input(slot, item_id)
		ON CONFLICT (user_id, slot) DO UPDATE
		SET item_id = excluded.item_id`, userID, slots, itemIDs)
	if err != nil {
		return fmt.Errorf("save equipment batch: %w", err)
	}
	return nil
}

func (s *PostgresStore) Equipment(ctx context.Context, userID int) ([34]int, error) {
	var equipment [34]int
	rows, err := s.pool.Query(ctx, `SELECT slot, item_id FROM equipment WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return equipment, fmt.Errorf("load equipment: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slot, itemID int
		if err := rows.Scan(&slot, &itemID); err != nil {
			return equipment, fmt.Errorf("scan equipment: %w", err)
		}
		if slot >= 0 && slot < len(equipment) {
			equipment[slot] = itemID
		}
	}
	return equipment, rows.Err()
}

func (s *PostgresStore) PowerSlots(ctx context.Context, userID int) ([10]bool, error) {
	var slots [10]bool
	rows, err := s.pool.Query(ctx, `SELECT slot FROM power_slots WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return slots, fmt.Errorf("load power slots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			return slots, fmt.Errorf("scan power slot: %w", err)
		}
		if slot >= 1 && slot <= len(slots) {
			slots[slot-1] = true
		}
	}
	return slots, rows.Err()
}

func (s *PostgresStore) SyncSlotVouchers(ctx context.Context, userID int) error {
	if userID < 1 {
		return fmt.Errorf("invalid user ID")
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM inventory WHERE user_id = $1 AND item_id BETWEEN 10050 AND 10064`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory (user_id, item_id, type, upgrade, expiry)
			SELECT user_id, 10049 + slot, 0, 0, 'permanent'
			FROM power_slots WHERE user_id = $1 ORDER BY slot`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO inventory (user_id, item_id, type, upgrade, expiry)
			SELECT user_id, 10061 + set_index, 0, 0, 'permanent'
			FROM weapon_slot_extensions WHERE user_id = $1 ORDER BY set_index`, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("synchronize slot vouchers: %w", err)
	}
	return nil
}

func (s *PostgresStore) RecordKill(ctx context.Context, killerID, victimID int) error {
	if killerID < 1 || victimID < 1 {
		return fmt.Errorf("invalid killer or victim")
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO kill_log (killer_id, victim_id) VALUES ($1, $2)`, killerID, victimID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET kills = kills + 1 WHERE id = $1`, killerID); err != nil {
			return err
		}
		// A Power User death shield absorbs the death instead of the record.
		_, err := tx.Exec(ctx, `
			UPDATE users SET deaths = deaths + CASE WHEN death_shield > 0 THEN 0 ELSE 1 END,
			                 death_shield = GREATEST(death_shield - 1, 0)
			WHERE id = $1`, victimID)
		return err
	})
	if err != nil {
		return fmt.Errorf("register kill: %w", err)
	}
	return nil
}

func (s *PostgresStore) RegisterMatchResult(ctx context.Context, userID int, won bool) error {
	outcome := MatchLoss
	if won {
		outcome = MatchWin
	}
	return s.RegisterMatchOutcome(ctx, userID, outcome)
}

func (s *PostgresStore) RegisterMatchOutcome(ctx context.Context, userID int, outcome MatchOutcome) error {
	if userID < 1 {
		return fmt.Errorf("invalid match-result user")
	}
	column := ""
	switch outcome {
	case MatchDraw:
		column = "draws"
	case MatchWin:
		column = "wins"
	case MatchLoss:
		column = "losses"
	default:
		return fmt.Errorf("invalid match outcome %d", outcome)
	}
	// The dated event feeds the daily/weekly/monthly ranking boards.
	command := fmt.Sprintf(`
		WITH updated AS (UPDATE users SET %s = %s + 1 WHERE id = $1 RETURNING id)
		INSERT INTO ranking_events (user_id, event) SELECT id, $2 FROM updated`, column, column)
	result, err := s.pool.Exec(ctx, command, userID, rankingEventForOutcome(outcome))
	if err != nil {
		return fmt.Errorf("register match result: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("register match result: user %d not found", userID)
	}
	return nil
}

func (s *PostgresStore) AddPlayTime(ctx context.Context, userID int, seconds uint32) (uint32, error) {
	if userID < 1 {
		return 0, fmt.Errorf("invalid play-time user")
	}
	var total int64
	err := s.pool.QueryRow(ctx, `
		UPDATE users
		SET play_time_seconds = LEAST(4294967295, play_time_seconds + $1)
		WHERE id = $2
		RETURNING play_time_seconds`, int64(seconds), userID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("add play time: %w", err)
	}
	return uint32(total), nil
}

func (s *PostgresStore) CrestProgress(ctx context.Context, userID int) ([]CrestProgress, error) {
	if userID < 1 {
		return nil, fmt.Errorf("invalid crest-progress user")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT crest_id, progress
		FROM crest_progress WHERE user_id = $1 ORDER BY crest_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("load crest progress: %w", err)
	}
	defer rows.Close()
	result := make([]CrestProgress, 0, 8)
	for rows.Next() {
		var crestID int
		var progress int64
		if err := rows.Scan(&crestID, &progress); err != nil {
			return nil, fmt.Errorf("scan crest progress: %w", err)
		}
		if crestID < 0 || crestID > math.MaxUint16 || progress < 0 || progress > math.MaxUint32 {
			return nil, fmt.Errorf("crest progress is outside protocol range")
		}
		result = append(result, CrestProgress{CrestID: uint16(crestID), Progress: uint32(progress)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crest progress: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) SetCrestProgress(ctx context.Context, userID int, crestID uint16, progress uint32) error {
	if userID < 1 {
		return fmt.Errorf("invalid crest-progress user")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO crest_progress (user_id, crest_id, progress, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id, crest_id) DO UPDATE
		SET progress = excluded.progress, updated_at = excluded.updated_at`, userID, crestID, progress)
	if err != nil {
		return fmt.Errorf("save crest progress: %w", err)
	}
	return nil
}

func (s *PostgresStore) TutorialProgress(ctx context.Context, userID int) (TutorialProgress, error) {
	if userID < 1 {
		return TutorialProgress{}, fmt.Errorf("invalid tutorial-progress user")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO tutorial_progress (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return TutorialProgress{}, fmt.Errorf("initialize tutorial progress: %w", err)
	}
	var humanLevel, wolfLevel int
	if err := s.pool.QueryRow(ctx, `
		SELECT human_level, wolf_level FROM tutorial_progress WHERE user_id = $1`, userID).Scan(&humanLevel, &wolfLevel); err != nil {
		return TutorialProgress{}, fmt.Errorf("load tutorial progress: %w", err)
	}
	if humanLevel < 0 || humanLevel > 255 || wolfLevel < 0 || wolfLevel > 255 {
		return TutorialProgress{}, fmt.Errorf("tutorial progress is outside protocol range")
	}
	return TutorialProgress{HumanLevel: byte(humanLevel), WolfLevel: byte(wolfLevel)}, nil
}

func (s *PostgresStore) TutorialRewards(ctx context.Context, track, level byte) ([]TutorialReward, error) {
	if (track != 1 && track != 2) || level == 0 {
		return nil, fmt.Errorf("invalid tutorial reward track or level")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT item_id, days FROM tutorial_rewards
		WHERE track = $1 AND level = $2 ORDER BY item_id`, track, level)
	if err != nil {
		return nil, fmt.Errorf("load tutorial rewards: %w", err)
	}
	defer rows.Close()
	rewards := make([]TutorialReward, 0, 4)
	for rows.Next() {
		var reward TutorialReward
		if err := rows.Scan(&reward.ItemID, &reward.Days); err != nil {
			return nil, fmt.Errorf("scan tutorial reward: %w", err)
		}
		rewards = append(rewards, reward)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tutorial rewards: %w", err)
	}
	return rewards, nil
}

func (s *PostgresStore) ClaimTutorialReward(ctx context.Context, userID int, track, level byte) ([]Item, TutorialProgress, error) {
	if userID < 1 || (track != 1 && track != 2) || level == 0 {
		return nil, TutorialProgress{}, fmt.Errorf("invalid tutorial reward claim")
	}
	var granted []Item
	var progress TutorialProgress
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tutorial_progress (user_id) VALUES ($1)
			ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
			return err
		}
		var humanLevel, wolfLevel int
		if err := tx.QueryRow(ctx, `
			SELECT human_level, wolf_level FROM tutorial_progress
			WHERE user_id = $1 FOR UPDATE`, userID).Scan(&humanLevel, &wolfLevel); err != nil {
			return err
		}
		progress = TutorialProgress{HumanLevel: byte(humanLevel), WolfLevel: byte(wolfLevel)}
		current := humanLevel
		if track == 2 {
			current = wolfLevel
		}
		if int(level) <= current {
			return nil
		}
		if int(level) != current+1 {
			return fmt.Errorf("tutorial level %d does not follow %d", level, current)
		}
		claim, err := tx.Exec(ctx, `
			INSERT INTO tutorial_claims (user_id, track, level) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, track, level) DO NOTHING`, userID, track, level)
		if err != nil {
			return err
		}
		if claim.RowsAffected() == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT item_id, days FROM tutorial_rewards
			WHERE track = $1 AND level = $2 ORDER BY item_id`, track, level)
		if err != nil {
			return err
		}
		rewards := make([]TutorialReward, 0, 4)
		for rows.Next() {
			var reward TutorialReward
			if err := rows.Scan(&reward.ItemID, &reward.Days); err != nil {
				return err
			}
			rewards = append(rewards, reward)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, reward := range rewards {
			expiry := "permanent"
			if reward.Days > 0 {
				expiry = s.now().UTC().Add(time.Duration(reward.Days) * 24 * time.Hour).Format("20060102150405")
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO inventory (user_id, item_id, type, upgrade, expiry)
				VALUES ($1, $2, 0, 0, $3)`, userID, reward.ItemID, expiry); err != nil {
				return err
			}
			granted = append(granted, Item{ID: reward.ItemID, Type: 0, Expiry: expiry})
		}
		if track == 1 {
			progress.HumanLevel = level
			_, err = tx.Exec(ctx, `UPDATE tutorial_progress SET human_level = $1, updated_at = now() WHERE user_id = $2`, level, userID)
		} else {
			progress.WolfLevel = level
			_, err = tx.Exec(ctx, `UPDATE tutorial_progress SET wolf_level = $1, updated_at = now() WHERE user_id = $2`, level, userID)
		}
		return err
	})
	if err != nil {
		return nil, TutorialProgress{}, fmt.Errorf("claim tutorial reward: %w", err)
	}
	return granted, progress, nil
}

func (s *PostgresStore) CreditMatchRewards(ctx context.Context, userID, gold, gp int) error {
	if userID < 1 || gold < 0 || gp < 0 || gold > 1_000_000 || gp > 1_000_000 {
		return fmt.Errorf("invalid match award")
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var currentGP, currentGold, currentLevel int
		if err := tx.QueryRow(ctx, `SELECT gp, gold, level FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&currentGP, &currentGold, &currentLevel); err != nil {
			return err
		}
		newGP := minInt64(math.MaxInt32, int64(currentGP)+int64(gp))
		newGold := minInt64(math.MaxInt32, int64(currentGold)+int64(gold))
		newLevel, newGP := applyRankProgress(currentLevel, newGP)
		if _, err := tx.Exec(ctx, `UPDATE users SET gp = $1, gold = $2, level = $3 WHERE id = $4`, newGP, newGold, newLevel, userID); err != nil {
			return err
		}
		if gp == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO ranking_events (user_id, event, amount) VALUES ($1, $2, $3)`, userID, RankingEventGP, gp)
		return err
	})
	if err != nil {
		return fmt.Errorf("add match points: %w", err)
	}
	return nil
}

func minInt64(maximum int64, value int64) int {
	if value > maximum {
		value = maximum
	}
	return int(value)
}

func (s *PostgresStore) SetCharacterSlot(ctx context.Context, userID, slot, characterID int) error {
	if userID < 1 || slot < 1 || slot > 10 || characterID < 1 {
		return fmt.Errorf("invalid character slot")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO char_slots (user_id, slot, char_id) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, slot) DO UPDATE SET char_id = excluded.char_id`, userID, slot, characterID)
	if err != nil {
		return fmt.Errorf("save character slot: %w", err)
	}
	return nil
}

func (s *PostgresStore) CharacterSlots(ctx context.Context, userID int) (map[int]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT slot, char_id FROM char_slots WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return nil, fmt.Errorf("load character slots: %w", err)
	}
	defer rows.Close()
	slots := make(map[int]int)
	for rows.Next() {
		var slot, characterID int
		if err := rows.Scan(&slot, &characterID); err != nil {
			return nil, fmt.Errorf("scan character slot: %w", err)
		}
		slots[slot] = characterID
	}
	return slots, rows.Err()
}

func (s *PostgresStore) ReplaceCharacterSlots(ctx context.Context, userID int, slots map[int]int) error {
	if userID < 1 || len(slots) < 2 || len(slots) > 10 {
		return fmt.Errorf("invalid character selector")
	}
	characters := make(map[int]bool, len(slots))
	orderedSlots := make([]int, 0, len(slots))
	for slot, characterID := range slots {
		if slot < 1 || slot > 10 || characterID < 1 || characters[characterID] {
			return fmt.Errorf("invalid character selector")
		}
		characters[characterID] = true
		orderedSlots = append(orderedSlots, slot)
	}
	sort.Ints(orderedSlots)
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		for characterID := range characters {
			var owned bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM inventory
					WHERE user_id = $1 AND type = 1 AND item_id = $2
				)`, userID, characterID).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				return fmt.Errorf("character %d is not owned", characterID)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM char_slots WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, slot := range orderedSlots {
			if _, err := tx.Exec(ctx, `INSERT INTO char_slots (user_id, slot, char_id) VALUES ($1, $2, $3)`, userID, slot, slots[slot]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace character slots: %w", err)
	}
	return nil
}

func (s *PostgresStore) CharacterEquipment(ctx context.Context, userID int) (map[int]CharacterEquipment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT char_id, slot, item_unique
		FROM character_equipment
		WHERE user_id = $1
		ORDER BY char_id, slot`, userID)
	if err != nil {
		return nil, fmt.Errorf("load character equipment: %w", err)
	}
	defer rows.Close()
	result := make(map[int]CharacterEquipment)
	for rows.Next() {
		var characterID, slot, uniqueID int
		if err := rows.Scan(&characterID, &slot, &uniqueID); err != nil {
			return nil, fmt.Errorf("scan character equipment: %w", err)
		}
		if slot < 0 || slot >= CharacterEquipmentSlots {
			continue
		}
		equipment := result[characterID]
		equipment[slot] = uniqueID
		result[characterID] = equipment
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load character equipment: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) SetCharacterEquipment(ctx context.Context, userID, characterID int, equipment CharacterEquipment) error {
	if userID < 1 || characterID < 1 {
		return fmt.Errorf("invalid character equipment owner")
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var ownsCharacter bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM inventory
				WHERE user_id = $1 AND type = 1 AND item_id = $2
			)`, userID, characterID).Scan(&ownsCharacter); err != nil {
			return err
		}
		if !ownsCharacter {
			return fmt.Errorf("character %d is not owned", characterID)
		}
		for slot, uniqueID := range equipment {
			if uniqueID == 0 {
				continue
			}
			if uniqueID <= 2_000_000_000 || uniqueID-2_000_000_000 > 0xffff {
				return fmt.Errorf("character equipment slot %d has invalid unique ID %d", slot, uniqueID)
			}
			var ownsItem bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM inventory
					WHERE user_id = $1 AND type = 1 AND item_id = $2
				)`, userID, uniqueID-2_000_000_000).Scan(&ownsItem); err != nil {
				return err
			}
			if !ownsItem {
				return fmt.Errorf("character equipment item %d is not owned", uniqueID-2_000_000_000)
			}
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM character_equipment
			WHERE user_id = $1 AND char_id = $2`, userID, characterID); err != nil {
			return err
		}
		for slot, uniqueID := range equipment {
			if uniqueID == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO character_equipment (user_id, char_id, slot, item_unique)
				VALUES ($1, $2, $3, $4)`, userID, characterID, slot, uniqueID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save character equipment: %w", err)
	}
	return nil
}

func (s *PostgresStore) CharacterSelection(ctx context.Context, userID int) (CharacterSelection, error) {
	var loadout CharacterSelection
	err := s.pool.QueryRow(ctx, `
		SELECT red_char_id, blue_char_id FROM character_loadout WHERE user_id = $1`, userID).Scan(
		&loadout.RedCharacter, &loadout.BlueCharacter,
	)
	if noRows(err) {
		return CharacterSelection{}, nil
	}
	if err != nil {
		return CharacterSelection{}, fmt.Errorf("load character loadout: %w", err)
	}
	return loadout, nil
}

func (s *PostgresStore) SaveCharacterSelection(ctx context.Context, userID int, loadout CharacterSelection) error {
	if userID < 1 || loadout.RedCharacter < 1 || loadout.BlueCharacter < 1 {
		return fmt.Errorf("invalid character loadout")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO character_loadout (user_id, red_char_id, blue_char_id, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET
			red_char_id = excluded.red_char_id,
			blue_char_id = excluded.blue_char_id,
			updated_at = now()`, userID, loadout.RedCharacter, loadout.BlueCharacter)
	if err != nil {
		return fmt.Errorf("save character loadout: %w", err)
	}
	return nil
}

var (
	_ MatchOutcomeStore = (*PostgresStore)(nil)
	_ PlayTimeStore     = (*PostgresStore)(nil)
	_ ModerationStore   = (*PostgresStore)(nil)
)
