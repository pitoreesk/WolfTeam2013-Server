package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/store"
)

func TestChannelAccountRequestLayouts(t *testing.T) {
	nicknamePayload := []byte{6, 'F', 0, 'o', 0, 'x', 0}
	nickname, err := ParseChangeNicknameRequest(nicknamePayload)
	if err != nil || nickname != "Fox" {
		t.Fatalf("nickname = %q, %v", nickname, err)
	}
	if school, err := ParseChangeSchoolRequest([]byte{7, 0, 0, 0}); err != nil || school != 7 {
		t.Fatalf("school = %d, %v", school, err)
	}
	if ask, err := ParseAskRecommendationRequest([]byte{6, 'F', 0, 'o', 0, 'x', 0}); err != nil || ask != "Fox" {
		t.Fatalf("ask = %q, %v", ask, err)
	}
	answer, err := ParseAnswerRecommendationRequest([]byte{1, 6, 'F', 0, 'o', 0, 'x', 0})
	if err != nil || !answer.Accept || answer.Callsign != "Fox" {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
}

func TestChannelAccountReplyLayouts(t *testing.T) {
	version, err := CheckVersionPayload(377)
	if err != nil || !bytes.Equal(version, []byte{0, 6, '3', 0, '7', 0, '7', 0}) {
		t.Fatalf("version = %x, %v", version, err)
	}
	if school := ChangeSchoolPayload(7, 9); school[0] != 0 || binary.LittleEndian.Uint32(school[1:5]) != 7 || binary.LittleEndian.Uint32(school[5:9]) != 9 {
		t.Fatalf("school reply = %x", school)
	}
	if event := EventListPayload(); !bytes.Equal(event, []byte{0, 0}) {
		t.Fatalf("event-list reply = %x", event)
	}

	user := store.RecommendationUser{UserID: 42, Username: "user", Callsign: "Fox", Level: 7, Status: 3}
	list, err := RecommendationListPayload([]store.RecommendationUser{user})
	if err != nil {
		t.Fatal(err)
	}
	wantList := []byte{0, 1, 4, 'u', 's', 'e', 'r', 6, 'F', 0, 'o', 0, 'x', 0, 7, 42, 0, 0, 0, 3}
	if !bytes.Equal(list, wantList) {
		t.Fatalf("recommendation list = %x, want %x", list, wantList)
	}
	ask, err := AskRecommendationPayload(true, user)
	if err != nil || !bytes.Equal(ask, append([]byte{0, 0, 0, 1}, wantList[2:]...)) {
		t.Fatalf("ask reply = %x, %v", ask, err)
	}
	answer, err := AnswerRecommendationPayload(user)
	wantAnswer := []byte{0, 4, 'u', 's', 'e', 'r', 6, 'F', 0, 'o', 0, 'x', 0}
	if err != nil || !bytes.Equal(answer, wantAnswer) {
		t.Fatalf("answer reply = %x, %v", answer, err)
	}
}

func TestChannelAccountRejectsMalformedCountedStrings(t *testing.T) {
	for _, payload := range [][]byte{{}, {3, 'A', 0, 'B'}, {4, 'A', 0}} {
		if _, err := ParseChangeNicknameRequest(payload); err == nil {
			t.Fatalf("accepted malformed nickname %x", payload)
		}
	}
}
