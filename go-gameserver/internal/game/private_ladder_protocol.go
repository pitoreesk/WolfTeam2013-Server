package game

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type PrivateLadderRankRequest struct {
	Page     byte
	PageSize byte
	TopSize  byte
	Scope    byte
}

func ParsePrivateLadderRankRequest(payload []byte) (PrivateLadderRankRequest, error) {
	if len(payload) < 4 {
		return PrivateLadderRankRequest{}, fmt.Errorf("private-ladder rank request must contain four bytes")
	}
	return PrivateLadderRankRequest{Page: payload[0], PageSize: payload[1], TopSize: payload[2], Scope: payload[3]}, nil
}

// PrivateLadderSearchRequest is CS_LD_PRIVATE_FINDUSER_START_REQ 0x1B07
// (sub_342146E0): the room mode word, map and capacity the player selected in
// the ladder dialog. A "random/all" choice (a mode word carrying every ladder
// bit, 0x241A) is sent as mode 0xFFFFFFFF and map 0xFF; the capacity byte is
// 0xFF except in a private-ladder (type 4) channel.
type PrivateLadderSearchRequest struct {
	ModeWord uint32
	Map      byte
	Capacity byte
}

const (
	ladderAnyMode     = uint32(0xffffffff)
	ladderAnyByte     = byte(0xff)
	ladderAllModesSet = uint32(0x241a) // union of the four ladder modes (sub_342146E0 test)
)

// ladderModes are the ladder room modes (modelist.dat LADDER_*): deathmatch
// wolf/human and point wolf/human.
var ladderModes = []uint32{0x40a, 0x412, 0x2408, 0x2410}

func (r PrivateLadderSearchRequest) AnyMode() bool { return r.ModeWord == ladderAnyMode }

// Compatible reports whether two searches can meet: every field is equal or a
// wildcard on either side.
func (r PrivateLadderSearchRequest) Compatible(other PrivateLadderSearchRequest) bool {
	if !r.AnyMode() && !other.AnyMode() && r.ModeWord != other.ModeWord {
		return false
	}
	if r.Map != ladderAnyByte && other.Map != ladderAnyByte && r.Map != other.Map {
		return false
	}
	if r.Capacity != ladderAnyByte && other.Capacity != ladderAnyByte && r.Capacity != other.Capacity {
		return false
	}
	return true
}

func ParsePrivateLadderSearchRequest(payload []byte) (PrivateLadderSearchRequest, error) {
	if len(payload) < 6 {
		return PrivateLadderSearchRequest{}, fmt.Errorf("private-ladder search request must contain six bytes")
	}
	return PrivateLadderSearchRequest{
		ModeWord: binary.LittleEndian.Uint32(payload[:4]),
		Map:      payload[4],
		Capacity: payload[5],
	}, nil
}

// LadderOptionMode is one mode of the 0x1B16 ladder option list.
type LadderOptionMode struct {
	ModeWord uint32
	Maps     []LadderOptionMap
}

// LadderOptionMap is a map allowed for a mode and the capacities offered on
// it as a bit mask: the dialog picks a random set bit n and creates the room
// for 2*(n+1) players (sub_342B7870), so bit 0 is 1 vs 1 and bit 1 is 2 vs 2.
// The room master's client starts the match itself once the room is full and
// every other player is ready (0x163D -> sub_34214400), so the capacity must
// match the players the search pairs.
type LadderOptionMap struct {
	Map        byte
	Capacities byte
}

// LadderOptionsPayload encodes the 0x1B16 ladder option list (sub_342B9960,
// WLadderSection only): u16 n modes; per mode u32 mode word, u8 n maps, n x
// (u8 map, u8 capacity mask). The client keeps only maps it knows for the mode
// (sub_341AB820). Receiving it enables the ladder dialog (WLadderSection +1329),
// which the "EŞLEŞME BAŞLASIN" button needs before it sends FINDUSER_START.
func LadderOptionsPayload(modes []LadderOptionMode) ([]byte, error) {
	if len(modes) > 0xffff {
		return nil, fmt.Errorf("ladder option mode count exceeds two bytes")
	}
	writer := protocol.NewWriter(8 + len(modes)*140).U8(0).U16(uint16(len(modes)))
	for _, mode := range modes {
		if len(mode.Maps) > 255 {
			return nil, fmt.Errorf("ladder option map count exceeds one byte")
		}
		writer.U32(mode.ModeWord).U8(byte(len(mode.Maps)))
		for _, option := range mode.Maps {
			writer.U8(option.Map).U8(option.Capacities)
		}
	}
	return writer.Clone(), nil
}

// LadderRoomListPayload is FINDUSER_START_ACK 0x1B08 for a ladder (type 3)
// channel (sub_34214A00): u16 n, n x u16 room. The client enters each room it
// is given; with n = 0 it creates its own room from the dialog (CREATE_REQ).
func LadderRoomListPayload(rooms []int16) []byte {
	writer := protocol.NewWriter(3 + len(rooms)*2).U8(0).U16(uint16(len(rooms)))
	for _, room := range rooms {
		writer.U16(uint16(room))
	}
	return writer.Clone()
}

func ParsePrivateLadderSearchEndRequest(payload []byte) (byte, error) {
	if len(payload) < 1 {
		return 0, fmt.Errorf("private-ladder search-end request must contain one byte")
	}
	return payload[0], nil
}

func PrivateLadderSearchEndPayload(status byte) []byte {
	return protocol.NewWriter(2).U8(0).U8(status).Clone()
}

func PrivateLadderNotifyPayload(active bool) []byte {
	return protocol.NewWriter(2).U8(0).Bool(active).Clone()
}

func PrivateLadderLevelPayload() []byte { return []byte{0} }

func LoginRestrictPayload(restriction byte) []byte {
	return protocol.NewWriter(2).U8(0).U8(restriction).Clone()
}

func ParsePrivateChallengeRequest(payload []byte) (string, error) {
	target, offset, err := readPrideUTF16(payload, 0)
	if err != nil || offset > len(payload) || target == "" {
		return "", fmt.Errorf("private challenge target is invalid")
	}
	return target, nil
}

type PrivateChallengeResponse struct {
	Token      uint16
	Challenger string
	Rule       uint16
	Decision   byte
}

func ParsePrivateChallengeResponse(payload []byte) (PrivateChallengeResponse, error) {
	if len(payload) < 3 {
		return PrivateChallengeResponse{}, fmt.Errorf("private challenge response is too short")
	}
	result := PrivateChallengeResponse{Token: binary.LittleEndian.Uint16(payload[:2])}
	challenger, offset, err := readPrideUTF16(payload, 2)
	if err != nil || len(payload) < offset+3 {
		return PrivateChallengeResponse{}, fmt.Errorf("private challenge response is malformed")
	}
	result.Challenger = challenger
	// Client sends: decision(u8) then rule/detail(u16)
	result.Decision = payload[offset]
	result.Rule = binary.LittleEndian.Uint16(payload[offset+1 : offset+3])
	return result, nil
}

type PrivateChallengeResultRequest struct {
	Token    uint16
	Decision byte
}

// The result may contain only the decision or prefix it with the pending
// challenge token.
func ParsePrivateChallengeResultRequest(payload []byte) (PrivateChallengeResultRequest, error) {
	if len(payload) < 1 {
		return PrivateChallengeResultRequest{}, fmt.Errorf("private challenge-result request is empty")
	}
	if len(payload) >= 3 {
		return PrivateChallengeResultRequest{Token: binary.LittleEndian.Uint16(payload[:2]), Decision: payload[2]}, nil
	}
	return PrivateChallengeResultRequest{Decision: payload[0]}, nil
}

func PrivateLadderInfoPayload(info store.PrivateLadderInfo) []byte {
	return protocol.NewWriter(9).U8(0).U32(info.Points).U32(info.Rank).Clone()
}

func PrivateLadderRanksPayload(top, page []store.PrivateLadderRank) ([]byte, error) {
	if len(top) > 255 || len(page) > 255 {
		return nil, fmt.Errorf("private-ladder rank list exceeds one-byte count")
	}
	writer := protocol.NewWriter(512).U8(0)
	writeRanks := func(ranks []store.PrivateLadderRank) {
		writer.U8(byte(len(ranks)))
		for _, rank := range ranks {
			writePrideASCII(writer, rank.Username, 16)
			writePrideUTF16(writer, rank.Callsign, 24)
			writer.U8(rank.Level).U32(rank.PrideMarkID).U32(rank.Points).U32(rank.Rank).U8(rank.Status)
		}
	}
	writeRanks(top)
	writeRanks(page)
	return writer.Clone(), nil
}

func PrivateLadderNoticePayload(notice store.PrivateLadderNotice) ([]byte, error) {
	writer := protocol.NewWriter(256).U8(0)
	writePrideUTF16(writer, notice.Title, 63)
	writeLadderNoticeStyle(writer)
	writePrideASCII(writer, ladderNoticeDate(notice.StartsOn), 31)
	writePrideASCII(writer, ladderNoticeDate(notice.EndsOn), 31)
	writeLadderNoticeStyle(writer)
	if err := writeLadderWide16(writer, notice.Summary, 2047); err != nil {
		return nil, err
	}
	writeLadderNoticeStyle(writer)
	if err := writeLadderWide16(writer, notice.Details, 2047); err != nil {
		return nil, err
	}
	writeLadderNoticeStyle(writer)
	return writer.Clone(), nil
}

func writeLadderNoticeStyle(writer *protocol.Writer) {
	writer.U32(0).U8(0).U8(0)
}

func writeLadderWide16(writer *protocol.Writer, value string, maximumUnits int) error {
	value = truncateUTF16(value, maximumUnits)
	units := utf16.Encode([]rune(value))
	if len(units)*2 > 65535 {
		return fmt.Errorf("private-ladder notice text exceeds two-byte length")
	}
	writer.U16(uint16(len(units) * 2)).UTF16LE(value)
	return nil
}

func ladderNoticeDate(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format("20060102")
}

type PrivateLadderPeer struct {
	GlobalIP   netip.Addr
	GlobalPort uint16
	LocalIP    netip.Addr
	LocalPort  uint16
}

func PrivateLadderSearchPayload(roomNumber *int16, peers []PrivateLadderPeer, includeMatch bool) ([]byte, error) {
	writer := protocol.NewWriter(64).U8(0)
	if roomNumber == nil {
		writer.U16(0)
		return writer.Clone(), nil
	}
	// count=1 matched opponent + roomNumber
	writer.U16(1).U16(uint16(*roomNumber))
	if len(peers) > 255 {
		return nil, fmt.Errorf("private-ladder peer count exceeds one byte")
	}
	// sub_3420BFF0 reads: peerCount(u8) + per peer endpoints + matchFound(u8)
	writer.U8(byte(len(peers)))
	for _, peer := range peers {
		if err := writeLadderEndpoint(writer, peer.GlobalIP, peer.GlobalPort); err != nil {
			return nil, err
		}
		if err := writeLadderEndpoint(writer, peer.LocalIP, peer.LocalPort); err != nil {
			return nil, err
		}
	}
	// MatchFound byte: 1 = enter room, 0 = just notify
	matchByte := byte(0)
	if includeMatch {
		matchByte = 1
	}
	writer.U8(matchByte)
	return writer.Clone(), nil
}

func writeLadderEndpoint(writer *protocol.Writer, address netip.Addr, port uint16) error {
	if !address.Is4() {
		return fmt.Errorf("private-ladder endpoint must be IPv4")
	}
	bytes := address.As4()
	writer.Bytes(bytes[:]).U16(port)
	return nil
}

func PrivateChallengeAckPayload(status uint16) []byte {
	return protocol.NewWriter(3).U8(0).U16(status).Clone()
}

func PrivateChallengeReceivePayload(status, token uint16, username, callsign string, level byte, points, wins, losses uint32) ([]byte, error) {
	writer := protocol.NewWriter(64).U8(0).U16(status).U16(token)
	writePrideASCII(writer, username, 16)
	writePrideUTF16(writer, callsign, 24)
	writer.U8(level).U32(points).U32(wins).U32(losses)
	return writer.Clone(), nil
}

func PrivateChallengeResultPayload(status uint16, result byte, detail uint16) []byte {
	writer := protocol.NewWriter(6).U8(0).U16(status).U8(result)
	if result == 1 {
		writer.U16(detail)
	}
	return writer.Clone()
}

// ladderCapacityOneVsOne offers 1 vs 1 rooms (capacity 2).
const ladderCapacityOneVsOne byte = 0x01
