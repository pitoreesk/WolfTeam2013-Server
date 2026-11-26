package store

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestPostgresForgeRewardLifecycle(t *testing.T) {
	databaseURL := os.Getenv("WT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	backend, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	username := "forge" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(username) > 20 {
		username = username[:20]
	}
	if err := backend.Register(ctx, username, "integration-pass"); err != nil {
		t.Fatal(err)
	}
	var userID int
	if err := backend.pool.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = backend.pool.Exec(cleanupCtx, `
			UPDATE forge_recipes
			SET success_percent = (ARRAY[80,65,50,35,20])[challenge + 1]
			WHERE category BETWEEN 0 AND 2 AND challenge BETWEEN 0 AND 4`)
	}()

	if _, err := backend.pool.Exec(ctx, `UPDATE forge_recipes SET success_percent = 100 WHERE category = 0 AND challenge = 0`); err != nil {
		t.Fatal(err)
	}
	passed, err := backend.RunForgeChallenge(ctx, ForgeAttempt{UserID: userID, Category: 0, Round: 0, Action: 1})
	if err != nil || passed.Outcome != 1 || passed.RewardItem == 0 {
		t.Fatalf("passed roll = %+v, %v", passed, err)
	}
	var gifts, inventory int
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1`, userID).Scan(&gifts); err != nil || gifts != 0 {
		t.Fatalf("gift count before collection = %d, %v", gifts, err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, passed.RewardItem).Scan(&inventory); err != nil || inventory != 0 {
		t.Fatalf("inventory count before collection = %d, %v", inventory, err)
	}

	collected, err := backend.RunForgeChallenge(ctx, ForgeAttempt{
		UserID: userID, Category: 0, Round: 0, Action: 0,
		RewardItem: passed.RewardItem, RewardDays: passed.RewardDays,
	})
	if err != nil || collected.Outcome != 2 || collected.RewardItem != passed.RewardItem {
		t.Fatalf("collected roll = %+v, %v", collected, err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1`, userID).Scan(&gifts); err != nil || gifts != 1 {
		t.Fatalf("gift count after collection = %d, %v", gifts, err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, passed.RewardItem).Scan(&inventory); err != nil || inventory != 0 {
		t.Fatalf("inventory count after collection = %d, %v", inventory, err)
	}

	if _, err := backend.pool.Exec(ctx, `UPDATE forge_recipes SET success_percent = 0 WHERE category = 1 AND challenge = 0`); err != nil {
		t.Fatal(err)
	}
	broken, err := backend.RunForgeChallenge(ctx, ForgeAttempt{UserID: userID, Category: 1, Round: 0, Action: 1})
	if err != nil || broken.Outcome != 0 || broken.RewardItem == 0 {
		t.Fatalf("broken roll = %+v, %v", broken, err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1`, userID).Scan(&gifts); err != nil || gifts != 2 {
		t.Fatalf("gift count after pity reward = %d, %v", gifts, err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, broken.RewardItem).Scan(&inventory); err != nil || inventory != 0 {
		t.Fatalf("pity inventory count before claim = %d, %v", inventory, err)
	}
}
