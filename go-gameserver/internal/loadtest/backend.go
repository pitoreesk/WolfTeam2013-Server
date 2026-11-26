package loadtest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wtserver-go/internal/store"
)

// memoryUsers is deliberately private to the load-test package. There is no
// database handle or database URL in this implementation, so the load command
// cannot mutate a development or production database by mistake.
type memoryUsers struct {
	mu        sync.RWMutex
	nextID    int
	idsByName map[string]int
	users     map[int]*memoryUser

	operations      atomic.Uint64
	purchases       atomic.Uint64
	equipmentWrites atomic.Uint64
	matchResults    atomic.Uint64
}

type memoryUser struct {
	info           store.AccountSnapshot
	items          []store.Item
	equipment      [34]int
	powerSlots     [10]bool
	characterSlots map[int]int
	loadout        store.CharacterSelection
	crest          map[uint16]uint32
	tutorial       store.TutorialProgress
	titles         [4]string
}

type backendStats struct {
	Operations      uint64 `json:"operations"`
	Purchases       uint64 `json:"purchases"`
	EquipmentWrites uint64 `json:"equipment_writes"`
	MatchResults    uint64 `json:"match_results"`
}

func newMemoryUsers() *memoryUsers {
	return &memoryUsers{
		nextID:    1,
		idsByName: make(map[string]int),
		users:     make(map[int]*memoryUser),
	}
}

func (m *memoryUsers) stats() backendStats {
	return backendStats{
		Operations:      m.operations.Load(),
		Purchases:       m.purchases.Load(),
		EquipmentWrites: m.equipmentWrites.Load(),
		MatchResults:    m.matchResults.Load(),
	}
}

func (m *memoryUsers) operation() { m.operations.Add(1) }

func (m *memoryUsers) Login(_ context.Context, username, password string) (int, error) {
	m.operation()
	if strings.TrimSpace(username) == "" || password != loadPassword {
		return -1, nil
	}
	key := strings.ToLower(username)
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.idsByName[key]; ok {
		return id, nil
	}
	id := m.nextID
	m.nextID++
	m.idsByName[key] = id
	m.users[id] = &memoryUser{
		info: store.AccountSnapshot{
			Callsign: username,
			Level:    61,
			Gold:     10_000_000,
			WC:       10_000_000,
			Cash:     10_000_000,
		},
		items: []store.Item{
			{ID: 1001, Type: 0, Expiry: "permanent"},
			{ID: 2001, Type: 0, Expiry: "permanent"},
			{ID: 30005, Type: 1, Expiry: "permanent"},
			{ID: 30006, Type: 1, Expiry: "permanent"},
		},
		powerSlots:     [10]bool{true, true, true},
		characterSlots: map[int]int{0: 30005, 1: 30006},
		loadout:        store.CharacterSelection{RedCharacter: 30006, BlueCharacter: 30005},
		crest:          make(map[uint16]uint32),
	}
	return id, nil
}

func (m *memoryUsers) AccountSnapshot(_ context.Context, userID int) (store.AccountSnapshot, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return store.AccountSnapshot{}, err
	}
	return user.info, nil
}

func (m *memoryUsers) Items(_ context.Context, userID int) ([]store.Item, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return nil, err
	}
	return append([]store.Item(nil), user.items...), nil
}

func (m *memoryUsers) AddItem(_ context.Context, userID, itemID, days int) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.items = append(user.items, store.Item{ID: itemID, Type: itemType(itemID), Expiry: expiryForDays(days)})
	return nil
}

func (m *memoryUsers) CommitPurchase(_ context.Context, request store.PurchaseOrder) (store.PurchaseReceipt, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(request.UserID)
	if err != nil {
		return store.PurchaseReceipt{}, err
	}
	if !deduct(&user.info, request.Currency, int(request.Price)) {
		return purchaseResult(user.info, false, "insufficient test balance", ""), nil
	}
	quantity := request.Quantity
	if quantity <= 0 {
		quantity = 1
	}
	expiry := expiryForDays(request.Days)
	user.items = append(user.items, store.Item{ID: request.ItemID, Type: request.ItemType, Expiry: expiry, Quantity: quantity})
	m.purchases.Add(1)
	return purchaseResult(user.info, true, "", expiry), nil
}

func (m *memoryUsers) PurchaseBasket(ctx context.Context, requests []store.PurchaseOrder) (store.BasketPurchaseResult, error) {
	results := make([]store.PurchaseReceipt, 0, len(requests))
	for _, request := range requests {
		result, err := m.CommitPurchase(ctx, request)
		if err != nil || !result.Success {
			return store.BasketPurchaseResult{Items: results}, err
		}
		results = append(results, result)
	}
	info := store.AccountSnapshot{}
	if len(requests) > 0 {
		info, _ = m.AccountSnapshot(ctx, requests[0].UserID)
	}
	return store.BasketPurchaseResult{Success: true, Items: results, Gold: info.Gold, WC: info.WC, Cash: info.Cash}, nil
}

func (m *memoryUsers) DailyShopItems(context.Context, time.Time) ([2]uint16, error) {
	m.operation()
	return [2]uint16{10038, 10087}, nil
}

func (m *memoryUsers) ForgeRecipes(context.Context, byte) ([]store.ForgeRecipe, error) {
	m.operation()
	return nil, nil
}

func (m *memoryUsers) RunForgeChallenge(context.Context, store.ForgeAttempt) (store.ForgeResult, error) {
	m.operation()
	return store.ForgeResult{}, nil
}

func (m *memoryUsers) ForgeWinners(context.Context, byte) ([]store.ForgeWinner, error) {
	m.operation()
	return nil, nil
}

func (m *memoryUsers) VIPShopItems(context.Context, byte, byte) ([]store.VIPShopItem, error) {
	m.operation()
	return nil, nil
}

func (m *memoryUsers) GiftCount(context.Context, int) (int, error) {
	m.operation()
	return 0, nil
}

func (m *memoryUsers) Gifts(context.Context, int, byte, int, int) (store.GiftPage, error) {
	m.operation()
	return store.GiftPage{}, nil
}

func (m *memoryUsers) GiftDetail(context.Context, int, uint32, byte) (store.Gift, bool, error) {
	m.operation()
	return store.Gift{}, false, nil
}

func (m *memoryUsers) ClaimGift(context.Context, int, uint32) (store.GiftClaimResult, error) {
	m.operation()
	return store.GiftClaimResult{}, nil
}

func (m *memoryUsers) SendGift(_ context.Context, request store.GiftSendRequest) (store.GiftSendResult, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(request.SenderID)
	if err != nil {
		return store.GiftSendResult{}, err
	}
	result := store.GiftSendResult{Gold: user.info.Gold, WC: user.info.WC, Cash: user.info.Cash}
	price := int(request.Price)
	switch request.Currency {
	case 1:
		if result.Cash < price {
			result.Error = "insufficient-funds"
			return result, nil
		}
		result.Cash -= price
		user.info.Cash = result.Cash
	case 2:
		if result.WC < price {
			result.Error = "insufficient-funds"
			return result, nil
		}
		result.WC -= price
		user.info.WC = result.WC
	case 4:
		if result.Gold < price {
			result.Error = "insufficient-funds"
			return result, nil
		}
		result.Gold -= price
		user.info.Gold = result.Gold
	default:
		result.Error = "invalid-request"
		return result, nil
	}
	result.Success = true
	return result, nil
}

func (m *memoryUsers) RemoveInventoryItem(_ context.Context, userID, itemID int) (bool, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return false, err
	}
	for i, item := range user.items {
		if item.ID == itemID {
			if item.Quantity > 1 {
				user.items[i].Quantity--
				return true, nil
			}
			user.items = append(user.items[:i], user.items[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryUsers) ConsumeInventoryItem(ctx context.Context, userID, itemID int) (bool, error) {
	return m.RemoveInventoryItem(ctx, userID, itemID)
}

func (m *memoryUsers) ClaimPackageContents(context.Context, int, int) (bool, error) {
	m.operation()
	return true, nil
}

func (m *memoryUsers) DrawLuckyBox(context.Context, int, int, int) (store.LuckyBoxReceipt, error) {
	m.operation()
	return store.LuckyBoxReceipt{Success: true, BoxUnique: 1, RewardItem: 19601, Days: 7}, nil
}

func (m *memoryUsers) SetItemUpgrade(ctx context.Context, userID, itemID int, slot byte, upgradeID uint16) (bool, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return false, err
	}
	for i := range user.items {
		if user.items[i].ID == itemID {
			shift := uint(slot) * 16
			mask := int64(0xffff) << shift
			user.items[i].Upgrade = int(int64(user.items[i].Upgrade)&^mask | int64(upgradeID)<<shift)
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryUsers) SellItem(ctx context.Context, userID, itemID int) (store.SellResult, error) {
	removed, err := m.RemoveInventoryItem(ctx, userID, itemID)
	if err != nil || !removed {
		return store.SellResult{}, err
	}
	return store.SellResult{Success: true, Gold: 10_000_010, Cash: 10_000_000}, nil
}

func (m *memoryUsers) RefundItem(context.Context, int, int) (store.RefundResult, error) {
	m.operation()
	return store.RefundResult{Success: true}, nil
}

func (m *memoryUsers) PermanentItemTicketInfo(context.Context, int, int) (store.PermanentTicketInfo, error) {
	m.operation()
	return store.PermanentTicketInfo{}, nil
}

func (m *memoryUsers) UsePermanentItemTicket(context.Context, int, int, int) (bool, error) {
	m.operation()
	return true, nil
}

func (m *memoryUsers) SyncSlotVouchers(context.Context, int) error {
	m.operation()
	return nil
}

func (m *memoryUsers) SetEquipment(_ context.Context, field string, value, userID int) error {
	m.operation()
	var slot int
	if _, err := fmt.Sscanf(field, "%d", &slot); err != nil || slot < 0 || slot >= 34 {
		return fmt.Errorf("invalid equipment field %q", field)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.equipment[slot] = value
	m.equipmentWrites.Add(1)
	return nil
}

func (m *memoryUsers) SetEquipmentBatch(_ context.Context, userID int, updates map[int]int) error {
	for slot, value := range updates {
		if slot < 0 || slot >= 34 || value < 0 {
			return fmt.Errorf("invalid equipment update slot %d value %d", slot, value)
		}
	}
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	for slot, value := range updates {
		user.equipment[slot] = value
	}
	m.equipmentWrites.Add(uint64(len(updates)))
	return nil
}

func (m *memoryUsers) Equipment(_ context.Context, userID int) ([34]int, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return [34]int{}, err
	}
	return user.equipment, nil
}

func (m *memoryUsers) PowerSlots(_ context.Context, userID int) ([10]bool, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return [10]bool{}, err
	}
	return user.powerSlots, nil
}

func (m *memoryUsers) RecordKill(_ context.Context, killerID, _ int) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(killerID)
	if err != nil {
		return err
	}
	user.info.Kills++
	return nil
}

func (m *memoryUsers) RegisterMatchResult(ctx context.Context, userID int, won bool) error {
	outcome := store.MatchLoss
	if won {
		outcome = store.MatchWin
	}
	return m.RegisterMatchOutcome(ctx, userID, outcome)
}

func (m *memoryUsers) RegisterMatchOutcome(_ context.Context, userID int, outcome store.MatchOutcome) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	switch outcome {
	case store.MatchWin:
		user.info.Wins++
	case store.MatchLoss:
		user.info.Losses++
	case store.MatchDraw:
		user.info.Draws++
	default:
		return fmt.Errorf("invalid match outcome %d", outcome)
	}
	m.matchResults.Add(1)
	return nil
}

func (m *memoryUsers) AddPlayTime(_ context.Context, userID int, seconds uint32) (uint32, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return 0, err
	}
	maximum := ^uint32(0)
	if maximum-user.info.PlayTimeSeconds < seconds {
		user.info.PlayTimeSeconds = maximum
	} else {
		user.info.PlayTimeSeconds += seconds
	}
	return user.info.PlayTimeSeconds, nil
}

func (m *memoryUsers) CrestProgress(_ context.Context, userID int) ([]store.CrestProgress, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return nil, err
	}
	result := make([]store.CrestProgress, 0, len(user.crest))
	for id, progress := range user.crest {
		result = append(result, store.CrestProgress{CrestID: id, Progress: progress})
	}
	return result, nil
}

func (m *memoryUsers) SetCrestProgress(_ context.Context, userID int, crestID uint16, progress uint32) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.crest[crestID] = progress
	return nil
}

func (m *memoryUsers) TutorialProgress(_ context.Context, userID int) (store.TutorialProgress, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return store.TutorialProgress{}, err
	}
	return user.tutorial, nil
}

func (m *memoryUsers) TutorialRewards(context.Context, byte, byte) ([]store.TutorialReward, error) {
	m.operation()
	return nil, nil
}

func (m *memoryUsers) ClaimTutorialReward(_ context.Context, userID int, human, wolf byte) ([]store.Item, store.TutorialProgress, error) {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return nil, store.TutorialProgress{}, err
	}
	user.tutorial = store.TutorialProgress{HumanLevel: human, WolfLevel: wolf}
	return append([]store.Item(nil), user.items...), user.tutorial, nil
}

func (m *memoryUsers) CreditMatchRewards(_ context.Context, userID, gold, gp int) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.info.Gold += gold
	user.info.GP += gp
	if user.info.Level > 1 && user.info.Level < 62 && user.info.GP >= store.NextLevelThreshold(user.info.Level) {
		user.info.Level--
		user.info.GP = 0
	}
	return nil
}

func (m *memoryUsers) SetCharacterSlot(_ context.Context, userID, slot, characterID int) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.characterSlots[slot] = characterID
	return nil
}

func (m *memoryUsers) CharacterSlots(_ context.Context, userID int) (map[int]int, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return nil, err
	}
	return cloneMap(user.characterSlots), nil
}

func (m *memoryUsers) ReplaceCharacterSlots(_ context.Context, userID int, slots map[int]int) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.characterSlots = cloneMap(slots)
	return nil
}

func (m *memoryUsers) CharacterSelection(_ context.Context, userID int) (store.CharacterSelection, error) {
	m.operation()
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return store.CharacterSelection{}, err
	}
	return user.loadout, nil
}

func (m *memoryUsers) SaveCharacterSelection(_ context.Context, userID int, loadout store.CharacterSelection) error {
	m.operation()
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.loadout = loadout
	return nil
}

func (m *memoryUsers) SetEquippedTitle(_ context.Context, userID int, slot byte, title string) error {
	m.operation()
	if slot >= 4 {
		return fmt.Errorf("invalid title slot %d", slot)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	user, err := m.userLocked(userID)
	if err != nil {
		return err
	}
	user.titles[slot] = title
	return nil
}

func (m *memoryUsers) userLocked(userID int) (*memoryUser, error) {
	user := m.users[userID]
	if user == nil {
		return nil, fmt.Errorf("unknown in-memory user %d", userID)
	}
	return user, nil
}

func itemType(itemID int) int {
	if itemID >= 30000 {
		return 1
	}
	return 0
}

func expiryForDays(days int) string {
	if days <= 0 {
		return "permanent"
	}
	return time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format("20060102150405")
}

func purchaseResult(info store.AccountSnapshot, success bool, message, expiry string) store.PurchaseReceipt {
	return store.PurchaseReceipt{
		Success: success,
		Expiry:  expiry,
		Gold:    info.Gold,
		WC:      info.WC,
		Cash:    info.Cash,
		Error:   message,
	}
}

func deduct(info *store.AccountSnapshot, currency, amount int) bool {
	if amount < 0 {
		return false
	}
	var balance *int
	switch currency {
	case 1:
		balance = &info.Cash
	case 2:
		balance = &info.WC
	default:
		balance = &info.Gold
	}
	if *balance < amount {
		return false
	}
	*balance -= amount
	return true
}

func cloneMap(source map[int]int) map[int]int {
	result := make(map[int]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

var _ store.GameRepository = (*memoryUsers)(nil)
