package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type prideTestUsers struct {
	*fakeUsers
	store.PrideStore
}

func (p *prideTestUsers) CreatePride(_ context.Context, userID int, name, intro string) (store.Pride, store.PrideMember, bool, error) {
	pride := store.Pride{ID: 9, Name: name, Master: "Player", MasterUserID: userID, Intro: intro, MemberCount: 1, CreatedAt: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)}
	member := store.PrideMember{UserID: userID, Username: "user", Callsign: "Player", Role: 8, Contribution: 42}
	return pride, member, true, nil
}

func (p *prideTestUsers) PrideBattleResult(context.Context, uint32) (store.PrideBattleSummary, error) {
	return store.PrideBattleSummary{PrideID: 9, Wins: 4, Losses: 2, RankPoints: 10, WinRate: 67, CurrentRank: 3, PeakRank: 2}, nil
}

func (p *prideTestUsers) PrideForUser(_ context.Context, userID int) (store.Pride, store.PrideMember, bool, error) {
	return store.Pride{
		ID: 9, Name: "Wolf", Master: "Player", MasterUserID: userID, MemberCount: 1,
		CreatedAt: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC),
	}, store.PrideMember{UserID: userID, Role: 8}, true, nil
}

func (p *prideTestUsers) SchedulePrideClose(_ context.Context, userID int) (store.Pride, bool, error) {
	closeAt := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	return store.Pride{
		ID: 9, Name: "Wolf", Master: "Player", MasterUserID: userID,
		CloseStatus: 10, CloseAt: &closeAt,
	}, true, nil
}

func (p *prideTestUsers) PrideBattleHistory(_ context.Context, _ uint32, _ int, _ bool) ([]store.PrideBattleEntry, int, error) {
	return []store.PrideBattleEntry{
		{
			ID:              1,
			PrideID:         9,
			OpponentPrideID: 10,
			OpponentName:    "Rivals",
			OpponentMaster:  "EnemyLeader",
			Won:             true,
			Mode:            1,
			Map:             2,
			ScoreA:          100,
			ScoreB:          50,
			EarnPoints:      3,
			PlayedAt:        time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		},
	}, 1, nil
}

func (p *prideTestUsers) SavePrideBattle(_ context.Context, _ uint32, _ uint32, _ int, _ int, _ bool, _ int, _ int, _ int, _ []store.PrideBattleMemberEntry) error {
	return nil
}

func TestPrideCreateAndBattleResultDispatch(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &prideTestUsers{fakeUsers: &fakeUsers{}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 42, "Player", 7)
	client := transport.NewEncryptedConn(clientNet, nil)

	invoke := func(messageID, replyID uint16, payload []byte) transport.Frame {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: messageID, Payload: payload})
		}()
		frame := assertFrameID(t, client, replyID)
		if messageID == RequestUserInfo {
			assertFrameID(t, client, ReplyTutorialClear) // 2012 login tutorial progress
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return frame
	}

	create := invoke(RequestPrideCreate, ReplyPrideCreate, []byte{8, 'W', 0, 'o', 0, 'l', 0, 'f', 0, 4, 'H', 0, 'i', 0})
	if create.Payload[0] != 0 || binary.LittleEndian.Uint32(create.Payload[1:5]) != 9 || create.Payload[5] != 8 ||
		create.Payload[27] != prideGradeMaster || create.Payload[33] != 1 { // grade 0 (master), member count 1 (then Gold)
		t.Fatalf("pride create reply = %x", create.Payload)
	}
	battle := invoke(RequestPrideResult, ReplyPrideResult, []byte{9, 0, 0, 0})
	if battle.Payload[0] != 0 || binary.LittleEndian.Uint32(battle.Payload[1:5]) != 9 ||
		binary.LittleEndian.Uint32(battle.Payload[5:9]) != 4 || binary.LittleEndian.Uint32(battle.Payload[9:13]) != 2 ||
		battle.Payload[13] != 67 || binary.LittleEndian.Uint32(battle.Payload[18:22]) != 3 ||
		binary.LittleEndian.Uint32(battle.Payload[22:26]) != 2 {
		t.Fatalf("pride battle reply = %x", battle.Payload)
	}
	battleList := invoke(RequestPrideBattleList, ReplyPrideBattleList, make([]byte, 6))
	if len(battleList.Payload) < 4 || battleList.Payload[0] != 0 || battleList.Payload[1] != 1 || battleList.Payload[2] != 1 || battleList.Payload[3] != 1 {
		t.Fatalf("pride battle list reply = %x", battleList.Payload)
	}

	userInfo := invoke(RequestUserInfo, ReplyUserInfo, nil)
	prideOffset := 2 + len("Player")*2 + 86 // 2013 TR USERINFO_ACK
	if binary.LittleEndian.Uint32(userInfo.Payload[prideOffset:prideOffset+4]) != 9 {
		t.Fatalf("persisted pride missing from user-info reply = %x", userInfo.Payload)
	}
}

func TestPrideCloseFieldListTransitionRemovesLastRoomMember(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	users := &prideTestUsers{fakeUsers: &fakeUsers{}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 42, "Player", 7)

	room, err := server.allocateRoom(RoomConfig{Title: "Pride close regression", Capacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, added := room.Add(session); !added {
		t.Fatal("failed to add pride master to room")
	}

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: RequestPrideClose})
	}()

	client := transport.NewEncryptedConn(clientNet, nil)
	closeReply := assertFrameID(t, client, ReplyPrideClose)
	wantClose := append([]byte{0, 10, 8}, []byte("20260725")...)
	if len(closeReply.Payload) < len(wantClose) || !bytes.Equal(closeReply.Payload[:len(wantClose)], wantClose) ||
		!bytes.Equal(closeReply.Payload[len(wantClose):], make([]byte, len(closeReply.Payload)-len(wantClose))) {
		t.Fatalf("pride close reply = %x, want %x", closeReply.Payload, wantClose)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if session.RoomNumber != room.Number || room.Snapshot().Users != 1 {
		t.Fatalf("pride reply changed room membership before the field-list transition: session room=%d users=%d", session.RoomNumber, room.Snapshot().Users)
	}

	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: RequestFieldList})
	}()
	assertFrameID(t, client, ReplyExitRoom)
	assertFrameID(t, client, ReplyFieldList)
	assertFrameID(t, client, ReplyJoinAllow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if session.RoomNumber != -1 || room.Snapshot().Users != 0 {
		t.Fatalf("room membership survived the field-list transition: session room=%d users=%d", session.RoomNumber, room.Snapshot().Users)
	}
	if server.roomByNumber(room.Number) != nil {
		t.Fatal("empty room survived the field-list transition")
	}
	if slot, pending := session.pendingClientRoomDeparture(); !pending || slot != 0 {
		t.Fatalf("field-list transition did not preserve the client cleanup slot: slot=%d pending=%t", slot, pending)
	}
}
