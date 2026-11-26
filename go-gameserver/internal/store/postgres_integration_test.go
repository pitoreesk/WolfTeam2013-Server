package store

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestPostgresAccountAndInventoryFlow(t *testing.T) {
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

	username := "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(username) > 20 {
		username = username[:20]
	}
	password := "integration-pass"
	if err := backend.Register(ctx, username, password); err != nil {
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
	starterItems, err := backend.Items(ctx, userID)
	if err != nil {
		t.Fatalf("load starter items: %v", err)
	}
	characters := make(map[int]bool)
	for _, item := range starterItems {
		if item.Type == 1 {
			characters[item.ID] = true
		}
	}
	if len(characters) != 2 || !characters[30005] || !characters[30006] {
		t.Fatalf("starter characters = %v, want only defaults 30005 and 30006", characters)
	}
	loadout, err := backend.CharacterSelection(ctx, userID)
	if err != nil || loadout != (CharacterSelection{RedCharacter: 30006, BlueCharacter: 30005}) {
		t.Fatalf("starter character loadout = %+v, %v", loadout, err)
	}
	characterSlots, err := backend.CharacterSlots(ctx, userID)
	if err != nil || len(characterSlots) != 2 || characterSlots[1] != 30005 || characterSlots[2] != 30006 {
		t.Fatalf("starter character slots = %v, %v", characterSlots, err)
	}
	powerSlots, err := backend.PowerSlots(ctx, userID)
	if err != nil {
		t.Fatalf("load starter power slots: %v", err)
	}
	for slot, unlocked := range powerSlots {
		if unlocked {
			t.Fatalf("starter power slot %d is unlocked", slot+1)
		}
	}
	var weaponSlotExtensions int
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM weapon_slot_extensions WHERE user_id = $1`, userID).Scan(&weaponSlotExtensions); err != nil {
		t.Fatalf("load starter weapon slot extensions: %v", err)
	}
	if weaponSlotExtensions != 0 {
		t.Fatalf("starter weapon slot extensions = %d, want 0", weaponSlotExtensions)
	}

	if got, err := backend.Login(ctx, username, "wrong-password"); err != nil || got != -1 {
		t.Fatalf("invalid-password login = %d, %v", got, err)
	}
	if got, err := backend.Login(ctx, username, ""); err != nil || got != -1 {
		t.Fatalf("empty password login = %d, %v", got, err)
	}
	if got, err := backend.Login(ctx, username, password); err != nil || got != userID {
		t.Fatalf("correct password login = %d, %v, want %d", got, err, userID)
	}
	newPassword := "changed-integration-pass"
	if err := backend.ChangePassword(ctx, username, newPassword); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if got, err := backend.Login(ctx, username, password); err != nil || got != -1 {
		t.Fatalf("old password after change = %d, %v", got, err)
	}
	if got, err := backend.Login(ctx, username, newPassword); err != nil || got != userID {
		t.Fatalf("new password login = %d, %v, want %d", got, err, userID)
	}
	if err := backend.SetEquipmentBatch(ctx, userID, map[int]int{0: 2_000_001_001, 24: 2_000_010_001}); err != nil {
		t.Fatalf("set equipment batch: %v", err)
	}
	equipment, err := backend.Equipment(ctx, userID)
	if err != nil || equipment[0] != 2_000_001_001 || equipment[24] != 2_000_010_001 {
		t.Fatalf("equipment after batch = %v, %v", equipment, err)
	}
	if err := backend.SetEquipmentBatch(ctx, userID, map[int]int{0: 2_000_001_002, 34: 1}); err == nil {
		t.Fatal("accepted invalid equipment batch")
	}
	equipment, err = backend.Equipment(ctx, userID)
	if err != nil || equipment[0] != 2_000_001_001 || equipment[24] != 2_000_010_001 {
		t.Fatalf("invalid equipment batch partially persisted: %v, %v", equipment, err)
	}

	info, err := backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Callsign != username || info.Authority != 0 ||
		info.Gold != 100000 || info.Wins != 0 || info.Losses != 0 {
		t.Fatalf("initial user info = %+v, %v", info, err)
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE users SET authority = 13 WHERE id = $1`, userID); err != nil {
		t.Fatalf("set authority: %v", err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Authority != 13 {
		t.Fatalf("persisted authority = %+v, %v", info, err)
	}
	muteStartedAt := time.Now().UTC().Truncate(time.Second)
	muteExpiresAt := muteStartedAt.Add(30 * time.Minute)
	if found, err := backend.SetMutedByCallsign(ctx, username, ModerationState{
		Active: true, StartedAt: muteStartedAt, ExpiresAt: muteExpiresAt, Reason: "integration mute",
	}); err != nil || !found {
		t.Fatalf("set muted state = %v, %v", found, err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || !info.Muted || !info.MuteStartedAt.Equal(muteStartedAt) ||
		!info.MuteExpiresAt.Equal(muteExpiresAt) || info.MuteReason != "integration mute" {
		t.Fatalf("persisted mute state = %+v, %v", info, err)
	}
	if found, err := backend.SetMutedByCallsign(ctx, username, ModerationState{
		Active: true, StartedAt: muteStartedAt.Add(-2 * time.Hour),
		ExpiresAt: muteStartedAt.Add(-time.Hour), Reason: "expired mute",
	}); err != nil || !found {
		t.Fatalf("set expired mute state = %v, %v", found, err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Muted || info.MuteReason != "" {
		t.Fatalf("expired mute state = %+v, %v", info, err)
	}
	banStartedAt := time.Now().UTC().Truncate(time.Second)
	banExpiresAt := banStartedAt.Add(time.Hour)
	if found, err := backend.SetBannedByCallsign(ctx, username, ModerationState{
		Active: true, StartedAt: banStartedAt, ExpiresAt: banExpiresAt, Reason: "integration ban",
	}); err != nil || !found {
		t.Fatalf("set banned state = %v, %v", found, err)
	}
	if got, err := backend.Login(ctx, username, newPassword); err != nil || got != -1 {
		t.Fatalf("banned account login = %d, %v", got, err)
	}
	if found, err := backend.SetBannedByCallsign(ctx, username, ModerationState{}); err != nil || !found {
		t.Fatalf("clear banned state = %v, %v", found, err)
	}
	if got, err := backend.Login(ctx, username, newPassword); err != nil || got != userID {
		t.Fatalf("unbanned account login = %d, %v, want %d", got, err, userID)
	}
	if found, err := backend.SetBannedByCallsign(ctx, username, ModerationState{
		Active: true, StartedAt: banStartedAt.Add(-2 * time.Hour),
		ExpiresAt: banStartedAt.Add(-time.Hour), Reason: "expired ban",
	}); err != nil || !found {
		t.Fatalf("set expired ban state = %v, %v", found, err)
	}
	if got, err := backend.Login(ctx, username, newPassword); err != nil || got != userID {
		t.Fatalf("expired-ban login = %d, %v, want %d", got, err, userID)
	}
	var stillBanned bool
	if err := backend.pool.QueryRow(ctx, `SELECT banned FROM users WHERE id = $1`, userID).Scan(&stillBanned); err != nil {
		t.Fatalf("load expired ban cleanup: %v", err)
	}
	if stillBanned {
		t.Fatal("expired ban was not cleared during login")
	}
	if err := backend.CreditMatchRewards(ctx, userID, 0, 1999); err != nil {
		t.Fatalf("add GP below rank threshold: %v", err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Level != 61 || info.GP != 1999 {
		t.Fatalf("progress below rank threshold = %+v, %v", info, err)
	}
	if err := backend.CreditMatchRewards(ctx, userID, 0, 1); err != nil {
		t.Fatalf("add rank-up GP: %v", err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Level != 60 || info.GP != 0 {
		t.Fatalf("rank-up progress = %+v, %v, want level 60 with zero GP", info, err)
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE users SET level = 62, gp = 0 WHERE id = $1`, userID); err != nil {
		t.Fatalf("set reserved rank fixture: %v", err)
	}
	if err := backend.CreditMatchRewards(ctx, userID, 0, 3000); err != nil {
		t.Fatalf("add reserved-rank GP: %v", err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Level != 62 || info.GP != 3000 {
		t.Fatalf("reserved-rank progress = %+v, %v, want rank 62 unchanged", info, err)
	}
	if err := backend.RegisterMatchResult(ctx, userID, true); err != nil {
		t.Fatalf("register win: %v", err)
	}
	if err := backend.RegisterMatchResult(ctx, userID, false); err != nil {
		t.Fatalf("register loss: %v", err)
	}
	if err := backend.RegisterMatchOutcome(ctx, userID, MatchDraw); err != nil {
		t.Fatalf("register draw: %v", err)
	}
	if total, err := backend.AddPlayTime(ctx, userID, 120); err != nil || total != 120 {
		t.Fatalf("first play-time update = %d, %v", total, err)
	}
	if total, err := backend.AddPlayTime(ctx, userID, 30); err != nil || total != 150 {
		t.Fatalf("second play-time update = %d, %v", total, err)
	}
	if _, err := backend.pool.Exec(ctx, `
		UPDATE users SET headshot_kills = 2, special_weapon_kills = 3, wolf_kills = 4
		WHERE id = $1`, userID); err != nil {
		t.Fatalf("set detailed kill counters: %v", err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Wins != 1 || info.Losses != 1 || info.Draws != 1 ||
		info.Kills != 0 || info.Deaths != 0 || info.HeadshotKills != 2 ||
		info.SpecialWeaponKills != 3 || info.WolfKills != 4 || info.PlayTimeSeconds != 150 {
		t.Fatalf("match result counters = %+v, %v", info, err)
	}
	if err := backend.SetCrestProgress(ctx, userID, 123, 4567); err != nil {
		t.Fatalf("set crest progress: %v", err)
	}
	crestProgress, err := backend.CrestProgress(ctx, userID)
	if err != nil || len(crestProgress) != 1 || crestProgress[0] != (CrestProgress{CrestID: 123, Progress: 4567}) {
		t.Fatalf("crest progress = %+v, %v", crestProgress, err)
	}
	rewardInsert, err := backend.pool.Exec(ctx, `
		INSERT INTO tutorial_rewards (track, level, item_id, days)
		VALUES (2, 1, 29999, 7) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatalf("insert tutorial reward fixture: %v", err)
	}
	if rewardInsert.RowsAffected() == 1 {
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM tutorial_rewards WHERE track = 2 AND level = 1 AND item_id = 29999`)
		}()
	}
	rewards, err := backend.TutorialRewards(ctx, 2, 1)
	if err != nil || len(rewards) == 0 {
		t.Fatalf("tutorial rewards = %+v, %v", rewards, err)
	}
	granted, tutorialProgress, err := backend.ClaimTutorialReward(ctx, userID, 2, 1)
	foundTutorialFixture := false
	for _, item := range granted {
		if item.ID == 29999 {
			foundTutorialFixture = true
		}
	}
	if err != nil || !foundTutorialFixture || tutorialProgress.WolfLevel != 1 {
		t.Fatalf("tutorial claim = items %+v progress %+v, %v", granted, tutorialProgress, err)
	}
	granted, tutorialProgress, err = backend.ClaimTutorialReward(ctx, userID, 2, 1)
	if err != nil || len(granted) != 0 || tutorialProgress.WolfLevel != 1 {
		t.Fatalf("replayed tutorial claim = items %+v progress %+v, %v", granted, tutorialProgress, err)
	}
	channels, err := backend.Channels(ctx)
	if err != nil || len(channels) == 0 || channels[0].Port != 40707 {
		t.Fatalf("channels = %+v, %v", channels, err)
	}

	purchase, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 7777, ItemType: 0, Days: 30, Currency: 4, Price: 100,
	})
	if err != nil || !purchase.Success || purchase.Gold != 99900 {
		t.Fatalf("purchase = %+v, %v", purchase, err)
	}
	firstPurchaseExpiry, err := time.ParseInLocation("20060102150405", purchase.Expiry, time.UTC)
	if err != nil {
		t.Fatalf("parse first purchase expiry %q: %v", purchase.Expiry, err)
	}
	renewal, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 7777, ItemType: 0, Days: 7, Currency: 4, Price: 0,
	})
	if err != nil || !renewal.Success || renewal.Expiry != firstPurchaseExpiry.AddDate(0, 0, 7).Format("20060102150405") {
		t.Fatalf("renewed purchase = %+v, %v", renewal, err)
	}
	var renewedRows int
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = 7777`, userID).Scan(&renewedRows); err != nil || renewedRows != 1 {
		t.Fatalf("renewed purchase rows = %d, %v, want 1", renewedRows, err)
	}
	active, err := backend.HasActiveItem(ctx, userID, 7777)
	if err != nil || !active {
		t.Fatalf("active timed inventory item = %v, %v, want true", active, err)
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE inventory SET expiry = '20000101000000' WHERE user_id = $1 AND item_id = 7777`, userID); err != nil {
		t.Fatalf("expire inventory fixture: %v", err)
	}
	active, err = backend.HasActiveItem(ctx, userID, 7777)
	if err != nil || active {
		t.Fatalf("expired inventory item = %v, %v, want false", active, err)
	}
	if err := backend.SetEquipment(ctx, "1", 2_000_007_777, userID); err != nil {
		t.Fatalf("equip expiring inventory item: %v", err)
	}
	itemsAfterExpiry, err := backend.Items(ctx, userID)
	if err != nil {
		t.Fatalf("load inventory after expiry: %v", err)
	}
	for _, item := range itemsAfterExpiry {
		if item.ID == 7777 {
			t.Fatalf("expired item remained in inventory snapshot: %+v", item)
		}
	}
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = 7777`, userID).Scan(&renewedRows); err != nil || renewedRows != 0 {
		t.Fatalf("expired inventory rows = %d, %v, want 0", renewedRows, err)
	}
	equipment, err = backend.Equipment(ctx, userID)
	if err != nil || equipment[1] != 0 {
		t.Fatalf("equipment after item expiry = %v, %v", equipment, err)
	}
	expiredItems, err := backend.TakeExpiredItems(ctx, userID, 8)
	if err != nil || len(expiredItems) != 1 || expiredItems[0] != 7777 {
		t.Fatalf("expired inventory notifications = %v, %v, want [7777]", expiredItems, err)
	}
	expiredItems, err = backend.TakeExpiredItems(ctx, userID, 8)
	if err != nil || len(expiredItems) != 0 {
		t.Fatalf("replayed expired inventory notifications = %v, %v, want empty", expiredItems, err)
	}
	setPurchase, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 14007, ItemType: 0, Days: 30, Currency: 2, Price: 0,
		Components: []PurchaseComponent{{ItemID: 7781, Days: 30}, {ItemID: 30011, Days: 7}},
	})
	if err != nil || !setPurchase.Success {
		t.Fatalf("set purchase = %+v, %v", setPurchase, err)
	}
	var setRows, weaponRows, characterRows int
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE item_id = 14007),
		       count(*) FILTER (WHERE item_id = 7781 AND type = 0),
		       count(*) FILTER (WHERE item_id = 30011 AND type = 1)
		FROM inventory WHERE user_id = $1`, userID).Scan(&setRows, &weaponRows, &characterRows); err != nil {
		t.Fatalf("load expanded set inventory: %v", err)
	}
	if setRows != 0 || weaponRows != 1 || characterRows != 1 {
		t.Fatalf("expanded set rows = container %d, weapon %d, character %d", setRows, weaponRows, characterRows)
	}
	if result, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 15005, ItemType: 0, Days: 0, Currency: 4, Price: 0,
	}); err != nil || !result.Success {
		t.Fatalf("purchase consumable = %+v, %v", result, err)
	}
	used, err := backend.ConsumeInventoryItem(ctx, userID, 15005)
	if err != nil || !used {
		t.Fatalf("use supported consumable = %v, %v", used, err)
	}
	info, err = backend.AccountSnapshot(ctx, userID)
	if err != nil || info.Gold != 199900 {
		t.Fatalf("gold after consumable = %d, %v, want 199900", info.Gold, err)
	}
	if result, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 15030, ItemType: 0, Days: 0, Quantity: 10, Currency: 4, Price: 0,
	}); err != nil || !result.Success {
		t.Fatalf("purchase consumable quantity = %+v, %v", result, err)
	}
	var quantityRows, quantityUnits int
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(quantity), 0) FROM inventory
		WHERE user_id = $1 AND item_id = 15030`, userID).Scan(&quantityRows, &quantityUnits); err != nil || quantityRows != 1 || quantityUnits != 10 {
		t.Fatalf("consumable quantity stack = %d rows/%d units, %v, want 1/10", quantityRows, quantityUnits, err)
	}
	if used, err := backend.ConsumeInventoryItem(ctx, userID, 15030); err != nil || !used {
		t.Fatalf("consume quantity stack = %v, %v", used, err)
	}
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(quantity), 0) FROM inventory
		WHERE user_id = $1 AND item_id = 15030`, userID).Scan(&quantityRows, &quantityUnits); err != nil || quantityRows != 1 || quantityUnits != 9 {
		t.Fatalf("consumable quantity after use = %d rows/%d units, %v, want 1/9", quantityRows, quantityUnits, err)
	}
	if result, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 19001, ItemType: 0, Quantity: 3, Currency: 4, Price: 0,
	}); err != nil || !result.Success {
		t.Fatalf("purchase in-game counted item = %+v, %v", result, err)
	}
	if err := backend.SetEquipment(ctx, "24", 2_000_019_001, userID); err != nil {
		t.Fatalf("equip in-game counted item: %v", err)
	}
	for _, wantRemaining := range []int{2, 1, 0} {
		consumed, err := backend.ConsumeCountedItems(ctx, userID, []int{2_000_019_001})
		if err != nil || len(consumed) != 1 || consumed[0].UniqueID != 2_000_019_001 ||
			consumed[0].Remaining != wantRemaining {
			t.Fatalf("consume in-game counted item = %+v, %v, want remaining %d", consumed, err, wantRemaining)
		}
	}
	var equippedCountedItem int
	if err := backend.pool.QueryRow(ctx, `
		SELECT item_id FROM equipment WHERE user_id = $1 AND slot = 24`,
		userID).Scan(&equippedCountedItem); err != nil || equippedCountedItem != 0 {
		t.Fatalf("depleted counted item equipment = %d, %v, want cleared", equippedCountedItem, err)
	}
	if result, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 15031, ItemType: 0, Quantity: 3, Currency: 4, Price: 10,
	}); err != nil || !result.Success {
		t.Fatalf("purchase refundable quantity stack = %+v, %v", result, err)
	}
	refunded, err := backend.RefundItem(ctx, userID, 15031)
	if err != nil || !refunded.Success || refunded.RefundedGold != 4 {
		t.Fatalf("refund quantity unit = %+v, %v, want 4 Gold", refunded, err)
	}
	var remainingPrice int64
	if err := backend.pool.QueryRow(ctx, `
		SELECT quantity, paid_price FROM inventory
		WHERE user_id = $1 AND item_id = 15031`, userID).Scan(&quantityUnits, &remainingPrice); err != nil || quantityUnits != 2 || remainingPrice != 6 {
		t.Fatalf("refunded quantity stack = %d units/%d price, %v, want 2/6", quantityUnits, remainingPrice, err)
	}
	sold, err := backend.SellItem(ctx, userID, 15031)
	if err != nil || !sold.Success {
		t.Fatalf("sell quantity unit = %+v, %v", sold, err)
	}
	if err := backend.pool.QueryRow(ctx, `
		SELECT quantity, paid_price FROM inventory
		WHERE user_id = $1 AND item_id = 15031`, userID).Scan(&quantityUnits, &remainingPrice); err != nil || quantityUnits != 1 || remainingPrice != 3 {
		t.Fatalf("sold quantity stack = %d units/%d price, %v, want 1/3", quantityUnits, remainingPrice, err)
	}
	if result, err := backend.CommitPurchase(ctx, PurchaseOrder{
		UserID: userID, ItemID: 7778, ItemType: 0, Days: 30, Currency: 4, Price: 0,
	}); err != nil || !result.Success {
		t.Fatalf("purchase non-consumable = %+v, %v", result, err)
	}
	used, err = backend.ConsumeInventoryItem(ctx, userID, 7778)
	if err != nil || used {
		t.Fatalf("use unsupported inventory item = %v, %v, want false", used, err)
	}
	items, err := backend.Items(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	stillOwned := false
	for _, item := range items {
		if item.ID == 7778 {
			stillOwned = true
			break
		}
	}
	if !stillOwned {
		t.Fatal("unsupported item use removed the inventory row")
	}
	senderName := "sender" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(senderName) > 20 {
		senderName = senderName[:20]
	}
	if err := backend.Register(ctx, senderName, password); err != nil {
		t.Fatalf("register gift sender: %v", err)
	}
	var senderID int
	if err := backend.pool.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, senderName).Scan(&senderID); err != nil {
		t.Fatalf("load gift sender: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, senderID)
	}()
	ladderInfo, err := backend.PrivateLadderInfo(ctx, userID)
	if err != nil || ladderInfo.Points != 0 || ladderInfo.Rank == 0 {
		t.Fatalf("initial private-ladder info = %+v, %v", ladderInfo, err)
	}
	if err := backend.RegisterPrivateLadderMatch(ctx, []int{userID}, []int{senderID}); err != nil {
		t.Fatalf("register private-ladder match: %v", err)
	}
	ladderInfo, err = backend.PrivateLadderInfo(ctx, userID)
	if err != nil || ladderInfo.Points != 10 {
		t.Fatalf("updated private-ladder info = %+v, %v", ladderInfo, err)
	}
	ladderRanks, err := backend.PrivateLadderRanks(ctx, 0, 3)
	if err != nil || len(ladderRanks) == 0 || ladderRanks[0].Username != username || ladderRanks[0].Points != 10 {
		t.Fatalf("private-ladder ranks = %+v, %v", ladderRanks, err)
	}
	if _, err := backend.pool.Exec(ctx, `
		INSERT INTO private_ladder_notices (title, starts_on, ends_on, summary, details)
		VALUES ('Integration season', current_date - 1, current_date + 1, 'summary', 'details')`); err != nil {
		t.Fatalf("insert private-ladder notice: %v", err)
	}
	ladderNotice, err := backend.PrivateLadderNotice(ctx, time.Now())
	if err != nil || ladderNotice.Title != "Integration season" || ladderNotice.Summary != "summary" {
		t.Fatalf("private-ladder notice = %+v, %v", ladderNotice, err)
	}
	var expiredGiftID uint32
	if err := backend.pool.QueryRow(ctx, `
		INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, message, created_at, expires_at)
		VALUES ($1, $2, 7777, 0, 7, 'expired integration gift', now() - INTERVAL '91 days', now() - INTERVAL '1 day')
		RETURNING id`, senderID, userID).Scan(&expiredGiftID); err != nil {
		t.Fatalf("insert expired gift fixture: %v", err)
	}
	var giftID uint32
	if err := backend.pool.QueryRow(ctx, `
		INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, message)
		VALUES ($1, $2, 7778, 0, 30, 'integration gift') RETURNING id`, senderID, userID).Scan(&giftID); err != nil {
		t.Fatalf("insert gift fixture: %v", err)
	}
	if count, err := backend.GiftCount(ctx, userID); err != nil || count != 1 {
		t.Fatalf("gift count = %d, %v", count, err)
	}
	var expiredRows int
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE id = $1`, expiredGiftID).Scan(&expiredRows); err != nil || expiredRows != 0 {
		t.Fatalf("expired gift rows = %d, %v", expiredRows, err)
	}
	if expiredClaim, err := backend.ClaimGift(ctx, userID, expiredGiftID); err != nil || expiredClaim.Success {
		t.Fatalf("expired gift claim = %+v, %v", expiredClaim, err)
	}
	page, err := backend.Gifts(ctx, userID, 0, 0, 13)
	if err != nil || page.Total != 1 || len(page.Gifts) != 1 || page.Gifts[0].ID != giftID ||
		page.Gifts[0].Counterparty != senderName || page.Gifts[0].Days != 30 || page.Gifts[0].InboxDays != 90 {
		t.Fatalf("received gift page = %+v, %v", page, err)
	}
	sent, err := backend.Gifts(ctx, senderID, 1, 0, 13)
	if err != nil || len(sent.Gifts) != 1 || sent.Gifts[0].Counterparty != username {
		t.Fatalf("sent gift page = %+v, %v", sent, err)
	}
	detail, found, err := backend.GiftDetail(ctx, userID, giftID, 0)
	if err != nil || !found || detail.Status != 1 || detail.Message != "integration gift" || detail.Days != 30 || detail.InboxDays != 90 {
		t.Fatalf("gift detail = %+v, %v, %v", detail, found, err)
	}
	var ownedBeforeClaim int
	var expiryBeforeClaim string
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*), min(expiry) FROM inventory
		WHERE user_id = $1 AND item_id = 7778`, userID).Scan(&ownedBeforeClaim, &expiryBeforeClaim); err != nil {
		t.Fatalf("count inventory before gift claim: %v", err)
	}
	previousGiftExpiry, err := time.ParseInLocation("20060102150405", expiryBeforeClaim, time.UTC)
	if err != nil {
		t.Fatalf("parse pre-gift expiry %q: %v", expiryBeforeClaim, err)
	}
	claim, err := backend.ClaimGift(ctx, userID, giftID)
	if err != nil || !claim.Success || claim.Gift.ItemID != 7778 || claim.Gift.Status != 2 ||
		claim.Expiry != previousGiftExpiry.AddDate(0, 0, 30).Format("20060102150405") {
		t.Fatalf("gift claim = %+v, %v", claim, err)
	}
	replay, err := backend.ClaimGift(ctx, userID, giftID)
	if err != nil || replay.Success {
		t.Fatalf("replayed gift claim = %+v, %v", replay, err)
	}
	var grantedCount int
	if err := backend.pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = 7778`, userID).Scan(&grantedCount); err != nil || grantedCount != ownedBeforeClaim {
		t.Fatalf("claimed gift inventory count = %d, %v; before %d", grantedCount, err, ownedBeforeClaim)
	}
	setGift, err := backend.SendGift(ctx, GiftSendRequest{
		SenderID: senderID, ReceiverCallsign: username, ItemID: 14007, ItemType: 0,
		Days: 30, Currency: 2, Price: 0, Message: "integration set gift",
		Components: []PurchaseComponent{{ItemID: 7782, Days: 30}, {ItemID: 30012, Days: 7}},
	})
	if err != nil || !setGift.Success {
		t.Fatalf("send set gift = %+v, %v", setGift, err)
	}
	var setGiftID uint32
	if err := backend.pool.QueryRow(ctx, `
		SELECT id FROM gifts
		WHERE sender_user_id = $1 AND recipient_user_id = $2 AND item_id = 14007
		ORDER BY id DESC LIMIT 1`, senderID, userID).Scan(&setGiftID); err != nil {
		t.Fatalf("load set gift: %v", err)
	}
	setGiftClaim, err := backend.ClaimGift(ctx, userID, setGiftID)
	if err != nil || !setGiftClaim.Success {
		t.Fatalf("claim set gift = %+v, %v", setGiftClaim, err)
	}
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE item_id = 14007),
		       count(*) FILTER (WHERE item_id = 7782 AND type = 0),
		       count(*) FILTER (WHERE item_id = 30012 AND type = 1)
		FROM inventory WHERE user_id = $1`, userID).Scan(&setRows, &weaponRows, &characterRows); err != nil {
		t.Fatalf("load claimed set gift inventory: %v", err)
	}
	if setRows != 0 || weaponRows != 1 || characterRows != 1 {
		t.Fatalf("claimed set gift rows = container %d, weapon %d, character %d", setRows, weaponRows, characterRows)
	}
	prideName := "P" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(prideName) > 16 {
		prideName = prideName[:16]
	}
	pride, master, created, err := backend.CreatePride(ctx, userID, prideName, "integration pride")
	if err != nil || !created || pride.ID == 0 || pride.Name != prideName || master.Role != 8 {
		t.Fatalf("create pride = %+v, %+v, %v, %v", pride, master, created, err)
	}
	pridePage, err := backend.PrideList(ctx, prideName, "", 0, 10)
	if err != nil || pridePage.Total != 1 || len(pridePage.Prides) != 1 || pridePage.Prides[0].ID != pride.ID {
		t.Fatalf("pride list = %+v, %v", pridePage, err)
	}
	appliedPride, applied, err := backend.ApplyPride(ctx, senderID, pride.ID)
	if err != nil || !applied || appliedPride.ID != pride.ID {
		t.Fatalf("apply pride = %+v, %v, %v", appliedPride, applied, err)
	}
	applications, err := backend.PrideApplications(ctx, userID)
	if err != nil || len(applications) != 1 || applications[0].Username != senderName {
		t.Fatalf("pride applications = %+v, %v", applications, err)
	}
	acceptedPride, acceptedMember, accepted, err := backend.AcceptPrideApplication(ctx, userID, senderName)
	if err != nil || !accepted || acceptedPride.ID != pride.ID || acceptedMember.UserID != senderID {
		t.Fatalf("accept pride member = %+v, %+v, %v, %v", acceptedPride, acceptedMember, accepted, err)
	}
	members, err := backend.PrideMembers(ctx, pride.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("pride members = %+v, %v", members, err)
	}
	memos, added, err := backend.AddPrideMemo(ctx, senderID, pride.ID, "integration memo")
	if err != nil || !added || len(memos) != 1 || memos[0].Message != "integration memo" {
		t.Fatalf("add pride memo = %+v, %v, %v", memos, added, err)
	}
	if deleted, err := backend.DeletePrideMemo(ctx, userID, pride.ID, memos[0].ID); err != nil || !deleted {
		t.Fatalf("delete pride memo = %v, %v", deleted, err)
	}
	updatedPride, updated, err := backend.SetPrideMessages(ctx, userID, "new intro", "new notice")
	if err != nil || !updated || updatedPride.Intro != "new intro" || updatedPride.Notice != "new notice" {
		t.Fatalf("update pride messages = %+v, %v, %v", updatedPride, updated, err)
	}
	closing, scheduled, err := backend.SchedulePrideClose(ctx, userID)
	if err != nil || !scheduled || closing.CloseStatus != 10 || closing.CloseAt == nil {
		t.Fatalf("schedule pride close = %+v, %v, %v", closing, scheduled, err)
	}
	reopened, canceled, err := backend.CancelPrideClose(ctx, userID)
	if err != nil || !canceled || reopened.CloseStatus != 0 || reopened.CloseAt != nil {
		t.Fatalf("cancel pride close = %+v, %v, %v", reopened, canceled, err)
	}
	changed, newMaster, changedOK, err := backend.ChangePrideMaster(ctx, userID, senderName)
	if err != nil || !changedOK || changed.Master != senderName || newMaster.Role != 8 {
		t.Fatalf("change pride master = %+v, %+v, %v, %v", changed, newMaster, changedOK, err)
	}
	changed, newMaster, changedOK, err = backend.ChangePrideMaster(ctx, senderID, username)
	if err != nil || !changedOK || changed.Master != username || newMaster.Role != 8 {
		t.Fatalf("restore pride master = %+v, %+v, %v, %v", changed, newMaster, changedOK, err)
	}

	opponentName := "opp" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(opponentName) > 20 {
		opponentName = opponentName[:20]
	}
	if err := backend.Register(ctx, opponentName, password); err != nil {
		t.Fatalf("register pride opponent: %v", err)
	}
	var opponentID int
	if err := backend.pool.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, opponentName).Scan(&opponentID); err != nil {
		t.Fatalf("load pride opponent: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, opponentID)
	}()
	opponentPrideName := "O" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(opponentPrideName) > 16 {
		opponentPrideName = opponentPrideName[:16]
	}
	opponentPride, _, opponentCreated, err := backend.CreatePride(ctx, opponentID, opponentPrideName, "opponent")
	if err != nil || !opponentCreated {
		t.Fatalf("create opponent pride = %+v, %v, %v", opponentPride, opponentCreated, err)
	}
	if err := backend.RegisterPrideMatch(ctx, []int{userID, senderID}, []int{opponentID}); err != nil {
		t.Fatalf("register pride match: %v", err)
	}
	summary, err := backend.PrideBattleResult(ctx, pride.ID)
	if err != nil || summary.Wins != 1 || summary.Losses != 0 || summary.RankPoints != 3 ||
		summary.WinRate != 100 || summary.CurrentRank != 1 || summary.PeakRank != 1 {
		t.Fatalf("winner pride result = %+v, %v", summary, err)
	}
	opponentSummary, err := backend.PrideBattleResult(ctx, opponentPride.ID)
	if err != nil || opponentSummary.Wins != 0 || opponentSummary.Losses != 1 {
		t.Fatalf("loser pride result = %+v, %v", opponentSummary, err)
	}
	ranks, err := backend.PrideRanks(ctx, 8)
	if err != nil || len(ranks) < 2 || ranks[0].ID != pride.ID {
		t.Fatalf("pride ranks = %+v, %v", ranks, err)
	}
	if _, scheduled, err := backend.SchedulePrideClose(ctx, opponentID); err != nil || !scheduled {
		t.Fatalf("schedule opponent pride close = %v, %v", scheduled, err)
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE prides SET close_at = now() - interval '1 second' WHERE id = $1`, opponentPride.ID); err != nil {
		t.Fatalf("age pride closure fixture: %v", err)
	}
	closures, err := backend.CompletePrideClosures(ctx, time.Now())
	if err != nil || len(closures) != 1 || closures[0].PrideID != opponentPride.ID || len(closures[0].MemberUserIDs) != 1 || closures[0].MemberUserIDs[0] != opponentID {
		t.Fatalf("complete pride closure = %+v, %v", closures, err)
	}
	if _, found, err := backend.PrideByID(ctx, opponentPride.ID); err != nil || found {
		t.Fatalf("closed pride still exists = %v, %v", found, err)
	}
	if kickedPride, kickedMember, kicked, err := backend.KickPrideMember(ctx, userID, senderName); err != nil || !kicked || kickedPride.ID != pride.ID || kickedMember.UserID != senderID {
		t.Fatalf("kick pride member = %+v, %+v, %v, %v", kickedPride, kickedMember, kicked, err)
	}
	usernames, err := backend.PrideMemberUsernames(ctx, userID)
	if err != nil || len(usernames) != 1 || usernames[0] != username {
		t.Fatalf("pride member IDs = %+v, %v", usernames, err)
	}
	if err := backend.ReplaceCharacterSlots(ctx, userID, map[int]int{1: 30005, 2: 30006}); err != nil {
		t.Fatalf("replace character slots: %v", err)
	}
	if _, err := backend.pool.Exec(ctx, `
		INSERT INTO inventory (user_id, item_id, type, expiry)
		VALUES ($1, 31267, 1, '20991231235959')`, userID); err != nil {
		t.Fatalf("insert character equipment fixture: %v", err)
	}
	var characterEquipment CharacterEquipment
	characterEquipment[0] = 2_000_031_267
	characterEquipment[15] = 2_000_031_267
	if err := backend.SetCharacterEquipment(ctx, userID, 30005, characterEquipment); err != nil {
		t.Fatalf("set character equipment: %v", err)
	}
	equipmentByCharacter, err := backend.CharacterEquipment(ctx, userID)
	if err != nil || equipmentByCharacter[30005] != characterEquipment {
		t.Fatalf("character equipment = %+v, %v", equipmentByCharacter, err)
	}
	invalidEquipment := CharacterEquipment{2_000_033_999}
	if err := backend.SetCharacterEquipment(ctx, userID, 30005, invalidEquipment); err == nil {
		t.Fatal("accepted unowned character equipment")
	}
}

func TestPostgresChannelAccountFlow(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	firstName, secondName := "ca"+suffix+"a", "ca"+suffix+"b"
	for _, username := range []string{firstName, secondName} {
		if err := backend.Register(ctx, username, "integration-pass"); err != nil {
			t.Fatal(err)
		}
	}
	var firstID, secondID int
	if err := backend.pool.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, firstName).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if err := backend.pool.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, secondName).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = ANY($1)`, []int{firstID, secondID})
	}()

	firstCallsign := "First" + suffix
	changedCallsign, changed, err := backend.ChangeCallsign(ctx, firstID, firstCallsign)
	if err != nil || !changed || changedCallsign != firstCallsign {
		t.Fatalf("change callsign = %q/%v, %v", changedCallsign, changed, err)
	}
	unchanged, changed, err := backend.ChangeCallsign(ctx, secondID, firstCallsign)
	if err != nil || changed || unchanged != secondName {
		t.Fatalf("duplicate callsign = %q/%v, %v", unchanged, changed, err)
	}
	state, changed, err := backend.ChangeSchool(ctx, firstID, 7)
	if err != nil || !changed || state.SchoolID != 7 {
		t.Fatalf("change school = %+v/%v, %v", state, changed, err)
	}
	if _, err := backend.pool.Exec(ctx, `UPDATE users SET wolf_points = 1234 WHERE id = $1`, firstID); err != nil {
		t.Fatal(err)
	}
	state, err = backend.ChannelAccountState(ctx, firstID)
	if err != nil || state.WolfPoints != 1234 {
		t.Fatalf("channel account state = %+v, %v", state, err)
	}

	targetID, requester, target, asked, err := backend.AskRecommendation(ctx, firstID, secondName)
	if err != nil || !asked || targetID != secondID || int(requester.UserID) != firstID || int(target.UserID) != secondID {
		t.Fatalf("ask recommendation = %d/%+v/%+v/%v, %v", targetID, requester, target, asked, err)
	}
	pending, err := backend.PendingRecommendations(ctx, secondID)
	if err != nil || len(pending) != 1 || int(pending[0].UserID) != firstID {
		t.Fatalf("pending recommendations = %+v, %v", pending, err)
	}
	requesterID, _, responder, answered, err := backend.AnswerRecommendation(ctx, secondID, firstCallsign, true)
	if err != nil || !answered || requesterID != firstID || int(responder.UserID) != secondID {
		t.Fatalf("answer recommendation = %d/%+v/%v, %v", requesterID, responder, answered, err)
	}
	pending, err = backend.PendingRecommendations(ctx, secondID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after answer = %+v, %v", pending, err)
	}
}

func TestPostgresClientTelemetryFlow(t *testing.T) {
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

	sessionID := uint16(time.Now().UnixNano())
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = backend.pool.Exec(cleanupCtx, `DELETE FROM client_telemetry WHERE session_id = $1 AND provider = $2 AND payload = $3`, int(sessionID), TelemetryApex, payload)
	}()
	if err := backend.RecordClientTelemetry(ctx, ClientTelemetry{SessionID: sessionID, Provider: TelemetryApex, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := backend.pool.QueryRow(ctx, `
		SELECT count(*) FROM client_telemetry
		WHERE user_id IS NULL AND session_id = $1 AND provider = $2 AND payload = $3`, int(sessionID), TelemetryApex, payload).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted telemetry rows = %d", count)
	}
}
