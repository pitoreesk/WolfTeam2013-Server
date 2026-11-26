package store

import (
	"context"
	"time"
)

type AccountSnapshot struct {
	Callsign           string
	Authority          byte
	Muted              bool
	MuteStartedAt      time.Time
	MuteExpiresAt      time.Time
	MuteReason         string
	Level              int
	GP                 int
	Gold               int
	WC                 int
	Cash               int
	Kills              int
	Deaths             int
	Wins               int
	Losses             int
	Draws              int
	HeadshotKills      int
	SpecialWeaponKills int
	WolfKills          int
	PlayTimeSeconds    uint32
}

// ModerationState describes one account sanction. A zero ExpiresAt means a
// permanent sanction; inactive states clear all associated metadata.
type ModerationState struct {
	Active    bool
	StartedAt time.Time
	ExpiresAt time.Time
	Reason    string
}

func (state ModerationState) ActiveAt(now time.Time) bool {
	return state.Active && (state.ExpiresAt.IsZero() || now.Before(state.ExpiresAt))
}

type MatchOutcome byte

const (
	MatchDraw MatchOutcome = iota
	MatchWin
	MatchLoss
)

// MatchOutcomeStore persists all three match-result counters. It is an
// optional extension so smaller UserStore implementations can retain the
// historical win/loss-only boundary.
type MatchOutcomeStore interface {
	RegisterMatchOutcome(context.Context, int, MatchOutcome) error
}

// PlayTimeStore atomically adds completed-match play time and returns the
// account's new cumulative number of seconds.
type PlayTimeStore interface {
	AddPlayTime(context.Context, int, uint32) (uint32, error)
}

// ModerationStore persists account sanctions. Callsigns are matched
// case-insensitively.
type ModerationStore interface {
	SetMutedByCallsign(context.Context, string, ModerationState) (bool, error)
	SetBannedByCallsign(context.Context, string, ModerationState) (bool, error)
}

type Item struct {
	ID       int
	Type     int
	Upgrade  int
	Expiry   string
	Quantity int
}

type CountedItemUse struct {
	UniqueID  int
	Remaining int
}

// CountedItemStore consumes permanent quantity items reported as used during a
// match.
type CountedItemStore interface {
	ConsumeCountedItems(context.Context, int, []int) ([]CountedItemUse, error)
}

type PurchaseComponent struct {
	ItemID int
	Days   int
}

type PurchaseOrder struct {
	UserID     int
	ItemID     int
	ItemType   int
	Days       int
	Quantity   int // Zero means one; quantity purchases use permanent stacked rows.
	Currency   int
	Price      uint32
	Components []PurchaseComponent
}

type PurchaseReceipt struct {
	Success bool
	Expiry  string
	Gold    int
	WC      int
	Cash    int
	Error   string
}

type LuckyBoxReceipt struct {
	Success    bool
	BoxUnique  int
	RewardItem int
	Days       int
	Remaining  int
	Error      string
}

type SellResult struct {
	Success bool
	Gold    int
	Cash    int
}

type RefundResult struct {
	Success      bool
	Gold         int
	WC           int
	Cash         int
	RefundedGold int
	RefundedWC   int
	RefundedCash int
}

type PermanentTicketInfo struct {
	Valid      bool
	TargetType int
	Eligible   []Item
}

type BasketPurchaseResult struct {
	Success bool
	Items   []PurchaseReceipt
	Gold    int
	WC      int
	Cash    int
}

type ForgeRecipe struct {
	Category   byte
	Challenge  byte
	Choice     byte
	CostCash   int
	RewardItem int
	RewardDays int
}

type ForgeResult struct {
	// Status is the 2012 CS_IN_FORGE_CHALLENGE_ACK result code when nonzero:
	// 98 = not enough currency, 97 = generic failure (string 9457).
	Status     byte
	Outcome    byte
	Cash       int
	RewardItem int
	RewardDays int
}

type ForgeAttempt struct {
	UserID     int
	Category   byte
	Round      byte
	Action     byte
	RewardItem int
	RewardDays int
}

type ForgeWinner struct {
	Callsign   string
	RewardItem int
	Category   byte
	Days       int
	WonAt      time.Time
}

type VIPShopItem struct {
	Section  byte
	Page     byte
	Slot     byte
	GroupID  uint16
	Mode     byte
	ItemID   int
	Currency int
	Days     int
	Price    uint32
}

type Gift struct {
	ID           uint32
	Status       byte
	ItemID       int
	ItemType     int
	Days         int // Duration granted to the inventory item when claimed.
	Quantity     int // Permanent-item units granted when claimed.
	InboxDays    int // Whole days remaining before the mailbox entry expires.
	Counterparty string
	Message      string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type GiftPage struct {
	Gifts      []Gift
	Total      int
	TotalPages int
}

type GiftClaimResult struct {
	Success bool
	Gift    Gift
	Expiry  string
	Gold    int
	Cash    int
}

type GiftSendRequest struct {
	SenderID         int
	ReceiverCallsign string
	ItemID           int
	ItemType         int
	Days             int
	Quantity         int
	Currency         int
	Price            uint32
	Message          string
	Components       []PurchaseComponent
}

type GiftSendResult struct {
	Success bool
	Gold    int
	WC      int
	Cash    int
	Error   string
}

type CharacterSelection struct {
	RedCharacter  int
	BlueCharacter int
}

const CharacterEquipmentSlots = 16

type CharacterEquipment [CharacterEquipmentSlots]int

// CharacterEquipmentStore persists the sixteen character-item unique IDs
// carried by each equipment selector record.
type CharacterEquipmentStore interface {
	CharacterEquipment(context.Context, int) (map[int]CharacterEquipment, error)
	SetCharacterEquipment(context.Context, int, int, CharacterEquipment) error
}

type CrestProgress struct {
	CrestID  uint16
	Progress uint32
}

type TutorialProgress struct {
	HumanLevel byte
	WolfLevel  byte
}

type TutorialReward struct {
	ItemID int
	Days   int
}

// ActiveItemStore exposes timed inventory entitlements to gameplay systems.
// Inventory ownership is enough for EX items such as Power User and Power
// Master; they are not installed into a weapon/equipment slot.
type ActiveItemStore interface {
	HasActiveItem(context.Context, int, int) (bool, error)
}

// ExpiredInventoryStore removes expired ownership rows and returns pending
// notifications in protocol-sized batches.
type ExpiredInventoryStore interface {
	TakeExpiredItems(context.Context, int, int) ([]int, error)
}

// EquipmentBatchStore persists all weapon and power-slot changes from one
// equipment packet atomically. Implementations must not retain the updates
// map after the call returns.
type EquipmentBatchStore interface {
	SetEquipmentBatch(context.Context, int, map[int]int) error
}

// AccountRepository owns authentication and the account projection shown by
// the game protocol. Authentication always verifies the supplied password.
type AccountRepository interface {
	Login(context.Context, string, string) (int, error)
	AccountSnapshot(context.Context, int) (AccountSnapshot, error)
}

// InventoryRepository owns item grants, consumption, equipment, and all
// mutations of a player's inventory.
type InventoryRepository interface {
	Items(context.Context, int) ([]Item, error)
	AddItem(context.Context, int, int, int) error
	RemoveInventoryItem(context.Context, int, int) (bool, error)
	ConsumeInventoryItem(context.Context, int, int) (bool, error)
	ClaimPackageContents(context.Context, int, int) (bool, error)
	DrawLuckyBox(context.Context, int, int, int) (LuckyBoxReceipt, error)
	SetItemUpgrade(context.Context, int, int, byte, uint16) (bool, error)
	SellItem(context.Context, int, int) (SellResult, error)
	RefundItem(context.Context, int, int) (RefundResult, error)
	PermanentItemTicketInfo(context.Context, int, int) (PermanentTicketInfo, error)
	UsePermanentItemTicket(context.Context, int, int, int) (bool, error)
	SyncSlotVouchers(context.Context, int) error
	SetEquipment(context.Context, string, int, int) error
	Equipment(context.Context, int) ([34]int, error)
	PowerSlots(context.Context, int) ([10]bool, error)
}

// CommerceRepository groups transactional shop, forge, and gift operations.
type CommerceRepository interface {
	CommitPurchase(context.Context, PurchaseOrder) (PurchaseReceipt, error)
	PurchaseBasket(context.Context, []PurchaseOrder) (BasketPurchaseResult, error)
	DailyShopItems(context.Context, time.Time) ([2]uint16, error)
	ForgeRecipes(context.Context, byte) ([]ForgeRecipe, error)
	RunForgeChallenge(context.Context, ForgeAttempt) (ForgeResult, error)
	ForgeWinners(context.Context, byte) ([]ForgeWinner, error)
	VIPShopItems(context.Context, byte, byte) ([]VIPShopItem, error)
	GiftCount(context.Context, int) (int, error)
	Gifts(context.Context, int, byte, int, int) (GiftPage, error)
	GiftDetail(context.Context, int, uint32, byte) (Gift, bool, error)
	ClaimGift(context.Context, int, uint32) (GiftClaimResult, error)
	SendGift(context.Context, GiftSendRequest) (GiftSendResult, error)
}

// ProgressRepository records match outcomes, rewards, and account progression.
type ProgressRepository interface {
	RecordKill(context.Context, int, int) error
	RegisterMatchResult(context.Context, int, bool) error
	CrestProgress(context.Context, int) ([]CrestProgress, error)
	SetCrestProgress(context.Context, int, uint16, uint32) error
	TutorialProgress(context.Context, int) (TutorialProgress, error)
	TutorialRewards(context.Context, byte, byte) ([]TutorialReward, error)
	ClaimTutorialReward(context.Context, int, byte, byte) ([]Item, TutorialProgress, error)
	CreditMatchRewards(context.Context, int, int, int) error
}

// CharacterRepository owns character selection and character-scoped slots.
type CharacterRepository interface {
	SetCharacterSlot(context.Context, int, int, int) error
	CharacterSlots(context.Context, int) (map[int]int, error)
	ReplaceCharacterSlots(context.Context, int, map[int]int) error
	CharacterSelection(context.Context, int) (CharacterSelection, error)
	SaveCharacterSelection(context.Context, int, CharacterSelection) error
	SetEquippedTitle(context.Context, int, byte, string) error
}

// GameRepository is the composed persistence boundary used by the game
// service. Smaller subsystems can depend on one of the focused contracts.
type GameRepository interface {
	AccountRepository
	InventoryRepository
	CommerceRepository
	ProgressRepository
	CharacterRepository
}

// levelThresholds contains the configured numerical progression.
var levelThresholds = [...]int{
	0,
	2000, 2500, 3500, 5000,
	7000, 9500, 12500, 16000,
	20000, 24500, 29500, 35000,
	41000, 47500, 54500, 62000,
	70000, 79000, 89000, 101000,
	116000, 135000, 159000, 189000,
	226000, 271000, 325000, 389000,
	464000, 551000, 641000, 745000,
	865000, 1010000, 1180000, 1375000,
	1595000, 1845000, 2125000, 2435000,
	2775000, 3150000, 3560000, 4005000,
	4485000, 5010000, 5580000, 6195000,
	6855000, 7570000, 8340000, 9165000,
	10045000, 10990000, 12000000, 13075000,
	14767500, 17306250, 20691250, 24922500,
}

// NextLevelThreshold returns the GP required to advance from the supplied
// grade. Class serials run in reverse: 61 is Recruit and 1 is the highest
// configured grade. Grade 62 is reserved and does not advance.
func NextLevelThreshold(level int) int {
	if level >= 62 {
		return 0
	}
	if level <= 1 {
		return levelThresholds[len(levelThresholds)-1]
	}
	return levelThresholds[62-level]
}

// LevelBaseThreshold is the cumulative GP at which a grade starts: the gauge
// base the client stores at +2156 and draws as (GP - base) / (next - base)
// (sub_3415D830). GP is cumulative: GAMEPOINT_ACK (sub_342067D0) computes the
// match gain as new GP - previous GP, so GP never resets on promotion.
func LevelBaseThreshold(level int) int {
	if level >= 61 || level < 1 {
		return 0
	}
	return levelThresholds[61-level]
}

// applyRankProgress promotes through every grade the cumulative GP reaches.
func applyRankProgress(level, gp int) (int, int) {
	for level > 1 && level < 62 && gp >= NextLevelThreshold(level) {
		level--
	}
	return level, gp
}
