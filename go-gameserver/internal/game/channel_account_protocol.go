package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type AnswerRecommendationRequest struct {
	Accept   bool
	Callsign string
}

func ParseChangeNicknameRequest(payload []byte) (string, error) {
	value, _, err := readChannelUTF16(payload, 0)
	return value, err
}

func ChangeNicknamePayload(callsign string) ([]byte, error) {
	return channelUTF16AckPayload(callsign)
}

func CheckVersionPayload(version int) ([]byte, error) {
	return channelUTF16AckPayload(fmt.Sprintf("%d", version))
}

func ParseChangeSchoolRequest(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("change-school payload is too short")
	}
	return binary.LittleEndian.Uint32(payload[:4]), nil
}

func ChangeSchoolPayload(schoolID, gold uint32) []byte {
	return protocol.NewWriter(9).U8(0).U32(schoolID).U32(gold).Clone()
}

func WolfPointPayload(points uint32) []byte {
	return protocol.NewWriter(5).U8(0).U32(points).Clone()
}

// EventListPayload is the event-list refresh barrier; visible state arrives
// through separate pushes.
// EventListPayload is an empty 2012 CS_CH_EVENTLIST_ACK: result, u8 count 0.
func EventListPayload() []byte { return []byte{0, 0} }

func ParseAskRecommendationRequest(payload []byte) (string, error) {
	value, _, err := readChannelUTF16(payload, 0)
	return value, err
}

func ParseAnswerRecommendationRequest(payload []byte) (AnswerRecommendationRequest, error) {
	if len(payload) < 2 {
		return AnswerRecommendationRequest{}, fmt.Errorf("answer-recommendation payload is too short")
	}
	callsign, _, err := readChannelUTF16(payload, 1)
	if err != nil {
		return AnswerRecommendationRequest{}, err
	}
	return AnswerRecommendationRequest{Accept: payload[0] == 1, Callsign: callsign}, nil
}

func RecommendationListPayload(users []store.RecommendationUser) ([]byte, error) {
	if len(users) > 255 {
		users = users[:255]
	}
	writer := protocol.NewWriter(2 + len(users)*32).U8(0).U8(byte(len(users)))
	for _, user := range users {
		if err := writeRecommendationUser(writer, user); err != nil {
			return nil, err
		}
	}
	return writer.Clone(), nil
}

// AskRecommendationPayload carries a two-byte server token, a success byte,
// and a recommendation user record.
func AskRecommendationPayload(success bool, user store.RecommendationUser) ([]byte, error) {
	writer := protocol.NewWriter(41).U8(0).U16(0).Bool(success)
	if err := writeRecommendationUser(writer, user); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

// AnswerRecommendationPayload carries the answering account's login and
// callsign.
func AnswerRecommendationPayload(user store.RecommendationUser) ([]byte, error) {
	writer := protocol.NewWriter(33).U8(0)
	if err := writeChannelASCII(writer, user.Username, 20); err != nil {
		return nil, err
	}
	if err := writeChannelUTF16(writer, user.Callsign, 32); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

func writeRecommendationUser(writer *protocol.Writer, user store.RecommendationUser) error {
	if err := writeChannelASCII(writer, user.Username, 20); err != nil {
		return err
	}
	if err := writeChannelUTF16(writer, user.Callsign, 32); err != nil {
		return err
	}
	writer.U8(user.Level)
	writer.U32(user.UserID)
	writer.U8(user.Status)
	return nil
}

func channelUTF16Payload(value string) ([]byte, error) {
	writer := protocol.NewWriter(1 + len(value)*2)
	if err := writeChannelUTF16(writer, value, 127); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

func channelUTF16AckPayload(value string) ([]byte, error) {
	payload, err := channelUTF16Payload(value)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, payload...), nil
}

func writeChannelASCII(writer *protocol.Writer, value string, maximumBytes int) error {
	units := utf16.Encode([]rune(value))
	if len(units) > maximumBytes || len(units) > 255 {
		return fmt.Errorf("ASCII field exceeds %d bytes", maximumBytes)
	}
	writer.U8(byte(len(units)))
	writer.ASCII(value)
	return nil
}

func writeChannelUTF16(writer *protocol.Writer, value string, maximumUnits int) error {
	units := utf16.Encode([]rune(value))
	if len(units) > maximumUnits || len(units)*2 > 255 {
		return fmt.Errorf("UTF-16 field exceeds %d code units", maximumUnits)
	}
	writer.U8(byte(len(units) * 2))
	writer.UTF16LE(value)
	return nil
}

func readChannelUTF16(payload []byte, offset int) (string, int, error) {
	if offset < 0 || offset >= len(payload) {
		return "", offset, fmt.Errorf("missing counted UTF-16 length")
	}
	length := int(payload[offset])
	offset++
	if length%2 != 0 || offset+length > len(payload) {
		return "", offset, fmt.Errorf("invalid counted UTF-16 length %d", length)
	}
	units := make([]uint16, length/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(payload[offset+index*2:])
	}
	return string(utf16.Decode(units)), offset + length, nil
}
