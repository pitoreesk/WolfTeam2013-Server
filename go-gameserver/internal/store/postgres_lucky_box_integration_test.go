package store

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"
	"unicode/utf16"

	"wtserver-go/internal/lootbox"
)

func TestPostgresLuckyBoxQueuesRewardAsGift(t *testing.T) {
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
	catalogData := appendLuckyBoxTestReward(nil, 17008, 30011, 0, 7)
	catalogData = appendLuckyBoxTestReward(catalogData, 17006, 15005, 1, 3)
	catalog, err := lootbox.Parse(catalogData)
	if err != nil {
		t.Fatal(err)
	}
	backend.ConfigureLuckyBoxCatalog(catalog)

	username := "lucky" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	}()
	if _, err := backend.pool.Exec(ctx, `
		UPDATE inventory SET quantity = 2
		WHERE user_id = $1 AND item_id = 17008`, userID); err != nil {
		t.Fatalf("stack lucky-box fixture: %v", err)
	}

	result, err := backend.DrawLuckyBox(ctx, userID, 17008, 2_000_017_008)
	if err != nil || !result.Success || result.RewardItem != 30011 || result.Days != 7 || result.Remaining != 1 {
		t.Fatalf("open lucky box = %+v, %v", result, err)
	}

	var remainingBoxes int
	if err := backend.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(quantity), 0) FROM inventory WHERE user_id = $1 AND item_id = 17008`, userID).Scan(&remainingBoxes); err != nil || remainingBoxes != 1 {
		t.Fatalf("remaining lucky boxes = %d, %v, want 1", remainingBoxes, err)
	}

	var giftID uint32
	var giftItem, giftType, giftDays, giftQuantity, giftStatus int
	var sender, message string
	if err := backend.pool.QueryRow(ctx, `
		SELECT g.id, g.item_id, g.item_type, g.days, g.quantity, g.status, sender.callsign, g.message
		FROM gifts g JOIN users sender ON sender.id = g.sender_user_id
		WHERE g.recipient_user_id = $1
		ORDER BY g.id DESC LIMIT 1`, userID).Scan(&giftID, &giftItem, &giftType, &giftDays, &giftQuantity, &giftStatus, &sender, &message); err != nil {
		t.Fatalf("load lucky-box gift: %v", err)
	}
	if giftItem != result.RewardItem || giftType != 1 || giftDays != 7 || giftQuantity != 1 ||
		giftStatus != 0 || sender != "Lucky Box" || message != "Lucky Box reward" {
		t.Fatalf("lucky-box gift = item %d type %d days %d quantity %d status %d sender %q message %q",
			giftItem, giftType, giftDays, giftQuantity, giftStatus, sender, message)
	}

	var directTimedRewards int
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*) FROM inventory
		WHERE user_id = $1 AND item_id = $2
		  AND expiry NOT IN ('permanent', '20991231235959')`, userID, result.RewardItem).Scan(&directTimedRewards); err != nil || directTimedRewards != 0 {
		t.Fatalf("direct lucky-box inventory rewards = %d, %v, want 0", directTimedRewards, err)
	}

	if _, err := backend.pool.Exec(ctx, `
		INSERT INTO inventory (user_id, item_id, type, upgrade, expiry, quantity)
		VALUES ($1, 17006, 0, 0, 'permanent', 2)`, userID); err != nil {
		t.Fatalf("add consumable lucky-box fixture: %v", err)
	}
	consumable, err := backend.DrawLuckyBox(ctx, userID, 17006, 2_000_017_006)
	if err != nil || !consumable.Success || consumable.RewardItem == 0 || consumable.Days != 3 || consumable.Remaining != 1 {
		t.Fatalf("open consumable lucky box = %+v, %v", consumable, err)
	}
	if err := backend.pool.QueryRow(ctx, `
		SELECT g.id, g.item_id, g.item_type, g.days, g.quantity, g.status, sender.callsign, g.message
		FROM gifts g JOIN users sender ON sender.id = g.sender_user_id
		WHERE g.recipient_user_id = $1
		ORDER BY g.id DESC LIMIT 1`, userID).Scan(&giftID, &giftItem, &giftType, &giftDays, &giftQuantity, &giftStatus, &sender, &message); err != nil {
		t.Fatalf("load consumable lucky-box gift: %v", err)
	}
	if giftItem != consumable.RewardItem || giftType != 0 || giftDays != 0 || giftQuantity != 3 ||
		giftStatus != 0 || sender != "Lucky Box" {
		t.Fatalf("consumable lucky-box gift = item %d type %d days %d quantity %d status %d sender %q",
			giftItem, giftType, giftDays, giftQuantity, giftStatus, sender)
	}
	var directConsumableRewards int
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, consumable.RewardItem).Scan(&directConsumableRewards); err != nil || directConsumableRewards != 0 {
		t.Fatalf("direct consumable lucky-box rewards = %d, %v, want 0", directConsumableRewards, err)
	}
	claim, err := backend.ClaimGift(ctx, userID, giftID)
	if err != nil || !claim.Success || claim.Gift.Quantity != 3 || claim.Expiry != permanentInventoryExpiry {
		t.Fatalf("claim consumable lucky-box gift = %+v, %v", claim, err)
	}
	var claimedQuantity int
	if err := backend.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(quantity), 0) FROM inventory
		WHERE user_id = $1 AND item_id = $2`, userID, consumable.RewardItem).Scan(&claimedQuantity); err != nil || claimedQuantity != 3 {
		t.Fatalf("claimed consumable quantity = %d, %v, want 3", claimedQuantity, err)
	}
}

func appendLuckyBoxTestReward(data []byte, boxID, itemID, stackable, days uint32) []byte {
	name := utf16.Encode([]rune("Integration Box"))
	record := make([]byte, 6+len(name)*2+8*4)
	binary.LittleEndian.PutUint32(record[0:4], boxID)
	binary.LittleEndian.PutUint16(record[4:6], uint16(len(name)))
	offset := 6
	for _, unit := range name {
		binary.LittleEndian.PutUint16(record[offset:offset+2], unit)
		offset += 2
	}
	for _, value := range []uint32{itemID, 0, 10000, stackable, days, days, 0, 0} {
		binary.LittleEndian.PutUint32(record[offset:offset+4], value)
		offset += 4
	}
	return append(data, record...)
}
