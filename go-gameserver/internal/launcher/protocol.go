package launcher

import "encoding/binary"

const (
	MessageLoginRequest = 0x1310
	MessageAuthReply    = 0x1312
	MessageSystemInfo   = 0x4000
	MessageStartGame    = 0x4001
	AuthWrongPassword   = 0x19
	HeaderSize          = 8
	MaxFrameSize        = 8192
)

type ProtocolOptions struct {
	ChannelID        uint32
	SendResult10     bool
	ChallengePayload bool
}

func MessageID(frame []byte) (uint16, bool) {
	if len(frame) < HeaderSize {
		return 0, false
	}
	return binary.LittleEndian.Uint16(frame[4:6]), true
}

func FrameSize(header []byte) (int, bool) {
	if len(header) < HeaderSize {
		return 0, false
	}
	size := int(binary.LittleEndian.Uint16(header[:2]))
	return size, size >= HeaderSize && size <= MaxFrameSize
}

func Challenge(withPayload bool) []byte {
	if withPayload {
		return header(MessageAuthReply, 0, 0x2c)
	}
	return header(MessageAuthReply, 0, HeaderSize)
}

func AuthResult(result uint16) []byte {
	frame := header(MessageAuthReply, result, 10)
	frame[8] = 1
	return frame
}

func StartGame(channelID uint32) []byte {
	frame := header(MessageStartGame, 0, 12)
	binary.LittleEndian.PutUint32(frame[8:12], channelID)
	return frame
}

func header(messageID, field uint16, size int) []byte {
	frame := make([]byte, size)
	binary.LittleEndian.PutUint16(frame[:2], uint16(size))
	binary.LittleEndian.PutUint16(frame[4:6], messageID)
	binary.LittleEndian.PutUint16(frame[6:8], field)
	return frame
}

func Responses(messageID uint16, options ProtocolOptions) [][]byte {
	switch messageID {
	case MessageLoginRequest:
		responses := [][]byte{Challenge(options.ChallengePayload)}
		if options.SendResult10 {
			responses = append(responses, AuthResult(0x10))
		}
		return responses
	case MessageSystemInfo:
		return [][]byte{StartGame(options.ChannelID)}
	default:
		return nil
	}
}
