// Package testsupport provides reusable backend stubs for integration-style tests.
package testsupport

import (
	"context"
	"time"

	"wtserver-go/internal/store"
)

// UserStore is a configurable baseline implementation of store.UserStore.
// Zero-value methods succeed with empty results; setting Err makes every
// non-overridden operation fail with that error.
type GameRepository struct {
	Err error
}

var _ store.GameRepository = GameRepository{}

func (s GameRepository) Login(context.Context, string, string) (int, error) { return 1, s.Err }
func (s GameRepository) AccountSnapshot(context.Context, int) (store.AccountSnapshot, error) {
	return store.AccountSnapshot{}, s.Err
}
func (s GameRepository) Items(context.Context, int) ([]store.Item, error) { return nil, s.Err }
func (s GameRepository) AddItem(context.Context, int, int, int) error     { return s.Err }
func (s GameRepository) CommitPurchase(context.Context, store.PurchaseOrder) (store.PurchaseReceipt, error) {
	return store.PurchaseReceipt{}, s.Err
}
func (s GameRepository) PurchaseBasket(context.Context, []store.PurchaseOrder) (store.BasketPurchaseResult, error) {
	return store.BasketPurchaseResult{}, s.Err
}
func (s GameRepository) DailyShopItems(context.Context, time.Time) ([2]uint16, error) {
	return [2]uint16{}, s.Err
}
func (s GameRepository) ForgeRecipes(context.Context, byte) ([]store.ForgeRecipe, error) {
	return nil, s.Err
}
func (s GameRepository) RunForgeChallenge(context.Context, store.ForgeAttempt) (store.ForgeResult, error) {
	return store.ForgeResult{}, s.Err
}
func (s GameRepository) ForgeWinners(context.Context, byte) ([]store.ForgeWinner, error) {
	return nil, s.Err
}
func (s GameRepository) VIPShopItems(context.Context, byte, byte) ([]store.VIPShopItem, error) {
	return nil, s.Err
}
func (s GameRepository) GiftCount(context.Context, int) (int, error) { return 0, s.Err }
func (s GameRepository) Gifts(context.Context, int, byte, int, int) (store.GiftPage, error) {
	return store.GiftPage{}, s.Err
}
func (s GameRepository) GiftDetail(context.Context, int, uint32, byte) (store.Gift, bool, error) {
	return store.Gift{}, false, s.Err
}
func (s GameRepository) ClaimGift(context.Context, int, uint32) (store.GiftClaimResult, error) {
	return store.GiftClaimResult{}, s.Err
}
func (s GameRepository) SendGift(context.Context, store.GiftSendRequest) (store.GiftSendResult, error) {
	return store.GiftSendResult{}, s.Err
}
func (s GameRepository) RemoveInventoryItem(context.Context, int, int) (bool, error) {
	return s.Err == nil, s.Err
}
func (s GameRepository) ConsumeInventoryItem(context.Context, int, int) (bool, error) {
	return s.Err == nil, s.Err
}
func (s GameRepository) ClaimPackageContents(context.Context, int, int) (bool, error) {
	return s.Err == nil, s.Err
}
func (s GameRepository) DrawLuckyBox(context.Context, int, int, int) (store.LuckyBoxReceipt, error) {
	return store.LuckyBoxReceipt{}, s.Err
}
func (s GameRepository) SetItemUpgrade(context.Context, int, int, byte, uint16) (bool, error) {
	return s.Err == nil, s.Err
}
func (s GameRepository) SellItem(context.Context, int, int) (store.SellResult, error) {
	return store.SellResult{}, s.Err
}
func (s GameRepository) RefundItem(context.Context, int, int) (store.RefundResult, error) {
	return store.RefundResult{}, s.Err
}
func (s GameRepository) PermanentItemTicketInfo(context.Context, int, int) (store.PermanentTicketInfo, error) {
	return store.PermanentTicketInfo{}, s.Err
}
func (s GameRepository) UsePermanentItemTicket(context.Context, int, int, int) (bool, error) {
	return s.Err == nil, s.Err
}
func (s GameRepository) SyncSlotVouchers(context.Context, int) error { return s.Err }
func (s GameRepository) SetEquipment(context.Context, string, int, int) error {
	return s.Err
}
func (s GameRepository) Equipment(context.Context, int) ([34]int, error) { return [34]int{}, s.Err }
func (s GameRepository) PowerSlots(context.Context, int) ([10]bool, error) {
	return [10]bool{}, s.Err
}
func (s GameRepository) RecordKill(context.Context, int, int) error           { return s.Err }
func (s GameRepository) RegisterMatchResult(context.Context, int, bool) error { return s.Err }
func (s GameRepository) CrestProgress(context.Context, int) ([]store.CrestProgress, error) {
	return nil, s.Err
}
func (s GameRepository) SetCrestProgress(context.Context, int, uint16, uint32) error {
	return s.Err
}
func (s GameRepository) TutorialProgress(context.Context, int) (store.TutorialProgress, error) {
	return store.TutorialProgress{}, s.Err
}
func (s GameRepository) TutorialRewards(context.Context, byte, byte) ([]store.TutorialReward, error) {
	return nil, s.Err
}
func (s GameRepository) ClaimTutorialReward(context.Context, int, byte, byte) ([]store.Item, store.TutorialProgress, error) {
	return nil, store.TutorialProgress{}, s.Err
}
func (s GameRepository) CreditMatchRewards(context.Context, int, int, int) error { return s.Err }
func (s GameRepository) SetCharacterSlot(context.Context, int, int, int) error {
	return s.Err
}
func (s GameRepository) CharacterSlots(context.Context, int) (map[int]int, error) {
	return map[int]int{}, s.Err
}
func (s GameRepository) ReplaceCharacterSlots(context.Context, int, map[int]int) error {
	return s.Err
}
func (s GameRepository) CharacterSelection(context.Context, int) (store.CharacterSelection, error) {
	return store.CharacterSelection{}, s.Err
}
func (s GameRepository) SaveCharacterSelection(context.Context, int, store.CharacterSelection) error {
	return s.Err
}
func (s GameRepository) SetEquippedTitle(context.Context, int, byte, string) error { return s.Err }
