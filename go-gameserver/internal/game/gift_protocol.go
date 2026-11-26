package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

const (
	GiftMailboxCapacity = 100
	GiftPageSize        = 13
)

type GiftListRequest struct {
	Kind     byte
	PageSize byte
	Page     byte
}

func ParseGiftListRequest(payload []byte) (GiftListRequest, error) {
	if len(payload) < 3 {
		return GiftListRequest{}, fmt.Errorf("gift-list payload is too short")
	}
	request := GiftListRequest{Kind: payload[0], PageSize: payload[1], Page: payload[2]}
	if request.Kind > 1 {
		return GiftListRequest{}, fmt.Errorf("gift-list kind %d is invalid", request.Kind)
	}
	if request.PageSize == 0 || request.PageSize > GiftPageSize {
		return GiftListRequest{}, fmt.Errorf("gift-list page size %d is invalid", request.PageSize)
	}
	return request, nil
}

type GiftDetailRequest struct {
	GiftID uint32
	Kind   byte
}

type GiftSendRequest struct {
	ItemID           int
	Days             int
	ReceiverCallsign string
	Message          string
	ItemType         int
	Price            uint32
	WolfCoinReward   uint32
}

// ParseGiftSendRequest parses user-entered UTF-16 fields followed by the
// selected price and the AP purchase's WolfCoin reward.
func ParseGiftSendRequest(payload []byte) (GiftSendRequest, error) {
	if len(payload) < 15 {
		return GiftSendRequest{}, fmt.Errorf("send-gift payload is too short")
	}
	request := GiftSendRequest{
		ItemID: int(binary.LittleEndian.Uint16(payload[0:2])),
		Days:   int(binary.LittleEndian.Uint16(payload[2:4])),
	}
	var err error
	offset := 4
	request.ReceiverCallsign, offset, err = readChannelUTF16(payload, offset)
	if err != nil {
		return GiftSendRequest{}, fmt.Errorf("parse gift receiver: %w", err)
	}
	request.Message, offset, err = readChannelUTF16(payload, offset)
	if err != nil {
		return GiftSendRequest{}, fmt.Errorf("parse gift message: %w", err)
	}
	if offset+9 > len(payload) {
		return GiftSendRequest{}, fmt.Errorf("send-gift price fields exceed payload")
	}
	request.ItemType = int(payload[offset])
	request.Price = binary.LittleEndian.Uint32(payload[offset+1 : offset+5])
	request.WolfCoinReward = binary.LittleEndian.Uint32(payload[offset+5 : offset+9])
	if request.ItemID < 1 || request.Days > 3650 || request.ReceiverCallsign == "" {
		return GiftSendRequest{}, fmt.Errorf("send-gift identifiers are invalid")
	}
	if request.ItemType < 0 || request.ItemType > 1 {
		return GiftSendRequest{}, fmt.Errorf("send-gift item type %d is invalid", request.ItemType)
	}
	return request, nil
}

func GiftSendPayment(request GiftSendRequest) (currency int, price uint32, err error) {
	if request.Price == 0 {
		return 0, 0, fmt.Errorf("send-gift packet has no price")
	}
	if request.WolfCoinReward > 0 {
		return 1, request.Price, nil
	}
	return 4, request.Price, nil
}

func ParseGiftDetailRequest(payload []byte) (GiftDetailRequest, error) {
	if len(payload) < 5 {
		return GiftDetailRequest{}, fmt.Errorf("gift-detail payload is too short")
	}
	request := GiftDetailRequest{GiftID: binary.LittleEndian.Uint32(payload[:4]), Kind: payload[4]}
	if request.GiftID == 0 || request.Kind > 1 {
		return GiftDetailRequest{}, fmt.Errorf("gift-detail identifiers are invalid")
	}
	return request, nil
}

func ParseGiftClaimRequest(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("gift-claim payload is too short")
	}
	giftID := binary.LittleEndian.Uint32(payload[:4])
	if giftID == 0 {
		return 0, fmt.Errorf("gift ID %d is invalid", giftID)
	}
	return giftID, nil
}

// ParseGiftReceiverCheckRequest parses a byte-counted UTF-16LE nickname. The
// rest of the encrypted frame is padding and is intentionally ignored.
func ParseGiftReceiverCheckRequest(payload []byte) (string, error) {
	callSign, _, err := readChannelUTF16(payload, 0)
	if err != nil {
		return "", fmt.Errorf("parse gift receiver check: %w", err)
	}
	if callSign == "" {
		return "", fmt.Errorf("gift receiver nickname is empty")
	}
	return callSign, nil
}

// GiftReceiverCheckPayload carries a leading success byte followed by whether
// the nickname exists.
func GiftReceiverCheckPayload(found bool) []byte {
	if found {
		return []byte{0, 1}
	}
	return []byte{0, 0}
}

// SendGiftPayload carries status, gold, and cash.
func SendGiftPayload(status byte, result store.GiftSendResult) []byte {
	return protocol.NewWriter(9).U8(status).
		U32(uint32(max(result.Gold, 0))).U32(uint32(max(result.Cash, 0))).Clone()
}

// GiftItemCountPayload carries inbox capacity and item count.
func GiftItemCountPayload(capacity, count int) []byte {
	// The common ACK layer consumes the leading success byte.
	return protocol.NewWriter(5).U8(0).U16(uint16(min(max(capacity, 0), 65535))).U16(uint16(min(max(count, 0), 65535))).Clone()
}

// GiftPeriodEndPayload carries a 16-bit summary and a counted list of
// (16-bit item ID, 8-bit period state) records after the common status.
func GiftPeriodEndPayload() []byte {
	return protocol.NewWriter(4).U8(0).U16(0).U8(0).Clone()
}

// GiftItemListPayload encodes up to thirteen records and stores creation dates
// as eight-digit strings.
func GiftItemListPayload(request GiftListRequest, page store.GiftPage) ([]byte, error) {
	if page.Total < 0 || page.Total > 65535 || page.TotalPages < 0 || page.TotalPages > 255 || len(page.Gifts) > GiftPageSize {
		return nil, fmt.Errorf("gift page exceeds protocol limits")
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(7 + len(page.Gifts)*32).U8(0).U8(request.Kind).U8(request.Page).U8(byte(page.TotalPages)).U16(uint16(page.Total)).U8(byte(len(page.Gifts)))
	for _, gift := range page.Gifts {
		if gift.ID == 0 || gift.ItemID < 1 || gift.ItemID > 65535 || gift.InboxDays < 0 || gift.InboxDays > 65535 || gift.Status > 2 {
			return nil, fmt.Errorf("gift %d contains invalid protocol fields", gift.ID)
		}
		counterparty := truncateUTF16(gift.Counterparty, 31)
		writer.U32(gift.ID).U16(uint16(gift.ItemID)).U16(uint16(gift.InboxDays)).
			U8(byte(len(utf16.Encode([]rune(counterparty))) * 2)).UTF16LE(counterparty).
			U8(gift.Status).U8(8).ASCII(gift.CreatedAt.UTC().Format("20060102"))
	}
	return writer.Clone(), nil
}

// GiftDetailPayload carries a raw YYYYMMDD field and allows claiming only for
// received status 0 or 1.
// GiftDetailPayload encodes CS_GI_DETAILLIST_ACK (0x1A05). The 2013 parser
// (sub_3425B990) ends with u32 WC and u32 Gold conversion values: a non-zero
// WC shows the convert button with string 9459 ("%d KP"), otherwise a
// non-zero Gold shows it with 9460 ("%d Altın"); the sent-gifts tab (kind 1)
// never shows it. refundWC/refundGold come from giftRefundValue.
func GiftDetailPayload(kind byte, gift store.Gift, found bool, refund ...int) ([]byte, error) {
	if kind > 1 {
		return nil, fmt.Errorf("gift-detail kind %d is invalid", kind)
	}
	if !found {
		gift = store.Gift{Status: 2, InboxDays: -1}
	}
	counterparty := truncateUTF16(gift.Counterparty, 31)
	message := truncateUTF16(gift.Message, 127)
	counterpartyBytes := len(utf16.Encode([]rune(counterparty))) * 2
	messageBytes := len(utf16.Encode([]rune(message))) * 2
	displayCount := 0
	if found {
		displayCount = gift.Days
		if displayCount == 0 {
			displayCount = max(gift.Quantity, 1)
		}
		displayCount = min(displayCount, 0xffff)
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(25 + counterpartyBytes + messageBytes).U8(0).U8(kind).U32(gift.ID).
		U16(uint16(max(gift.ItemID, 0))).U16(uint16(displayCount)).
		U8(gift.Status).U8(byte(counterpartyBytes)).UTF16LE(counterparty)
	if found {
		writer.U8(8).ASCII(gift.CreatedAt.UTC().Format("20060102"))
	} else {
		writer.U8(0)
	}
	refundWC, refundGold := 0, 0
	if found && kind == 0 && len(refund) == 2 {
		refundWC, refundGold = max(refund[0], 0), max(refund[1], 0)
	}
	writer.U8(byte(messageBytes)).UTF16LE(message).U8(byte(max(gift.ItemType, 0))).
		U32(uint32(refundWC)).U32(uint32(refundGold))
	return writer.Clone(), nil
}

// GiftUsePayload carries balance fields followed by a standard item record.
func GiftUsePayload(result store.GiftClaimResult) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(41).U8(0).U32(uint32(max(result.Gold, 0))).U32(uint32(max(result.Cash, 0)))
	if !result.Success {
		writer.U32(0).U16(0).U8(0).U16(0).U8(0).U8(0).U8(0)
		return writer.Clone()
	}
	writer.U32(uint32(2_000_000_000 + result.Gift.ItemID)).U16(uint16(result.Gift.ItemID))
	expiry := expiryStampFrom(result.Expiry)
	useCount := max(result.Gift.Days, 0)
	if expiry.unlimited() {
		useCount = max(result.Gift.Quantity, 1)
	}
	writer.U8(byte(len(expiry))).ASCII(string(expiry)).U16(uint16(min(useCount, 0xffff))).
		U8(byte(result.Gift.ItemType)).U8(2).U8(0)
	return writer.Clone()
}

func truncateUTF16(value string, maximumUnits int) string {
	if maximumUnits < 1 {
		return ""
	}
	units := utf16.Encode([]rune(value))
	if len(units) <= maximumUnits {
		return value
	}
	return string(utf16.Decode(units[:maximumUnits]))
}

// GiftRefundReplyPayload encodes CS_GI_REFUND_ACK (0x1A11, sub_34209720):
// i32 WC then i32 Gold received. WC > 0 shows 9461 ("%d KP Aldın"), else Gold
// > 0 shows 9462; closing that box refreshes the gift box. 0/0 shows nothing.
func GiftRefundReplyPayload(wc, gold int) []byte {
	return protocol.NewWriter(9).U8(0).U32(uint32(max(wc, 0))).U32(uint32(max(gold, 0))).Clone()
}
