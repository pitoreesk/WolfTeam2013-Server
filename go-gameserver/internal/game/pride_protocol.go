package game

import (
	"encoding/binary"
	"fmt"
	"time"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type PrideCreateRequest struct {
	Name  string
	Intro string
}

func ParsePrideCreateRequest(payload []byte) (PrideCreateRequest, error) {
	name, offset, err := readPrideUTF16(payload, 0)
	if err != nil {
		return PrideCreateRequest{}, err
	}
	intro, _, err := readPrideUTF16(payload, offset)
	if err != nil {
		return PrideCreateRequest{}, err
	}
	if name == "" {
		return PrideCreateRequest{}, fmt.Errorf("pride name is empty")
	}
	return PrideCreateRequest{Name: name, Intro: intro}, nil
}

type PrideListRequest struct {
	Name   string
	Master string
	Mode   byte
	Page   uint16
}

func ParsePrideListRequest(payload []byte) (PrideListRequest, error) {
	name, offset, err := readPrideUTF16(payload, 0)
	if err != nil {
		return PrideListRequest{}, err
	}
	master, offset, err := readPrideUTF16(payload, offset)
	if err != nil || len(payload) < offset+3 {
		return PrideListRequest{}, fmt.Errorf("pride-list payload is incomplete")
	}
	return PrideListRequest{Name: name, Master: master, Mode: payload[offset], Page: binary.LittleEndian.Uint16(payload[offset+1 : offset+3])}, nil
}

func ParsePrideIDRequest(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("pride ID payload is too short")
	}
	return binary.LittleEndian.Uint32(payload[:4]), nil
}

// PrideBattleListRequest is the exact CS_PR_BATTLELIST_REQ payload observed
// in Cshell at 0x34247B50: PrideID followed by two one-byte selectors. Their
// business names are not recoverable from the stripped client, so they remain
// neutral here rather than being guessed.
type PrideBattleListRequest struct {
	PrideID uint32
	Arg1    byte
	Arg2    byte
}

func ParsePrideBattleListRequest(payload []byte) (PrideBattleListRequest, error) {
	if len(payload) < 6 {
		return PrideBattleListRequest{}, fmt.Errorf("pride battle-list payload is too short")
	}
	return PrideBattleListRequest{
		PrideID: binary.LittleEndian.Uint32(payload[:4]),
		Arg1:    payload[4],
		Arg2:    payload[5],
	}, nil
}

type PrideInfoRequest struct {
	ID   uint32
	Mode byte
}

func ParsePrideInfoRequest(payload []byte) (PrideInfoRequest, error) {
	if len(payload) < 5 {
		return PrideInfoRequest{}, fmt.Errorf("pride-info payload is too short")
	}
	return PrideInfoRequest{ID: binary.LittleEndian.Uint32(payload[:4]), Mode: payload[4]}, nil
}

type PrideAcceptRequest struct {
	Username string
	Callsign string
	Accept   bool
	Mode     byte
	PrideID  uint32
}

func ParsePrideAcceptRequest(payload []byte) (PrideAcceptRequest, error) {
	username, offset, err := readPrideASCII(payload, 0)
	if err != nil {
		return PrideAcceptRequest{}, err
	}
	callsign, offset, err := readPrideUTF16(payload, offset)
	if err != nil || len(payload) < offset+6 {
		return PrideAcceptRequest{}, fmt.Errorf("accept-pride payload is incomplete")
	}
	return PrideAcceptRequest{Username: username, Callsign: callsign, Accept: payload[offset] != 0, Mode: payload[offset+1], PrideID: binary.LittleEndian.Uint32(payload[offset+2 : offset+6])}, nil
}

func ParsePrideUsernameRequest(payload []byte) (string, error) {
	username, _, err := readPrideASCII(payload, 0)
	return username, err
}

type PrideMemoRequest struct {
	PrideID uint32
	Message string
}

func ParsePrideMemoRequest(payload []byte) (PrideMemoRequest, error) {
	if len(payload) < 5 {
		return PrideMemoRequest{}, fmt.Errorf("pride memo payload is too short")
	}
	message, _, err := readPrideUTF16(payload, 4)
	if err != nil {
		return PrideMemoRequest{}, err
	}
	return PrideMemoRequest{PrideID: binary.LittleEndian.Uint32(payload[:4]), Message: message}, nil
}

func ParsePrideMemoDeleteRequest(payload []byte) (uint32, uint32, error) {
	if len(payload) < 8 {
		return 0, 0, fmt.Errorf("delete-pride-memo payload is too short")
	}
	return binary.LittleEndian.Uint32(payload[:4]), binary.LittleEndian.Uint32(payload[4:8]), nil
}

func ParsePrideMessagesRequest(payload []byte) (string, string, error) {
	intro, offset, err := readPrideUTF16(payload, 0)
	if err != nil {
		return "", "", err
	}
	notice, _, err := readPrideUTF16(payload, offset)
	return intro, notice, err
}

// prideGrade is the account Pride grade byte (+793). The 2013 client shows
// the master's controls for grade 0 and the member's for 8: the admin page
// (sub_34328EF0) shows PrideClose for 0 and PrideLeave for 8, and
// CHANGEMASTER_ACK (sub_341777C0) turns 0 into 8 (the old master) and 8 into 0
// for the member whose callsign it carries (the new master). The database
// keeps role 8 for the master. A closing Pride carries its close state
// (>= 10) instead, followed by the close date. USERINFO, CHARSTDINFO,
// GETPRIDEINFO and CREATEPRIDE all write it.
func prideGrade(pride store.Pride, role byte) byte {
	if pride.CloseStatus >= 10 {
		return pride.CloseStatus
	}
	if role == prideRoleMaster {
		return prideGradeMaster
	}
	return prideGradeMember
}

// prideRoleMaster is pride_members.role of the master.
const prideRoleMaster = 8

// Wire grades (see prideGrade).
const (
	prideGradeMaster byte = 0
	prideGradeMember byte = 8
)

// PrideCreatePayload encodes CS_PR_CREATEPRIDE_ACK (sub_34256480): u32 Pride
// id (+1568), name, master, grade (+793, + date when >= 10), intro (appended
// to the "Klan" label), u8 (+708, the member-count byte of the other Pride
// records) and the account's Gold balance (+2124).
func PrideCreatePayload(pride store.Pride, member store.PrideMember, gold int, success bool) []byte {
	if !success {
		writer := protocol.NewWriter(16).
			U8(0).U32(0).
			U8(0).U8(0).U8(0).U8(0).U8(0).
			U32(0)
		return writer.Clone()
	}
	writer := protocol.NewWriter(64).U8(0).U32(pride.ID)
	writePrideUTF16(writer, pride.Name, 31)
	writePrideUTF16(writer, pride.Master, 31)
	grade := prideGrade(pride, member.Role)
	writer.U8(grade)
	if grade > 9 {
		writePrideASCII(writer, prideDate(pride.CloseAt), 31)
	}
	writePrideUTF16(writer, pride.Intro, 127)
	writer.U8(byte(min(max(pride.MemberCount, 1), 255))).
		U32(uint32(max(gold, 0)))
	return writer.Clone()
}

func PrideListPayload(currentPage uint16, page store.PridePage) ([]byte, error) {
	if page.Total > 65535 || page.TotalPages > 65536 || len(page.Prides) > 255 {
		return nil, fmt.Errorf("pride list exceeds protocol limits")
	}
	totalPageIndex := max(page.TotalPages-1, 0)
	writer := protocol.NewWriter(5 + len(page.Prides)*64).
		U8(0).U16(currentPage).U16(uint16(totalPageIndex)).
		U8(byte(len(page.Prides)))
	for _, pride := range page.Prides {
		writer.U32(pride.ID)
		writePrideUTF16(writer, pride.Name, 31)
		writePrideUTF16(writer, pride.Master, 31)
		writePrideUTF16(writer, pride.Intro, 127)
	}
	return writer.Clone(), nil
}

// PrideInfoPayload is the Pride record of 0x1707 (another Pride's page): its
// state byte is the Pride's close state.
func PrideInfoPayload(pride store.Pride, role byte, found bool) []byte {
	return prideInfoPayloadWithState(pride, pride.CloseStatus, found)
}

func prideInfoPayloadWithState(pride store.Pride, state byte, found bool) []byte {
	writer := protocol.NewWriter(96).U8(0)
	if !found {
		writer.U32(0)
		return writer.Clone()
	}
	writer.U32(pride.ID)
	writePrideUTF16(writer, pride.Name, 31)
	writePrideUTF16(writer, pride.Master, 31)
	memberCountByte := byte(min(max(pride.MemberCount, 1), 255))
	writer.U32(uint32(max(pride.RankPoints, 0))).
		U8(memberCountByte)
	writePrideASCII(writer, pride.CreatedAt.UTC().Format("20060102"), 21)
	writer.U16(uint16(min(max(pride.MemberCount, 0), 65535)))
	writePrideUTF16(writer, pride.Intro, 127)
	writePrideUTF16(writer, pride.Notice, 127)
	writer.U8(state)
	if state > 9 {
		writePrideASCII(writer, prideDate(pride.CloseAt), 31)
	}
	return writer.Clone()
}

func PrideApplicationPayload(pride store.Pride) []byte {
	writer := protocol.NewWriter(40).U8(0).U32(pride.ID)
	writePrideUTF16(writer, pride.Name, 31)
	return writer.Clone()
}

func PrideWaitingListPayload(applications []store.PrideApplication) ([]byte, error) {
	if len(applications) > 255 {
		return nil, fmt.Errorf("pride application list exceeds protocol limit")
	}
	writer := protocol.NewWriter(1 + len(applications)*64).
		U8(0).U8(byte(len(applications)))
	for _, application := range applications {
		writePrideASCII(writer, application.Username, 16)
		writePrideUTF16(writer, application.Callsign, 24)
		writer.U8(byte(min(max(application.Level, 0), 255))).
			U32(uint32(max(application.GP, 0))).U32(uint32(max(application.Draws, 0))).
			U32(uint32(max(application.Wins, 0))).
			U32(uint32(max(application.Losses, 0)))
		writePrideASCII(writer, application.AppliedAt.UTC().Format("20060102150405"), 14)
		writePrideASCII(writer, application.LastLogin.UTC().Format("20060102150405"), 14)
	}
	return writer.Clone(), nil
}

func PrideAcceptPayload(pride store.Pride, member store.PrideMember, mode byte, accepted bool) []byte {
	writer := protocol.NewWriter(64).U8(0)
	writePrideASCII(writer, member.Username, 16)
	writePrideUTF16(writer, member.Callsign, 24)
	writer.Bool(accepted).
		U8(member.Role).
		U32(pride.ID).U8(mode)
	writePrideUTF16(writer, pride.Name, 31)
	return writer.Clone()
}

func PrideMemberChangePayload(pride store.Pride, member store.PrideMember, success bool) []byte {
	writer := protocol.NewWriter(64).U8(0)
	writePrideASCII(writer, member.Username, 16)
	writePrideUTF16(writer, member.Callsign, 24)
	writer.U8(member.Role).
		U32(pride.ID).Bool(success)
	writePrideUTF16(writer, pride.Name, 31)
	return writer.Clone()
}

func PrideChangeMasterPayload(pride store.Pride, member store.PrideMember, success bool) []byte {
	writer := protocol.NewWriter(40).U8(0)
	writePrideUTF16(writer, member.Callsign, 31)
	prideID := uint32(0)
	if success {
		prideID = pride.ID
	}
	writer.U32(prideID).Bool(success)
	return writer.Clone()
}

func PrideClosePayload(pride store.Pride, success bool) []byte {
	writer := protocol.NewWriter(12).U8(0)
	if success {
		writer.U8(pride.CloseStatus)
		writePrideASCII(writer, prideDate(pride.CloseAt), 31)
	} else {
		writer.U8(0).U8(0)
	}
	return writer.Clone()
}

func PrideCloseCompletePayload(closure store.PrideClosure) []byte {
	writer := protocol.NewWriter(24).U8(0)
	writePrideASCII(writer, closure.Name, 16)
	writer.U32(closure.PrideID).U8(0)
	return writer.Clone()
}

func PrideMembersPayload(members []store.PrideMember) ([]byte, error) {
	if len(members) > 255 {
		return nil, fmt.Errorf("pride member list exceeds protocol limit")
	}
	writer := protocol.NewWriter(1 + len(members)*64).
		U8(0).U8(byte(len(members)))
	for _, member := range members {
		writePrideASCII(writer, member.Username, 16)
		writePrideUTF16(writer, member.Callsign, 24)
		writer.U8(byte(min(max(member.Level, 0), 255))).
			U32(uint32(max(member.GP, 0))).U32(uint32(max(member.Draws, 0))).
			U32(uint32(max(member.Wins, 0))).
			U32(uint32(max(member.Losses, 0))).U8(member.Role)
		writePrideASCII(writer, member.LastLogin.UTC().Format("20060102150405"), 14)
	}
	return writer.Clone(), nil
}

func PrideMemosPayload(prideID uint32, memos []store.PrideMemo) ([]byte, error) {
	if len(memos) > 255 {
		return nil, fmt.Errorf("pride memo list exceeds protocol limit")
	}
	writer := protocol.NewWriter(5 + len(memos)*64).
		U8(0).U32(prideID).U8(byte(len(memos)))
	for _, memo := range memos {
		writer.U32(memo.ID)
		writePrideUTF16(writer, memo.Author, 31)
		writePrideUTF16(writer, memo.Message, 127)
		writePrideASCII(writer, memo.CreatedAt.UTC().Format("20060102150405"), 20)
	}
	return writer.Clone(), nil
}

func PrideMemoStatusPayload() []byte { return []byte{0} }

func PrideMemoDeletePayload(prideID uint32, memos []store.PrideMemo) ([]byte, error) {
	return PrideMemosPayload(prideID, memos)
}

func PrideCloseCancelPayload(status byte) []byte {
	return protocol.NewWriter(2).U8(0).U8(status).Clone()
}

func MyPrideInfoPayload(contribution int) []byte {
	return protocol.NewWriter(5).U8(0).U32(uint32(max(contribution, 0))).Clone()
}

// MyPrideChainPayload answers 0x1746 (CS_PR_MYCHAININFO_ACK, sub_342513D0):
// u8 unknown (-> info +4, not seen read), u8 own chain level code (-> +0),
// u8 chain member count (-> +8). The chain page (sub_34329610) re-asks when
// the code or count differ from what it computed from the chain list.
func MyPrideChainPayload(levelCode, count byte) []byte {
	return []byte{0, 0, levelCode, count}
}

// PrideChainEntry is one online member of a Pride chain.
type PrideChainEntry struct {
	Callsign string
	Order    uint32 // map key: the client lists the chain by ascending key
}

// PrideChainListPayload answers 0x1748 (CS_PR_CHAINLIST_ACK, sub_343020F0):
// u8 count (max 50), then per member str8 narrow text (not seen read; empty),
// str8 UTF-16 callsign (byte length) and u32 key.
func PrideChainListPayload(entries []PrideChainEntry) []byte {
	if len(entries) > prideChainMaxMembers {
		entries = entries[:prideChainMaxMembers]
	}
	writer := protocol.NewWriter(2 + len(entries)*40).U8(0).U8(byte(len(entries)))
	for _, entry := range entries {
		writer.U8(0)
		writePrideUTF16(writer, entry.Callsign, 12)
		writer.U32(entry.Order)
	}
	return writer.Clone()
}

// prideChainMaxMembers is the client's list cap (sub_343020F0).
const prideChainMaxMembers = 50

// prideChainTiers is dword_34530F34: the chain positions per level, read from
// the end (sub_34329B00).
var prideChainTiers = [6]int{15, 12, 10, 8, 5, 5}

// prideChainLevel is the level of the member at a 1-based chain position, as
// the chain page computes it (sub_34329B00): with up to 5 members the chain
// is off (5); otherwise the first 5 are level 4, the next 8 level 3, the next
// 10 level 2 and the next 12 level 1; above 35 members everyone is level 0
// ("Bingo!!").
func prideChainLevel(position, count int) int {
	if count > 35 {
		return 0
	}
	level := 5
	if count > 5 {
		level = 4
	}
	remaining := position
	for ; level >= 0; level-- {
		remaining -= prideChainTiers[level]
		if remaining <= 0 {
			return level
		}
	}
	return 0
}

// prideChainLevelCode is the wire form of a level (sub_34329610 maps 0 to
// level 5 "Kapalı" and 100 to level 0 "Bingo!!").
func prideChainLevelCode(level int) byte {
	switch level {
	case 5:
		return 0
	case 0:
		return 100
	}
	return byte(level)
}

func PrideMessagesPayload(pride store.Pride) []byte {
	writer := protocol.NewWriter(32).U8(0)
	writePrideUTF16(writer, pride.Intro, 127)
	writePrideUTF16(writer, pride.Notice, 127)
	return writer.Clone()
}

func PrideRecentRankPayload(prideID uint32, ranks []int) []byte {
	writer := protocol.NewWriter(32).U8(0).U32(prideID)
	for index := 0; index < 7; index++ {
		value := 0
		if index < len(ranks) {
			value = ranks[index]
		}
		writer.U32(uint32(max(value, 0)))
	}
	return writer.Clone()
}

func PrideBattleResultPayload(summary store.PrideBattleSummary) []byte {
	// Pride battle fields follow the common success status.
	writer := protocol.NewWriter(27).U8(0).
		U32(summary.PrideID).
		U32(uint32(max(summary.Wins, 0))).
		U32(uint32(max(summary.Losses, 0))).
		U8(byte(min(max(summary.WinRate, 0), 100))).
		U32(uint32(max(summary.RankPoints, 0))).
		U32(uint32(max(summary.CurrentRank, 0))).
		U32(uint32(max(summary.PeakRank, 0))).U8(0)
	return writer.Clone()
}

// PrideBattleListPayload builds CS_PR_BATTLELIST_ACK.
// IDA sub_3432B8F0 wire format:
//
//	u8  status
//	u8  countAll  (total entries visible to this pride — used for pagination indicator)
//	u8  countPage (entries in THIS page response)
//	u8  countC    (same as countPage, second counter read by inner loop)
//	repeat countC:
//	  u32  battleID
//	  u8+bytes   opponent_pride_name (ASCII length-prefixed)
//	  u16+bytes  vs_pride_callsign (master callsign, UTF-16LE length-prefixed in byte count)
//	  u32  played_at unix timestamp
//	  u8   result (0=win 1=lose 2=draw)
//	  u8   map
//	  u16  mode
//	  u8   ace_flag
//	  u8   earn_sign (0=positive, 1=negative)
//	  u8+bytes  ace_member_callsign (UTF-16LE, 0 bytes if no ace)
//	  u32  earn_points (absolute value)
//	  u32  total_score
func PrideBattleListPayload(entries []store.PrideBattleEntry, total int) []byte {
	if len(entries) > 255 {
		entries = entries[:255]
	}
	count := byte(len(entries))
	totalClamped := byte(min(max(total, 0), 255))
	writer := protocol.NewWriter(4 + len(entries)*80).
		U8(0).            // status ok
		U8(totalClamped). // countAll
		U8(count).        // countPage
		U8(count)         // countC (repeated)
	for _, e := range entries {
		// 2013 TR CS_PR_BATTLELIST_ACK entry (sub_34339C60): u32, str8 ASCII
		// (16-byte buffer), str8 UTF-16 (34-byte buffer), u32, u8, u8, u16,
		// u8, u8, u8, str8 UTF-16 (26-byte buffer), u32, u32. The string caps
		// keep the client's stack buffers from overflowing.
		writer.U32(uint32(e.ID))
		writePrideASCII(writer, e.OpponentName, 15)
		writePrideUTF16(writer, e.OpponentMaster, 16)
		writer.U32(uint32(e.PlayedAt.Unix()))
		resultByte := byte(1) // lose
		if e.Won {
			resultByte = 0 // win
		}
		writer.U8(resultByte)
		writer.U8(byte(e.Map))
		writer.U16(uint16(e.Mode))
		aceFlag := byte(0) // ace (hero) of this side
		if e.AceCallsign != "" {
			aceFlag = 1
		}
		writer.U8(aceFlag)
		earnSign := byte(0)
		earnAbs := e.EarnPoints
		if earnAbs < 0 {
			earnSign = 1
			earnAbs = -earnAbs
		}
		writer.U8(earnSign)
		writer.U8(0)                    // third u8 of the 2013 record (meaning unverified)
		writePrideUTF16(writer, e.AceCallsign, 12) // ace member callsign (26-byte buffer)
		writer.U32(uint32(earnAbs))
		scoreTotal := e.ScoreA + e.ScoreB
		writer.U32(uint32(max(scoreTotal, 0)))
	}
	return writer.Clone()
}

func PrideDetailedInfoPayload(request PrideInfoRequest, pride store.Pride, members []store.PrideMember, role byte, found bool) []byte {
	writer := protocol.NewWriter(128).
		U8(0).U32(request.ID).U8(request.Mode)
	if !found {
		writer.U32(0)
		return writer.Clone()
	}
	// GETPRIDEINFO (sub_3417AFF0) is the player's own Pride: its state byte
	// is the account grade (+793).
	base := prideInfoPayloadWithState(pride, prideGrade(pride, role), true)
	writer.Bytes(base[1:]).U8(0).U8(0)
	writePrideASCII(writer, "", 31)
	writer.U8(0).U8(0) // chain names, crest IDs
	if len(members) > 255 {
		members = members[:255]
	}
	writer.U8(byte(len(members)))
	for _, member := range members {
		writePrideASCII(writer, member.Username, 16)
		writePrideUTF16(writer, member.Callsign, 24)
	}
	return writer.Clone()
}

func PrideMemberIDsPayload(usernames []string) []byte {
	if len(usernames) > 255 {
		usernames = usernames[:255]
	}
	writer := protocol.NewWriter(1 + len(usernames)*17).
		U8(0).U8(byte(len(usernames)))
	for _, username := range usernames {
		writePrideASCII(writer, username, 16)
	}
	return writer.Clone()
}

func readPrideASCII(payload []byte, offset int) (string, int, error) {
	if offset < 0 || offset >= len(payload) {
		return "", offset, fmt.Errorf("counted ASCII field is missing")
	}
	length := int(payload[offset])
	offset++
	if len(payload) < offset+length {
		return "", offset, fmt.Errorf("counted ASCII field exceeds payload")
	}
	return string(payload[offset : offset+length]), offset + length, nil
}

func readPrideUTF16(payload []byte, offset int) (string, int, error) {
	if offset < 0 || offset >= len(payload) {
		return "", offset, fmt.Errorf("counted UTF-16 field is missing")
	}
	length := int(payload[offset])
	offset++
	if length%2 != 0 || len(payload) < offset+length {
		return "", offset, fmt.Errorf("counted UTF-16 field is invalid")
	}
	units := make([]uint16, length/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(payload[offset+index*2 : offset+index*2+2])
	}
	return string(utf16.Decode(units)), offset + length, nil
}

func writePrideASCII(writer *protocol.Writer, value string, maximum int) {
	value = truncateUTF16(value, maximum)
	units := utf16.Encode([]rune(value))
	writer.U8(byte(len(units)))
	for _, unit := range units {
		writer.U8(byte(unit))
	}
}

func writePrideUTF16(writer *protocol.Writer, value string, maximumUnits int) {
	value = truncateUTF16(value, min(maximumUnits, 127))
	units := utf16.Encode([]rune(value))
	writer.U8(byte(len(units) * 2)).UTF16LE(value)
}

func prideDate(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format("20060102")
}
