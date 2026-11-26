package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type Server struct {
	address      string
	advertisedIP netip.Addr
	relayPort    uint16
	channels     store.ChannelSource
	logger       *slog.Logger
}

func NewServer(address string, advertisedIP netip.Addr, relayPort uint16, channels store.ChannelSource, logger *slog.Logger) *Server {
	return &Server{
		address:      address,
		advertisedIP: advertisedIP,
		relayPort:    relayPort,
		channels:     channels,
		logger:       logger,
	}
}

func (s *Server) Run(ctx context.Context) error {
	return transport.ServeTCP(ctx, s.address, "broker", func(listener net.Listener) {
		s.logger.Info("broker server started", "address", listener.Addr().String())
	}, func(connection net.Conn) {
		if err := s.handleConnection(ctx, connection); err != nil && ctx.Err() == nil {
			s.logger.Warn("broker connection ended", "remote", connection.RemoteAddr(), "error", err)
		}
	})
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) error {
	defer connection.Close()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	go func() {
		<-connectionCtx.Done()
		_ = connection.Close()
	}()

	advertisedIP := s.advertisedIP
	s.logger.Info("broker client connected", "remote", connection.RemoteAddr(), "advertised_ip", advertisedIP)
	wire := transport.NewEncryptedConn(connection, nil)
	for {
		frame, err := wire.ReadFrame()
		if err != nil {
			if transport.IsClosed(err) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read broker frame: %w", err)
		}

		messageID := frame.MessageID
		s.logger.Debug("broker message received", "message_id", fmt.Sprintf("0x%04x", messageID), "sequence", frame.Sequence, "bytes", len(frame.Payload)+8)
		responseID, responsePayload, err := s.response(connectionCtx, messageID, advertisedIP)
		if err != nil {
			// A failed lookup does not invalidate the encrypted connection.
			s.logger.Warn("broker message failed", "message_id", fmt.Sprintf("0x%04x", messageID), "error", err)
			continue
		}
		if responseID == 0 {
			s.logger.Debug("ignoring unknown broker message", "message_id", fmt.Sprintf("0x%04x", messageID))
			continue
		}
		sequence, err := wire.WriteMessage(responseID, responsePayload)
		if err != nil {
			return fmt.Errorf("write broker response: %w", err)
		}
		s.logger.Debug("broker message sent", "message_id", fmt.Sprintf("0x%04x", responseID), "sequence", sequence, "payload_bytes", len(responsePayload))
	}
}

func (s *Server) response(ctx context.Context, messageID uint16, advertisedIP netip.Addr) (uint16, []byte, error) {
	switch messageID {
	case MessageChainListRequest:
		return MessageChainListReply, ChainListPayload(), nil
	case MessageRelayListRequest:
		channels, err := s.channels.Channels(ctx)
		if err != nil {
			return 0, nil, fmt.Errorf("load broker channels: %w", err)
		}
		channels = LayoutChannelsForBroker(channels)
		payload, buildErr := RelayListPayload(advertisedIP, s.relayPort, channels)
		return MessageRelayListReply, payload, buildErr
	case MessageWorldListRequest, MessageWorldInfoRequest:
		channels, err := s.channels.Channels(ctx)
		if err != nil {
			return 0, nil, fmt.Errorf("load broker channels: %w", err)
		}
		channels = LayoutChannelsForBroker(channels)
		if messageID == MessageWorldListRequest {
			payload, buildErr := WorldListPayload(advertisedIP, channels)
			return MessageWorldListReply, payload, buildErr
		}
		payload, buildErr := WorldInfoPayload(channels)
		return MessageWorldInfoReply, payload, buildErr
	default:
		return 0, nil, nil
	}
}
