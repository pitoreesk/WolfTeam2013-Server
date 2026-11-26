package game

import (
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

type channelAccountTestUsers struct {
	*fakeUsers
	store.ChannelAccountStore
	state store.ChannelAccountState
}

func (c *channelAccountTestUsers) ChangeCallsign(_ context.Context, _ int, callsign string) (string, bool, error) {
	return callsign, true, nil
}
func (c *channelAccountTestUsers) ChangeSchool(_ context.Context, _ int, school uint32) (store.ChannelAccountState, bool, error) {
	c.state.SchoolID = school
	return c.state, true, nil
}
func (c *channelAccountTestUsers) ChannelAccountState(context.Context, int) (store.ChannelAccountState, error) {
	return c.state, nil
}
func (c *channelAccountTestUsers) PendingRecommendations(context.Context, int) ([]store.RecommendationUser, error) {
	return nil, nil
}
func (c *channelAccountTestUsers) AskRecommendation(context.Context, int, string) (int, store.RecommendationUser, store.RecommendationUser, bool, error) {
	return 2,
		store.RecommendationUser{UserID: 1, Username: "one", Callsign: "One", Level: 5, Status: 3},
		store.RecommendationUser{UserID: 2, Username: "two", Callsign: "Two", Level: 6, Status: 3}, true, nil
}
func (c *channelAccountTestUsers) AnswerRecommendation(context.Context, int, string, bool) (int, store.RecommendationUser, store.RecommendationUser, bool, error) {
	return 1,
		store.RecommendationUser{UserID: 1, Username: "one", Callsign: "One", Level: 5, Status: 3},
		store.RecommendationUser{UserID: 2, Username: "two", Callsign: "Two", Level: 6, Status: 3}, true, nil
}

func TestChannelAccountDispatch(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &channelAccountTestUsers{fakeUsers: &fakeUsers{}, state: store.ChannelAccountState{WolfPoints: 99}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureGameVersion(378)
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 42, "Player", 7)
	client := transport.NewEncryptedConn(clientNet, nil)

	invoke := func(requestID, replyID uint16, payload []byte) transport.Frame {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: requestID, Payload: payload})
		}()
		frame := assertFrameID(t, client, replyID)
		if requestID == RequestUserInfo {
			assertFrameID(t, client, ReplyTutorialClear) // 2012 login tutorial progress
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return frame
	}

	nickname := invoke(RequestChangeNickname, ReplyChangeNickname, []byte{6, 'F', 0, 'o', 0, 'x', 0})
	if nickname.Payload[0] != 0 || nickname.Payload[1] != 6 || session.Callsign != "Fox" {
		t.Fatalf("nickname reply/session = %x/%q", nickname.Payload, session.Callsign)
	}
	version := invoke(RequestCheckVersion, ReplyCheckVersion, nil)
	if !bytesPrefix(version.Payload, []byte{0, 6, '3', 0, '7', 0, '8', 0}) {
		t.Fatalf("version reply = %x", version.Payload)
	}
	school := invoke(RequestChangeSchool, ReplyChangeSchool, []byte{7, 0, 0, 0})
	if school.Payload[0] != 0 || binary.LittleEndian.Uint32(school.Payload[1:5]) != 7 || binary.LittleEndian.Uint32(school.Payload[5:9]) != 100 {
		t.Fatalf("school reply = %x", school.Payload)
	}
	userInfo := invoke(RequestUserInfo, ReplyUserInfo, nil)
	userInfoTail := userInfo.Payload[2+int(userInfo.Payload[1]):]
	if binary.LittleEndian.Uint32(userInfoTail[2:6]) != 7 {
		t.Fatalf("school was not restored by user info: %x", userInfoTail[:10])
	}
	wolf := invoke(RequestWolfPoint, ReplyWolfPoint, nil)
	if wolf.Payload[0] != 0 || binary.LittleEndian.Uint32(wolf.Payload[1:5]) != 99 {
		t.Fatalf("wolf-point reply = %x", wolf.Payload)
	}
	event := invoke(RequestEventList, ReplyEventList, nil)
	if !channelAllZero(event.Payload) {
		t.Fatalf("event-list reply = %x", event.Payload)
	}
}

func TestRecommendationDispatchRoutesAskAndAcceptedAnswer(t *testing.T) {
	firstServerNet, firstClientNet := net.Pipe()
	secondServerNet, secondClientNet := net.Pipe()
	defer firstServerNet.Close()
	defer firstClientNet.Close()
	defer secondServerNet.Close()
	defer secondClientNet.Close()
	_ = firstClientNet.SetDeadline(time.Now().Add(3 * time.Second))
	_ = secondClientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &channelAccountTestUsers{fakeUsers: &fakeUsers{}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	first, err := newSession(transport.NewEncryptedConn(firstServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSession(transport.NewEncryptedConn(secondServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	first.setAuthenticated("one", 1, "One", 5)
	second.setAuthenticated("two", 2, "Two", 6)
	server.addSession(first)
	server.addSession(second)
	defer server.removeSession(first)
	defer server.removeSession(second)
	firstClient := transport.NewEncryptedConn(firstClientNet, nil)
	secondClient := transport.NewEncryptedConn(secondClientNet, nil)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), first, transport.Frame{
			MessageID: RequestAskRecommend,
			Payload:   []byte{6, 'T', 0, 'w', 0, 'o', 0},
		})
	}()
	senderAsk := assertFrameID(t, firstClient, ReplyAskRecommend)
	incomingAsk := assertFrameID(t, secondClient, ReplyAskRecommend)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if senderAsk.Payload[3] != 1 || incomingAsk.Payload[3] != 1 || senderAsk.Payload[5] != 't' || incomingAsk.Payload[5] != 'o' {
		t.Fatalf("ask routing = %x / %x", senderAsk.Payload, incomingAsk.Payload)
	}

	go func() {
		done <- server.handleMessage(context.Background(), second, transport.Frame{
			MessageID: RequestAnswerRecommend,
			Payload:   []byte{1, 6, 'O', 0, 'n', 0, 'e', 0},
		})
	}()
	responderAnswer := assertFrameID(t, secondClient, ReplyAnswerRecommend)
	requesterAnswer := assertFrameID(t, firstClient, ReplyAnswerRecommend)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if responderAnswer.Payload[2] != 'o' || requesterAnswer.Payload[2] != 't' {
		t.Fatalf("answer routing = %x / %x", responderAnswer.Payload, requesterAnswer.Payload)
	}
}

func bytesPrefix(left, right []byte) bool {
	if len(left) < len(right) {
		return false
	}
	for index := range right {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func channelAllZero(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}
