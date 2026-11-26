package game

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

func TestPrideRequestOpcodesAndSerializersMatchProtocol(t *testing.T) {
	pairs := [][2]uint16{
		{RequestPrideCreate, ReplyPrideCreate}, {RequestPrideClose, ReplyPrideClose},
		{RequestPrideList, ReplyPrideList}, {RequestPrideInfo, ReplyPrideInfo},
		{RequestPrideAskCancel, ReplyPrideAskCancel}, {RequestPrideAsk, ReplyPrideAsk},
		{RequestPrideLeave, ReplyPrideLeave}, {RequestPrideAccept, ReplyPrideAccept},
		{RequestPrideWaiting, ReplyPrideWaiting}, {RequestPrideMessages, ReplyPrideMessages},
		{RequestPrideMemo, ReplyPrideMemo}, {RequestPrideMemoAdd, ReplyPrideMemoAdd},
		{RequestPrideMemoDelete, ReplyPrideMemoDelete}, {RequestPrideMembers, ReplyPrideMembers},
		{RequestPrideMaster, ReplyPrideMaster}, {RequestPrideKick, ReplyPrideKick},
		{RequestPrideFullInfo, ReplyPrideFullInfo}, {RequestPrideCloseCancel, ReplyPrideCloseCancel},
		{RequestPrideResult, ReplyPrideResult}, {RequestPrideBattleList, ReplyPrideBattleList},
		{RequestPrideMemberIDs, ReplyPrideMemberIDs},
		{RequestMyPrideInfo, ReplyMyPrideInfo}, {RequestMyPrideChain, ReplyMyPrideChain},
		{RequestPrideChainList, ReplyPrideChainList}, {RequestPrideRecentRank, ReplyPrideRecentRank},
	}
	for _, pair := range pairs {
		if pair[1] != pair[0]+1 {
			t.Fatalf("pride opcode pair = 0x%04x/0x%04x", pair[0], pair[1])
		}
	}
	createBytes := []byte{8, 'W', 0, 'o', 0, 'l', 0, 'f', 0, 4, 'H', 0, 'i', 0}
	create, err := ParsePrideCreateRequest(createBytes)
	if err != nil || create.Name != "Wolf" || create.Intro != "Hi" {
		t.Fatalf("create pride request = %+v, %v", create, err)
	}
	listBytes := []byte{2, 'A', 0, 2, 'B', 0, 3, 4, 0}
	list, err := ParsePrideListRequest(listBytes)
	if err != nil || list.Name != "A" || list.Master != "B" || list.Mode != 3 || list.Page != 4 {
		t.Fatalf("pride list request = %+v, %v", list, err)
	}
	acceptBytes := []byte{4, 'u', 's', 'e', 'r', 2, 'C', 0, 1, 2, 9, 0, 0, 0}
	accept, err := ParsePrideAcceptRequest(acceptBytes)
	if err != nil || accept.Username != "user" || accept.Callsign != "C" || !accept.Accept || accept.Mode != 2 || accept.PrideID != 9 {
		t.Fatalf("accept pride request = %+v, %v", accept, err)
	}

	msgBytes := []byte{4, 'H', 0, 'i', 0, 8, 'N', 0, 'e', 0, 'w', 0, 's', 0}
	intro, notice, err := ParsePrideMessagesRequest(msgBytes)
	if err != nil || intro != "Hi" || notice != "News" {
		t.Fatalf("messages request = intro:%q notice:%q err:%v", intro, notice, err)
	}
}

func TestPrideListMemberMemoAndBattleLayoutsMatchProtocol(t *testing.T) {
	created := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	pride := store.Pride{ID: 9, Name: "Clan", Master: "M", Intro: "I", RankPoints: 44, MemberCount: 1, CreatedAt: created}
	list, err := PrideListPayload(0, store.PridePage{Prides: []store.Pride{pride}, Total: 1, TotalPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte{0, 0, 0, 0, 0, 1, 9, 0, 0, 0, 8, 'C', 0, 'l', 0, 'a', 0, 'n', 0, 2, 'M', 0, 2, 'I', 0}
	if !bytes.Equal(list, expected) {
		t.Fatalf("pride list = %x, want %x", list, expected)
	}

	members, err := PrideMembersPayload([]store.PrideMember{{
		Username: "u", Callsign: "C", Role: 8, Level: 7, GP: 100, Draws: 4, Wins: 2, Losses: 3, LastLogin: created,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if members[0] != 0 || members[1] != 1 || members[2] != 1 || members[3] != 'u' || members[4] != 2 ||
		!bytes.Equal(members[5:7], []byte{'C', 0}) || members[7] != 7 ||
		binary.LittleEndian.Uint32(members[8:12]) != 100 || binary.LittleEndian.Uint32(members[12:16]) != 4 ||
		binary.LittleEndian.Uint32(members[16:20]) != 2 || binary.LittleEndian.Uint32(members[20:24]) != 3 ||
		members[24] != 8 || members[25] != 14 || !bytes.Equal(members[26:40], []byte("20260718000000")) {
		t.Fatalf("pride members = %x", members)
	}

	memos, err := PrideMemosPayload(9, []store.PrideMemo{{ID: 5, Author: "A", Message: "Hi", CreatedAt: created}})
	if err != nil {
		t.Fatal(err)
	}
	if memos[0] != 0 || binary.LittleEndian.Uint32(memos[1:5]) != 9 || memos[5] != 1 || binary.LittleEndian.Uint32(memos[6:10]) != 5 ||
		memos[10] != 2 || !bytes.Equal(memos[11:13], []byte{'A', 0}) || memos[13] != 4 ||
		!bytes.Equal(memos[14:18], []byte{'H', 0, 'i', 0}) || memos[18] != 14 || !bytes.Equal(memos[19:33], []byte("20260718000000")) {
		t.Fatalf("pride memos = %x", memos)
	}

	battle := PrideBattleResultPayload(store.PrideBattleSummary{PrideID: 9, Wins: 4, Losses: 2, WinRate: 67, RankPoints: 10, CurrentRank: 20, PeakRank: 15})
	if len(battle) != 27 || battle[0] != 0 || binary.LittleEndian.Uint32(battle[1:5]) != 9 ||
		binary.LittleEndian.Uint32(battle[5:9]) != 4 || binary.LittleEndian.Uint32(battle[9:13]) != 2 || battle[13] != 67 ||
		binary.LittleEndian.Uint32(battle[14:18]) != 10 || binary.LittleEndian.Uint32(battle[18:22]) != 20 ||
		binary.LittleEndian.Uint32(battle[22:26]) != 15 || battle[26] != 0 {
		t.Fatalf("pride battle result = %x", battle)
	}
	if got := PrideBattleListPayload(nil, 0); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("empty pride battle list = %x", got)
	}
}

func TestPrideCloseLayoutMatchesProtocol(t *testing.T) {
	closeAt := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	got := PrideClosePayload(store.Pride{CloseStatus: 10, CloseAt: &closeAt}, true)
	want := append([]byte{0, 10, 8}, []byte("20260725")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("pride close = %x, want %x", got, want)
	}

	if failed := PrideClosePayload(store.Pride{}, false); !bytes.Equal(failed, []byte{0, 0, 0}) {
		t.Fatalf("failed pride close = %x", failed)
	}
}

func TestPrideFullInfoAndEmptyCreateAreStructurallyComplete(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	created := PrideCreatePayload(
		store.Pride{ID: 9, Name: "Clan", Master: "M", Intro: "I", MemberCount: 1, CreatedAt: createdAt},
		store.PrideMember{Role: 8, Contribution: 42}, 1234, true,
	)
	// ... grade 0 (master) after the master name, then member count 1 and Gold.
	if len(created) < 5 || created[len(created)-5] != 1 || binary.LittleEndian.Uint32(created[len(created)-4:]) != 1234 {
		t.Fatalf("created pride member count/gold = %x", created)
	}
	if created[5+1+8+1+2] != prideGradeMaster {
		t.Fatalf("created pride grade = %x", created)
	}

	info := PrideInfoPayload(store.Pride{
		ID: 9, Name: "Clan", Master: "M", RankPoints: 44, MemberCount: 1, CreatedAt: createdAt,
	}, 8, true)
	if len(info) < 22 || info[0] != 0 || info[21] != 1 {
		t.Fatalf("pride info member count = %x", info)
	}

	recent := PrideRecentRankPayload(9, []int{7, 6})
	if len(recent) != 33 || recent[0] != 0 || binary.LittleEndian.Uint32(recent[1:5]) != 9 ||
		binary.LittleEndian.Uint32(recent[5:9]) != 7 || binary.LittleEndian.Uint32(recent[9:13]) != 6 ||
		binary.LittleEndian.Uint32(recent[29:33]) != 0 {
		t.Fatalf("pride recent rank = %x", recent)
	}

	failed := PrideCreatePayload(store.Pride{}, store.PrideMember{}, 0, false)
	if len(failed) != 14 || failed[0] != 0 || binary.LittleEndian.Uint32(failed[10:14]) != 0 {
		t.Fatalf("failed pride create = %x", failed)
	}
	request := PrideInfoRequest{ID: 9, Mode: 2}
	empty := PrideDetailedInfoPayload(request, store.Pride{}, nil, 0, false)
	if !bytes.Equal(empty, []byte{0, 9, 0, 0, 0, 2, 0, 0, 0, 0}) {
		t.Fatalf("empty pride detail = %x", empty)
	}
}

func TestPrideControlPayloadsHaveExpectedLength(t *testing.T) {
	deletePayload, err := PrideMemoDeletePayload(9, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{name: "memo status", got: PrideMemoStatusPayload(), want: []byte{0}},
		{name: "empty memo delete refresh", got: deletePayload, want: []byte{0, 9, 0, 0, 0, 0}},
		{name: "cancel close", got: PrideCloseCancelPayload(10), want: []byte{0, 10}},
		{name: "my pride info", got: MyPrideInfoPayload(42), want: []byte{0, 42, 0, 0, 0}},
		{name: "my pride chain", got: MyPrideChainPayload(0, 0), want: []byte{0, 0, 0, 0}},
		{name: "pride chain list", got: PrideChainListPayload(nil), want: []byte{0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !bytes.Equal(test.got, test.want) {
				t.Fatalf("payload = %x, want %x", test.got, test.want)
			}
		})
	}
}

// Go translation of sub_34339C60's entry loop: checks exact consumption.
func TestPrideBattleListEntryMatches2013Parser(t *testing.T) {
	payload := PrideBattleListPayload([]store.PrideBattleEntry{{ID: 7, OpponentName: "SOSISLER", OpponentMaster: "Usta", Won: true, EarnPoints: -5}}, 1)
	r := newReader2012(t, payload)
	_, _, n := r.u8(), r.u8(), r.u8()
	for i := 0; i < int(n); i++ {
		r.u32()
		r.need(int(r.u8()))
		r.need(int(r.u8()))
		r.u32()
		r.u8()
		r.u8()
		r.u16()
		r.u8()
		r.u8()
		r.u8()
		r.need(int(r.u8()))
		r.u32()
		r.u32()
	}
	r.done()
}
