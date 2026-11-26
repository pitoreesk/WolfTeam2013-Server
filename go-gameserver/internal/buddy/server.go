package buddy

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

const (
	buddyCapabilityCore         uint32 = 1 << 0
	buddyCapabilitySetExtUser   uint32 = 1 << 3
	buddyCapabilityGroupGetList uint32 = 1 << 5
	buddyCapabilityGroupAdd     uint32 = 1 << 6
	buddyCapabilityChat         uint32 = 1 << 9
	buddyCapabilityAdd          uint32 = 1 << 12
	buddyCapabilityAccept       uint32 = 1 << 13
	buddyCapabilityRemove       uint32 = 1 << 14
	buddyCapabilityUserState    uint32 = 1 << 15
	buddyRemoveP2PMessageID     uint16 = 0xc043

	buddySupportedCapabilities = buddyCapabilityCore |
		buddyCapabilitySetExtUser |
		buddyCapabilityGroupGetList |
		buddyCapabilityGroupAdd |
		buddyCapabilityChat |
		buddyCapabilityAdd |
		buddyCapabilityAccept |
		buddyCapabilityRemove |
		buddyCapabilityUserState
)

// Server owns the buddy-service TCP endpoint.
type Server struct {
	address               string
	backend               store.BuddyStore
	logger                *slog.Logger
	sessionsMu            sync.Mutex
	sessions              map[string]*buddySession
	subscribersByUsername map[string]map[*buddySession]struct{}
	nextTunnelSequence    uint32
}

type buddySession struct {
	connection net.Conn
	logger     *slog.Logger
	writeMu    sync.Mutex
	username   string
	account    store.BuddyAccount
	buddies    map[string]struct{}
	key        [16]byte
	auth       bool
}

func NewServer(address string, backend store.BuddyStore, logger *slog.Logger) *Server {
	return &Server{
		address: address, backend: backend, logger: logger,
		sessions:              make(map[string]*buddySession),
		subscribersByUsername: make(map[string]map[*buddySession]struct{}),
	}
}

func (s *Server) Run(ctx context.Context) error {
	return s.RunReady(ctx, nil)
}

// RunReady closes ready only after the Buddy listener is bound. The app uses
// this barrier so the game service cannot advertise an endpoint that is not
// accepting connections yet.
func (s *Server) RunReady(ctx context.Context, ready chan<- struct{}) error {
	return transport.ServeTCP(ctx, s.address, "buddy", func(listener net.Listener) {
		s.logger.Info("buddy TCP server started", "address", listener.Addr().String())
		if ready != nil {
			close(ready)
		}
	}, func(connection net.Conn) {
		logger := s.logger.With("remote", connection.RemoteAddr().String())
		err := s.handleConnection(ctx, connection, logger)
		if err != nil && ctx.Err() == nil {
			logger.Debug("buddy connection ended", "error", err)
		} else {
			logger.Debug("buddy client disconnected")
		}
	})
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn, logger *slog.Logger) error {
	defer connection.Close()
	session := &buddySession{connection: connection, logger: logger}
	defer s.unregisterSession(session)
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	logger.Info("buddy client connected", "local", connection.LocalAddr().String())
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-closed:
		}
	}()
	defer close(closed)
	var account store.BuddyAccount
	authenticated := false
	var precredential [4]byte
	precredentialIssued := false
	groupListSent := false
	mailboxDelivered := false
	sendGroupList := func() error {
		if groupListSent {
			return nil
		}
		payload, err := groupListReplyPayload(defaultBuddyGroups[:])
		if err != nil {
			return fmt.Errorf("build buddy group list: %w", err)
		}
		if err := s.write(session, ReplyGroupList, payload); err != nil {
			return err
		}
		groupListSent = true
		if authenticated && !mailboxDelivered {
			if err := s.deliverPendingMessages(ctx, session); err != nil {
				return err
			}
			mailboxDelivered = true
		}
		return nil
	}

	for {
		request, err := readFrame(connection)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read buddy frame: %w", err)
		}
		logger.Debug("buddy message received",
			"message_id", fmt.Sprintf("0x%04x", request.MessageID),
			"payload_bytes", len(request.Payload), "prefix", payloadPrefix(request.Payload))
		switch request.MessageID {
		case RequestPrecredential:
			payload := make([]byte, 8)
			if _, err := io.ReadFull(crand.Reader, payload[:4]); err != nil {
				return fmt.Errorf("generate buddy credential: %w", err)
			}
			copy(precredential[:], payload[:4])
			precredentialIssued = true
			// The precredential reply carries a random credential followed by a
			// fixed capability mask.
			binary.LittleEndian.PutUint32(payload[4:], buddySupportedCapabilities)
			if err := s.write(session, ReplyPrecredential, payload); err != nil {
				return err
			}
		case RequestLogin:
			s.unregisterSession(session)
			authenticated = false
			account = store.BuddyAccount{}
			groupListSent = false
			mailboxDelivered = false
			username, usernameErr := loginUsername(request.Payload)
			if usernameErr != nil {
				logger.Warn("invalid buddy login", "error", usernameErr)
				if err := s.write(session, ReplyLogin, loginReplyPayload(1)); err != nil {
					return err
				}
				continue
			}
			account = store.BuddyAccount{Username: username}
			relations := []store.BuddyRelation(nil)
			if s.backend != nil {
				var found bool
				account, found, err = s.backend.BuddyAccountByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("authenticate buddy account %q: %w", username, err)
				}
				if !found {
					logger.Warn("buddy login account not found", "username", username)
					if err := s.write(session, ReplyLogin, loginReplyPayload(1)); err != nil {
						return err
					}
					continue
				}
				relations, err = s.backend.Buddies(ctx, account.UserID)
				if err != nil {
					return fmt.Errorf("load buddy list for %q: %w", account.Username, err)
				}
			}
			if !precredentialIssued {
				logger.Warn("buddy login arrived without a precredential", "username", username)
				if err := s.write(session, ReplyLogin, loginReplyPayload(1)); err != nil {
					return err
				}
				continue
			}
			sessionKey, keyErr := tunnelSessionKey(username, precredential)
			if keyErr != nil {
				return fmt.Errorf("derive buddy tunnel key for %q: %w", username, keyErr)
			}
			listPayload, listErr := buddyListReplyPayload(relations)
			if listErr != nil {
				return fmt.Errorf("build buddy list for %q: %w", account.Username, listErr)
			}
			if err := s.write(session, ReplyBuddyList, listPayload); err != nil {
				return err
			}
			if err := s.write(session, ReplyLogin, loginReplyPayload(0)); err != nil {
				return err
			}
			authenticated = true
			s.registerSession(session, account, sessionKey, relations)
			logger.Info("buddy account authenticated", "user_id", account.UserID,
				"username", account.Username, "buddy_count", len(relations))
		case RequestTunnel:
			if !authenticated {
				logger.Warn("unauthenticated buddy tunnel request")
				continue
			}
			if err := s.routeTunnel(session, request.Payload); err != nil {
				logger.Warn("invalid buddy tunnel request", "error", err)
			}
		case RequestBulkTunnel:
			if !authenticated {
				logger.Warn("unauthenticated buddy bulk tunnel request")
				continue
			}
			if err := s.routeBulkTunnel(ctx, session, request.Payload); err != nil {
				logger.Warn("invalid buddy bulk tunnel request", "error", err)
			}
		case AckTunnel:
			sequences, ackErr := parseTunnelAcknowledgement(request.Payload)
			if ackErr != nil {
				logger.Warn("invalid buddy tunnel acknowledgement", "error", ackErr)
				continue
			}
			logger.Debug("buddy tunnel acknowledged", "sequences", sequences)
		case RequestAddBuddy:
			username, groupID, requestErr := parseAddBuddyRequest(request.Payload)
			if requestErr != nil || !authenticated || s.backend == nil {
				if requestErr != nil {
					logger.Warn("invalid buddy add request", "error", requestErr)
				}
				payload, _ := addBuddyReplyPayload(1, store.BuddyRelation{})
				if err := s.write(session, ReplyAddBuddy, payload); err != nil {
					return err
				}
				continue
			}
			relation, added, addErr := s.backend.AddBuddy(ctx, account.UserID, username, groupID)
			if addErr != nil {
				return fmt.Errorf("add buddy %q for %q: %w", username, account.Username, addErr)
			}
			result := uint16(0)
			if !added {
				result = 1
			} else {
				s.addSessionBuddy(session, relation.Username)
			}
			payload, payloadErr := addBuddyReplyPayload(result, relation)
			if payloadErr != nil {
				return fmt.Errorf("build add-buddy reply: %w", payloadErr)
			}
			if err := s.write(session, ReplyAddBuddy, payload); err != nil {
				return err
			}
			if added {
				s.sendCurrentPresence(session, relation.Username)
			}
		case RequestRemoveBuddy:
			username, requestErr := parseRemoveBuddyRequest(request.Payload)
			if requestErr != nil || !authenticated || s.backend == nil {
				if requestErr != nil {
					logger.Warn("invalid buddy remove request", "error", requestErr)
				}
				payload, _ := removeBuddyReplyPayload(1, "")
				if err := s.write(session, ReplyRemoveBuddy, payload); err != nil {
					return err
				}
				continue
			}
			targetAccount, targetFound, targetErr := s.backend.BuddyAccountByUsername(ctx, username)
			if targetErr != nil {
				return fmt.Errorf("resolve removed buddy %q for %q: %w", username, account.Username, targetErr)
			}
			removed, removeErr := s.backend.RemoveBuddy(ctx, account.UserID, username)
			if removeErr != nil {
				return fmt.Errorf("remove buddy %q for %q: %w", username, account.Username, removeErr)
			}
			s.removeSessionBuddy(session, username)
			if removed && targetFound {
				if _, removeErr := s.backend.RemoveBuddy(ctx, targetAccount.UserID, account.Username); removeErr != nil {
					return fmt.Errorf("remove reciprocal buddy %q for %q: %w", account.Username, targetAccount.Username, removeErr)
				}
				s.removeSessionBuddyByUsername(targetAccount.Username, account.Username)
				if err := s.queueOfflineDelete(ctx, session, account, targetAccount); err != nil {
					return err
				}
			}
			payload, payloadErr := removeBuddyReplyPayload(0, username)
			if payloadErr != nil {
				return fmt.Errorf("build remove-buddy reply: %w", payloadErr)
			}
			if err := s.write(session, ReplyRemoveBuddy, payload); err != nil {
				return err
			}
		case RequestUserState:
			username, usernameErr := decodeFixedUsername(request.Payload)
			if usernameErr != nil {
				return fmt.Errorf("decode buddy user-state target: %w", usernameErr)
			}
			s.sessionsMu.Lock()
			target := s.sessions[strings.ToLower(username)]
			online := target != nil && target.auth
			s.sessionsMu.Unlock()
			payload, payloadErr := userStatePayload(request.Payload, online)
			if payloadErr != nil {
				return payloadErr
			}
			if err := s.write(session, NotifyUserState, payload); err != nil {
				return err
			}
		case RequestSetExt:
			if err := s.write(session, ReplySetExt, setExtReplyPayload()); err != nil {
				return err
			}
			// Ensure group-list initialization has completed before processing
			// extended session state.
			if err := sendGroupList(); err != nil {
				return err
			}
		case RequestGroupList:
			if err := sendGroupList(); err != nil {
				return err
			}
		case RequestGroupAdd:
			if len(request.Payload) != 44 {
				return fmt.Errorf("buddy group-add payload has %d bytes, want 44", len(request.Payload))
			}
			if err := s.write(session, ReplyGroupAdd, groupAddReplyPayload()); err != nil {
				return err
			}
		default:
			logger.Debug("unimplemented buddy message", "message_id", fmt.Sprintf("0x%04x", request.MessageID))
		}
	}
}

func (s *Server) registerSession(session *buddySession, account store.BuddyAccount, key [16]byte, relations []store.BuddyRelation) {
	username := strings.ToLower(account.Username)
	buddies := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		buddies[strings.ToLower(relation.Username)] = struct{}{}
	}

	s.sessionsMu.Lock()
	if session.username != "" && s.sessions[session.username] == session {
		delete(s.sessions, session.username)
	}
	s.removeSubscriberEdgesLocked(session)
	previous := s.sessions[username]
	if previous != nil && previous != session {
		s.removeSubscriberEdgesLocked(previous)
	}
	session.username = username
	session.account = account
	session.buddies = buddies
	session.key = key
	session.auth = true
	s.sessions[username] = session
	s.addSubscriberEdgesLocked(session)

	onlineBuddies := make([]string, 0, len(buddies))
	for buddyUsername := range buddies {
		buddy := s.sessions[buddyUsername]
		if buddy != nil && buddy != session && buddy.auth {
			onlineBuddies = append(onlineBuddies, buddy.account.Username)
		}
	}
	subscribers := s.subscribersLocked(username, session)
	s.sessionsMu.Unlock()
	if previous != nil && previous != session {
		previous.logger.Debug("buddy session replaced by a newer login")
		_ = previous.connection.Close()
	}
	for _, buddyUsername := range onlineBuddies {
		s.sendPresence(session, buddyUsername, true)
	}
	for _, subscriber := range subscribers {
		s.sendPresence(subscriber, account.Username, true)
	}
}

func (s *Server) unregisterSession(session *buddySession) {
	s.sessionsMu.Lock()
	registered := session.username != "" && s.sessions[session.username] == session
	username := session.account.Username
	subscribers := []*buddySession(nil)
	if registered {
		delete(s.sessions, session.username)
		subscribers = s.subscribersLocked(session.username, session)
	}
	s.removeSubscriberEdgesLocked(session)
	session.username = ""
	session.account = store.BuddyAccount{}
	session.buddies = nil
	session.key = [16]byte{}
	session.auth = false
	s.sessionsMu.Unlock()
	if registered {
		for _, subscriber := range subscribers {
			s.sendPresence(subscriber, username, false)
		}
	}
}

func (s *Server) addSessionBuddy(session *buddySession, username string) {
	s.sessionsMu.Lock()
	if session.auth {
		if session.buddies == nil {
			session.buddies = make(map[string]struct{})
		}
		buddyUsername := strings.ToLower(username)
		if _, exists := session.buddies[buddyUsername]; !exists {
			session.buddies[buddyUsername] = struct{}{}
			s.addSubscriberEdgeLocked(buddyUsername, session)
		}
	}
	s.sessionsMu.Unlock()
}

func (s *Server) removeSessionBuddy(session *buddySession, username string) {
	s.sessionsMu.Lock()
	if session.buddies != nil {
		buddyUsername := strings.ToLower(username)
		delete(session.buddies, buddyUsername)
		s.removeSubscriberEdgeLocked(buddyUsername, session)
	}
	s.sessionsMu.Unlock()
}

func (s *Server) removeSessionBuddyByUsername(ownerUsername, buddyUsername string) {
	s.sessionsMu.Lock()
	owner := s.sessions[strings.ToLower(ownerUsername)]
	if owner != nil && owner.buddies != nil {
		normalizedBuddy := strings.ToLower(buddyUsername)
		delete(owner.buddies, normalizedBuddy)
		s.removeSubscriberEdgeLocked(normalizedBuddy, owner)
	}
	s.sessionsMu.Unlock()
}

func (s *Server) addSubscriberEdgesLocked(session *buddySession) {
	for buddyUsername := range session.buddies {
		s.addSubscriberEdgeLocked(buddyUsername, session)
	}
}

func (s *Server) addSubscriberEdgeLocked(username string, session *buddySession) {
	if s.subscribersByUsername == nil {
		s.subscribersByUsername = make(map[string]map[*buddySession]struct{})
	}
	subscribers := s.subscribersByUsername[username]
	if subscribers == nil {
		subscribers = make(map[*buddySession]struct{})
		s.subscribersByUsername[username] = subscribers
	}
	subscribers[session] = struct{}{}
}

func (s *Server) removeSubscriberEdgesLocked(session *buddySession) {
	for buddyUsername := range session.buddies {
		s.removeSubscriberEdgeLocked(buddyUsername, session)
	}
}

func (s *Server) removeSubscriberEdgeLocked(username string, session *buddySession) {
	subscribers := s.subscribersByUsername[username]
	if subscribers == nil {
		return
	}
	delete(subscribers, session)
	if len(subscribers) == 0 {
		delete(s.subscribersByUsername, username)
	}
}

func (s *Server) subscribersLocked(username string, exclude *buddySession) []*buddySession {
	indexed := s.subscribersByUsername[username]
	result := make([]*buddySession, 0, len(indexed))
	for candidate := range indexed {
		if candidate != exclude && candidate.auth {
			result = append(result, candidate)
		}
	}
	return result
}

func (s *Server) sendPresence(target *buddySession, username string, online bool) {
	identity := make([]byte, buddyUsernameBytes)
	if err := encodeFixedUsername(identity, username); err != nil {
		target.logger.Warn("invalid buddy presence identity", "username", username, "error", err)
		return
	}
	payload, err := userStatePayload(identity, online)
	if err != nil {
		target.logger.Warn("build buddy presence notification", "username", username, "error", err)
		return
	}
	if err := s.write(target, NotifyUserState, payload); err != nil {
		target.logger.Debug("buddy presence delivery failed", "username", username, "online", online, "error", err)
		_ = target.connection.Close()
	}
}

func (s *Server) sendCurrentPresence(target *buddySession, username string) {
	s.sessionsMu.Lock()
	session := s.sessions[strings.ToLower(username)]
	online := session != nil && session.auth
	s.sessionsMu.Unlock()
	s.sendPresence(target, username, online)
}

func (s *Server) deliverPendingMessages(ctx context.Context, session *buddySession) error {
	mailbox, ok := s.backend.(store.BuddyMailboxStore)
	if !ok {
		return nil
	}
	s.sessionsMu.Lock()
	if !session.auth {
		s.sessionsMu.Unlock()
		return nil
	}
	account := session.account
	s.sessionsMu.Unlock()

	messages, err := mailbox.PendingBuddyMessages(ctx, account.UserID)
	if err != nil {
		return fmt.Errorf("load pending buddy messages for %q: %w", account.Username, err)
	}
	for _, message := range messages {
		notification, err := bulkTunnelNotificationPayload(message.Sender, message.MessageID, message.Body)
		if err != nil {
			return fmt.Errorf("build pending buddy message %d: %w", message.ID, err)
		}
		if err := s.write(session, NotifyBulkTunnel, notification); err != nil {
			return err
		}
		if err := mailbox.DeleteBuddyMessage(ctx, account.UserID, message.ID); err != nil {
			return err
		}
		session.logger.Debug("pending buddy message delivered", "source", message.Sender.Username,
			"p2p_message_id", fmt.Sprintf("0x%04x", message.MessageID), "payload_bytes", len(message.Body))
	}
	return nil
}

func (s *Server) queueOfflineDelete(ctx context.Context, source *buddySession, sourceAccount, targetAccount store.BuddyAccount) error {
	mailbox, ok := s.backend.(store.BuddyMailboxStore)
	if !ok {
		return nil
	}
	s.sessionsMu.Lock()
	target := s.sessions[strings.ToLower(targetAccount.Username)]
	online := target != nil && target.auth
	s.sessionsMu.Unlock()
	if online {
		return nil
	}

	// Route the embedded message through the server-relay envelope.
	messageRowID, stored, err := mailbox.QueueBuddyMessage(ctx, sourceAccount.UserID,
		targetAccount.Username, buddyRemoveP2PMessageID, nil)
	if err != nil {
		return fmt.Errorf("queue offline buddy delete for %q: %w", targetAccount.Username, err)
	}
	if !stored {
		return nil
	}
	source.logger.Debug("offline buddy delete queued", "target", targetAccount.Username,
		"p2p_message_id", fmt.Sprintf("0x%04x", buddyRemoveP2PMessageID))

	// Close the login race around the database insert. The normal login path
	// drains the row; if authentication completed meanwhile, deliver it here.
	s.sessionsMu.Lock()
	target = s.sessions[strings.ToLower(targetAccount.Username)]
	online = target != nil && target.auth
	recipientUserID := 0
	if online {
		recipientUserID = target.account.UserID
	}
	s.sessionsMu.Unlock()
	if !online {
		return nil
	}
	notification, err := bulkTunnelNotificationPayload(sourceAccount, buddyRemoveP2PMessageID, nil)
	if err != nil {
		return err
	}
	if err := s.write(target, NotifyBulkTunnel, notification); err != nil {
		_ = target.connection.Close()
		return nil
	}
	return mailbox.DeleteBuddyMessage(ctx, recipientUserID, messageRowID)
}

// nextTunnelSequenceLocked returns a non-zero sequence while sessionsMu is held.
func (s *Server) nextTunnelSequenceLocked() uint32 {
	s.nextTunnelSequence++
	if s.nextTunnelSequence == 0 {
		s.nextTunnelSequence++
	}
	return s.nextTunnelSequence
}

func (s *Server) routeTunnel(source *buddySession, payload []byte) error {
	s.sessionsMu.Lock()
	if !source.auth {
		s.sessionsMu.Unlock()
		return fmt.Errorf("source session is not authenticated")
	}
	sourceAccount := source.account
	sourceKey := source.key
	s.sessionsMu.Unlock()

	request, err := parseTunnelRequest(payload, sourceKey)
	if err != nil {
		return err
	}

	s.sessionsMu.Lock()
	target := s.sessions[strings.ToLower(request.TargetUsername)]
	if target == nil || !target.auth {
		s.sessionsMu.Unlock()
		source.logger.Debug("buddy tunnel target is offline", "target", request.TargetUsername,
			"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID))
		return nil
	}
	targetKey := target.key
	sequence := s.nextTunnelSequenceLocked()
	s.sessionsMu.Unlock()

	notification, err := tunnelNotificationPayload(sequence, sourceAccount, request.MessageID, request.Body, targetKey)
	if err != nil {
		return err
	}
	if err := s.write(target, NotifyTunnel, notification); err != nil {
		target.logger.Debug("buddy tunnel delivery failed", "source", sourceAccount.Username,
			"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID), "error", err)
		_ = target.connection.Close()
		return nil
	}
	source.logger.Debug("buddy tunnel routed", "target", request.TargetUsername,
		"sequence", sequence, "p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID),
		"payload_bytes", len(request.Body))
	return nil
}

func (s *Server) routeBulkTunnel(ctx context.Context, source *buddySession, payload []byte) error {
	s.sessionsMu.Lock()
	if !source.auth {
		s.sessionsMu.Unlock()
		return fmt.Errorf("source session is not authenticated")
	}
	sourceAccount := source.account
	s.sessionsMu.Unlock()

	request, err := parseBulkTunnelRequest(payload)
	if err != nil {
		return err
	}
	notification, err := bulkTunnelNotificationPayload(sourceAccount, request.MessageID, request.Body)
	if err != nil {
		return err
	}

	mailbox, hasMailbox := s.backend.(store.BuddyMailboxStore)
	delivered := 0
	queued := 0
	for _, targetUsername := range request.Targets {
		s.sessionsMu.Lock()
		target := s.sessions[strings.ToLower(targetUsername)]
		online := target != nil && target.auth
		s.sessionsMu.Unlock()

		if online {
			if err := s.write(target, NotifyBulkTunnel, notification); err == nil {
				delivered++
				continue
			} else {
				target.logger.Debug("buddy bulk tunnel delivery failed", "source", sourceAccount.Username,
					"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID), "error", err)
				_ = target.connection.Close()
			}
		}

		if !hasMailbox {
			source.logger.Debug("buddy bulk tunnel target is offline", "target", targetUsername,
				"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID))
			continue
		}
		messageRowID, stored, err := mailbox.QueueBuddyMessage(ctx, sourceAccount.UserID,
			targetUsername, request.MessageID, request.Body)
		if err != nil {
			return err
		}
		if !stored {
			source.logger.Debug("buddy bulk tunnel target was not queued", "target", targetUsername,
				"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID))
			continue
		}
		queued++

		// Close the race where the target authenticates while the database write
		// is in flight. Deliver immediately and remove the queued copy if it is
		// now online; otherwise its login path will drain the mailbox.
		s.sessionsMu.Lock()
		target = s.sessions[strings.ToLower(targetUsername)]
		online = target != nil && target.auth
		recipientUserID := 0
		if online {
			recipientUserID = target.account.UserID
		}
		s.sessionsMu.Unlock()
		if online {
			if err := s.write(target, NotifyBulkTunnel, notification); err == nil {
				if err := mailbox.DeleteBuddyMessage(ctx, recipientUserID, messageRowID); err != nil {
					return err
				}
				delivered++
				queued--
			} else {
				_ = target.connection.Close()
			}
		}
	}
	source.logger.Debug("buddy bulk tunnel routed", "targets", len(request.Targets), "delivered", delivered, "queued", queued,
		"p2p_message_id", fmt.Sprintf("0x%04x", request.MessageID), "payload_bytes", len(request.Body))
	return nil
}

func (s *Server) write(session *buddySession, messageID uint16, payload []byte) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if err := writeFrame(session.connection, messageID, payload); err != nil {
		return fmt.Errorf("write buddy frame 0x%04x: %w", messageID, err)
	}
	session.logger.Debug("buddy message sent", "message_id", fmt.Sprintf("0x%04x", messageID),
		"payload_bytes", len(payload), "prefix", payloadPrefix(payload))
	return nil
}

func payloadPrefix(payload []byte) string {
	if len(payload) > 16 {
		payload = payload[:16]
	}
	return fmt.Sprintf("% x", payload)
}
