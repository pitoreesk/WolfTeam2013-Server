package game

import (
	"container/list"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wtserver-go/internal/crestdata"
	"wtserver-go/internal/ptbonus"
	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

const (
	powerUserItemID   = 10027
	powerMasterItemID = 10092
)

var powerRoomItems = [...]PowerRoomItem{
	{UniqueID: 2_000_003_005, ItemID: 3005}, // AKEI-47 PT
	{UniqueID: 2_000_003_007, ItemID: 3007}, // AUZ A2 PT
	{UniqueID: 2_000_003_030, ItemID: 3030}, // EM-4 PT
	{UniqueID: 2_000_003_033, ItemID: 3033}, // XEM-8 PT
	{UniqueID: 2_000_003_021, ItemID: 3021}, // EF-2000 PT
	{UniqueID: 2_000_006_005, ItemID: 6005}, // I-115 WT
	{UniqueID: 2_000_009_004, ItemID: 9004}, // MK-5 EG
}

type Server struct {
	address         string
	matchPort       uint16
	botPeerPortBase uint16
	buddyPort       uint16
	rankingWebPort  uint16
	loginOrder      atomic.Uint64
	advertisedIP    netip.Addr
	publicIP        netip.Addr
	users           store.GameRepository
	shopCatalog     *shopdata.Catalog
	crests          *crestdata.Catalog
	ptTiers         []ptbonus.Tier
	logger          *slog.Logger
	gameVersion     int
	connectionWrap  func(net.Conn) net.Conn
	roundEndDelay   time.Duration

	mu                 sync.RWMutex
	sessions           map[*Session]struct{}
	nextSessionID      uint16
	online             onlineRegistry
	rooms              map[int16]*Room
	occupiedRooms      [roomIndexWordCount]uint64
	joinableRooms      [roomIndexWordCount]uint64
	channelMembers     [channelSheetCount][channelSheetCapacity]*Session
	channelRosterMu    sync.Mutex
	channelSilenced    bool
	lobbyUpdates       lobbyUpdateState
	lobbyUpdateMetrics *lobbyUpdateMetrics

	privateLadderMu         sync.Mutex
	privateLadderQueue      *list.List
	ladderSearches          map[*Session]PrivateLadderSearchRequest
	privateLadderSearches   map[*Session]*list.Element
	privateLadderChallenges map[uint16]privateLadderChallenge
	privateChallengeByUser  map[*Session]uint16
	privateLadderSequence   uint16

	relayMu          sync.Mutex
	relayIP          netip.Addr
	relayPort        uint16
	relayPortBase    uint16
	peerEndpointMode int
	relayContext     context.Context
	relayHost        string
	relaySockets     map[uint16]*net.UDPConn
	relayOwners      map[uint16]map[int16]*Session
	relayRoutes      map[relayRouteKey]*net.UDPAddr
	relayPending     map[relayRouteKey][][]byte
	relayProbes      map[relayProbeKey]struct{}
}

func NewServer(address string, matchPort, botPeerPortBase uint16, advertisedIP, publicIP netip.Addr, users store.GameRepository, logger *slog.Logger) *Server {
	return &Server{
		address:                 address,
		matchPort:               matchPort,
		botPeerPortBase:         botPeerPortBase,
		advertisedIP:            advertisedIP,
		publicIP:                publicIP,
		users:                   users,
		crests:                  crestdata.Default(),
		ptTiers:                 ptbonus.Tiers(),
		logger:                  logger,
		gameVersion:             377,
		roundEndDelay:           10 * time.Second,
		sessions:                make(map[*Session]struct{}),
		nextSessionID:           0x0800,
		online:                  newOnlineRegistry(),
		rooms:                   make(map[int16]*Room),
		lobbyUpdates:            newLobbyUpdateState(),
		privateLadderQueue:      list.New(),
		ladderSearches:          make(map[*Session]PrivateLadderSearchRequest),
		privateLadderSearches:   make(map[*Session]*list.Element),
		privateLadderChallenges: make(map[uint16]privateLadderChallenge),
		privateChallengeByUser:  make(map[*Session]uint16),
		relayIP:                 advertisedIP,
		peerEndpointMode:        1,
		relaySockets:            make(map[uint16]*net.UDPConn),
		relayOwners:             make(map[uint16]map[int16]*Session),
		relayRoutes:             make(map[relayRouteKey]*net.UDPAddr),
		relayPending:            make(map[relayRouteKey][][]byte),
		relayProbes:             make(map[relayProbeKey]struct{}),
	}
}

// ConfigureConnectionWrapper installs an optional connection decorator before
// encrypted framing begins. It is intended for diagnostics such as load-test
// byte and syscall counters and must be configured before Run.
func (s *Server) ConfigureConnectionWrapper(wrapper func(net.Conn) net.Conn) {
	s.connectionWrap = wrapper
}

// ConfigureRoundEndDelay changes the default 10-second transition between the
// end-round and end-game packets. Load tests use a shorter delay so the full
// persistence lifecycle completes without adding ten seconds per room wave.
func (s *Server) ConfigureRoundEndDelay(delay time.Duration) {
	if delay >= 0 {
		s.roundEndDelay = delay
	}
}

func (s *Server) ConfigureGameVersion(version int) {
	if version >= 0 {
		s.gameVersion = version
	}
}

func (s *Server) ConfigureBuddy(port uint16) {
	s.buddyPort = port
}

func (s *Server) ConfigureShopCatalog(catalog *shopdata.Catalog) {
	s.shopCatalog = catalog
}

func purchaseComponents(components []shopdata.SetComponent) []store.PurchaseComponent {
	if len(components) == 0 {
		return nil
	}
	result := make([]store.PurchaseComponent, len(components))
	for index, component := range components {
		result[index] = store.PurchaseComponent{ItemID: component.ItemID, Days: component.Days}
	}
	return result
}

func purchaseGrant(option shopdata.Offer) (days, quantity int) {
	if len(option.Components) == 0 && (option.PricingMode == 4 || option.PricingMode == 5) {
		return 0, option.Days
	}
	return option.Days, 1
}

func (s *Server) rejectGiftPurchase(ctx context.Context, session *Session, userID int, request GiftSendRequest, validationErr error) error {
	info, err := s.users.AccountSnapshot(ctx, userID)
	if err != nil {
		s.logBackendFallback("load balances after rejected gift", userID, err)
	}
	s.logger.Warn("gift purchase rejected", "user_id", userID, "receiver", request.ReceiverCallsign,
		"item", request.ItemID, "days", request.Days, "packet_price", request.Price,
		"wolf_coin_reward", request.WolfCoinReward, "error", validationErr)
	return session.Write(ReplySendGift, SendGiftPayload(1, store.GiftSendResult{Gold: info.Gold, WC: info.WC, Cash: info.Cash}))
}

func (s *Server) ConfigureRelay(address netip.Addr, sharedPort, portBase uint16, endpointMode int) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	s.relayIP = address
	s.relayPort = sharedPort
	s.relayPortBase = portBase
	s.peerEndpointMode = endpointMode
}

func (s *Server) Run(ctx context.Context) error {
	return transport.ServeTCP(ctx, s.address, "game", func(listener net.Listener) {
		s.logger.Info("game TCP server started", "address", listener.Addr().String())
		go s.runRoomProcessor(ctx)
		if _, ok := s.users.(store.PrideStore); ok {
			go s.runPrideProcessor(ctx)
		}
	}, func(connection net.Conn) {
		if err := s.handleConnection(ctx, connection); err != nil && ctx.Err() == nil {
			s.logger.Warn("game connection ended", "remote", connection.RemoteAddr(), "error", err)
		}
	})
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) error {
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	if s.connectionWrap != nil {
		connection = s.connectionWrap(connection)
	}
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wire := transport.NewEncryptedConn(connection, nil)
	defer wire.Close()
	go func() {
		<-connectionCtx.Done()
		_ = wire.Close()
	}()

	peerIP := netip.IPv4Unspecified()
	if address, ok := tcpAddress(connection.RemoteAddr()); ok && address.Is4() {
		peerIP = address
	}
	session, err := newSession(wire, s.connectionAdvertisedIP(connection), peerIP)
	if err != nil {
		return err
	}
	session.logger = s.logger
	if err := s.addConnectionSession(session); err != nil {
		return err
	}
	defer func() {
		if s.removeSession(session) {
			s.broadcastChannelRoster()
		}
	}()
	s.logger.Info("game client connected", "remote", wire.RemoteAddr(), "advertised_ip", session.AdvertisedIP, "session_id", fmt.Sprintf("0x%04x", session.SessionID()))

	for {
		frame, readErr := wire.ReadFrame()
		if readErr != nil {
			if transport.IsClosed(readErr) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read game frame: %w", readErr)
		}
		s.logger.Debug("game message received", "message_id", fmt.Sprintf("0x%04x", frame.MessageID), "sequence", frame.Sequence, "bytes", len(frame.Payload)+8)
		if handleErr := s.handleMessage(connectionCtx, session, frame); handleErr != nil {
			// A malformed or failed request does not invalidate the encrypted stream.
			s.logger.Warn("game message failed", "message_id", fmt.Sprintf("0x%04x", frame.MessageID), "error", handleErr)
		}
	}
}

func (s *Server) handleMessage(ctx context.Context, session *Session, frame transport.Frame) error {
	switch frame.MessageID {
	case RequestGameGuard, RequestXTrap, RequestApexClientInfo:
		return s.handleClientTelemetry(ctx, session, frame)
	case RequestChangeNickname, RequestCheckVersion, RequestChangeSchool,
		RequestWolfPoint, RequestEventList, RequestAskRecommend, RequestAnswerRecommend:
		return s.handleChannelAccountMessage(ctx, session, frame)
	case RequestGetUserID:
		return s.handleGetUserID(ctx, session, frame)
	case RequestGMCommand:
		request, err := ParseGMCommandRequest(frame.Payload)
		if err != nil {
			return err
		}
		return s.handleGMCommand(ctx, session, request)
	case RequestPrivateInfo, RequestPrivateNotice, RequestPrivateRankList,
		RequestPrivateFindStart, RequestPrivateFindEnd, RequestPrivateChallenge,
		RequestPrivateResult, RequestPrivateLevel, RequestPrivateReceive, RequestLoginRestrict:
		return s.handlePrivateLadderMessage(ctx, session, frame)
	case RequestPrideCreate, RequestPrideClose, RequestPrideList, RequestPrideInfo,
		RequestPrideAskCancel, RequestPrideAsk, RequestPrideLeave, RequestPrideAccept,
		RequestPrideWaiting, RequestPrideMessages, RequestPrideMemo, RequestPrideMemoAdd,
		RequestPrideMemoDelete, RequestPrideMembers, RequestPrideMaster, RequestPrideKick,
		RequestPrideChanged, RequestPrideChangedAck, RequestPrideFullInfo, RequestPrideCloseCancel,
		RequestPrideResult, RequestPrideBattleList, RequestPrideMemberIDs, RequestMyPrideInfo, RequestMyPrideChain,
		RequestPrideChainList, RequestPrideRecentRank:
		return s.handlePrideMessage(ctx, session, frame)
	case 0x1670:
		return nil
	case RequestChannelLogin:
		return s.handleLogin(ctx, session, frame.Payload)
	case RequestUDPAddress:
		return session.Write(ReplyUDPAddress, UDPAddressPayload())
	case RequestUDPSuccess:
		payload, err := UDPSuccessPayload(session.AdvertisedIP, s.matchPort)
		if err != nil {
			return err
		}
		return session.Write(ReplyUDPSuccess, payload)
	case RequestKeepAlive:
		session.mu.Lock()
		if session.UDPKicked {
			session.mu.Unlock()
			return nil
		}
		session.UDPKicked = true
		session.mu.Unlock()
		return session.Write(ReplyUDPAddress, UDPAddressPayload())
	case RequestUserInfo:
		_, callsign, userID, level := session.identity()
		info, err := s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load user info", userID, err)
			info = store.AccountSnapshot{Level: int(level)}
		}
		pride := store.Pride{}
		member := store.PrideMember{}
		schoolID := uint32(0)
		if accounts, ok := s.users.(store.ChannelAccountStore); ok {
			accountState, accountErr := accounts.ChannelAccountState(ctx, userID)
			if accountErr != nil {
				s.logBackendFallback("load channel account state", userID, accountErr)
			} else {
				schoolID = accountState.SchoolID
			}
		}
		if prides, ok := s.users.(store.PrideStore); ok {
			loadedPride, loadedMember, found, prideErr := prides.PrideForUser(ctx, userID)
			if prideErr != nil {
				s.logBackendFallback("load authenticated user's pride", userID, prideErr)
			} else if found {
				pride, member = loadedPride, loadedMember
			}
		}
		payload, err := userInfoWithPrideAndPlayTimePayload(callsign, info, pride, member, schoolID, session.playTimeSeconds())
		if err != nil {
			return err
		}
		if err := session.Write(ReplyUserInfo, payload); err != nil {
			return err
		}
		// The 2012 client learns tutorial progress only from 0x1345 (handled
		// by the pump in every state; WolfInterfaceManager +0x415DC/+0x415E0
		// are otherwise only reset) and requests it only after a reward, so
		// the Battle Room would show cleared chapters as locked. Send it with
		// the login user info.
		return s.writeTutorialProgress(ctx, session)
	case RequestTutorialClear:
		return s.writeTutorialProgress(ctx, session)
	case RequestChannelChat:
		_, callsign, userID, _ := session.identity()
		chat, err := ParseChannelChatRequest(frame.Payload)
		if err != nil {
			return err
		}
		text := chat.Text
		if command, ok, parseErr := ParseGMChatCommand(text); ok {
			if parseErr != nil {
				if !session.hasStaffAuthority() {
					return nil
				}
				return s.sendGMNotice(session, parseErr.Error())
			}
			return s.handleGMCommand(ctx, session, command)
		}
		if !s.channelChatAllowed(session) {
			return nil
		}
		color, label := session.powerUserSettings()
		session.mu.RLock()
		authority := session.UserAuthority
		session.mu.RUnlock()
		payload, err := ChannelChatPayload(frame.Payload, callsign, authority,
			s.hasActiveItem(ctx, userID, powerUserItemID), color, label)
		if err != nil {
			return err
		}
		recipients, err := s.channelChatRecipients(ctx, session, chat.Type)
		if err != nil {
			s.logBackendFallback("resolve channel chat recipients", userID, err)
			return err
		}
		s.broadcastChannelTo(recipients, ReplyChannelChat, payload)
		// Debug shortcut that ends the round as a win for the speaker's team;
		// restricted to staff so ordinary players cannot abuse it.
		if room := s.sessionRoom(session); room != nil && strings.EqualFold(text, "autowin") && session.hasStaffAuthority() {
			session.mu.RLock()
			team := session.RoomTeam
			session.mu.RUnlock()
			s.finishRound(room, team, true)
		}
		return nil
	case RequestCharStdInfo:
		return s.handleCharStdInfo(ctx, session, frame.Payload)
	case RequestCrestUser:
		return s.handleCrestUser(ctx, session)
	case RequestCrestEquip:
		return s.handleCrestEquip(ctx, session, frame.Payload)
	case RequestCrestChallenge:
		return s.handleCrestChallenge(ctx, session, frame.Payload)
	case RequestCrestCancel:
		return s.handleCrestCancel(ctx, session, frame.Payload)
	case RequestGetPowerUser:
		_, _, userID, _ := session.identity()
		color, label := session.powerUserSettings()
		payload, err := PowerUserInfoPayload(s.hasActiveItem(ctx, userID, powerUserItemID), color, label)
		if err != nil {
			return err
		}
		return session.Write(ReplyGetPowerUser, payload)
	case RequestSetPowerUser:
		settings, err := ParsePowerUserSettings(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		if !s.hasActiveItem(ctx, userID, powerUserItemID) {
			return session.Write(ReplySetPowerUser, PowerUserStatusPayload(1))
		}
		settings.Label = truncateUTF16(strings.TrimSpace(settings.Label), 16)
		session.setPowerUserSettings(settings.Color, settings.Label)
		if powerUsers, ok := s.users.(store.PowerUserStore); ok {
			if err := powerUsers.SavePowerUserSettings(ctx, userID, settings.Color, settings.Label); err != nil {
				s.logBackendFallback("save power-user settings", userID, err)
			}
		}
		return session.Write(ReplySetPowerUser, PowerUserStatusPayload(0))
	case RequestItemCheck:
		_, _, userID, _ := session.identity()
		equipment, err := s.users.Equipment(ctx, userID)
		if err != nil {
			s.logBackendFallback("load equipment for item check", userID, err)
		}
		powerSlots, err := s.users.PowerSlots(ctx, userID)
		if err != nil {
			s.logBackendFallback("load power slots for item check", userID, err)
		}
		return session.Write(ReplyItemCheck, ItemCheckPayload(equipment, powerSlots))
	case RequestSelectChar:
		payload, err := SelectCharPayload(session.AdvertisedIP, s.matchPort)
		if err != nil {
			return err
		}
		return session.Write(ReplySelectChar, payload)
	case RequestSNSAccountSave:
		// sub_34208280 sends an SNS id and password; the ACK (sub_34206390)
		// reads nothing. The credentials are deliberately not stored.
		return session.Write(ReplySNSAccountSave, []byte{0})
	case RequestSNSAccount:
		return session.Write(ReplySNSAccount, SNSAccountPayload())
	case RequestEventShop:
		_, _, userID, _ := session.identity()
		shop, premium := s.premiumShop(ctx, userID)
		return session.Write(ReplyEventShop, EventShopPayload(s.vipLounges(ctx), premium && shop.Enabled && len(shop.Items) != 0))
	case RequestPremiumShopList:
		_, _, userID, _ := session.identity()
		shop, _ := s.premiumShop(ctx, userID)
		return session.Write(ReplyPremiumShopList, PremiumShopPayload(shop, s.shopCatalog.PremiumMeta))
	case RequestDisconnect:
		return session.Write(ReplyDisconnect, DisconnectPayload())
	case RequestLogout:
		return session.Write(ReplyLogout, LogoutPayload())
	case RequestMacroMessage:
		var macros []MacroMessage
		if macroStore, ok := s.users.(store.MacroStore); ok {
			_, _, userID, _ := session.identity()
			saved, err := macroStore.Macros(ctx, userID)
			if err != nil {
				s.logBackendFallback("load macros", userID, err)
			}
			for _, macro := range saved {
				macros = append(macros, MacroMessage{Slot: macro.Slot, Flag: macro.Flag, Text: macro.Text})
			}
		}
		payload, err := MacroMessagePayload(macros)
		if err != nil {
			return err
		}
		return session.Write(ReplyMacroMessage, payload)
	case RequestSetMacroMessage:
		// sub_3432C490: u8 n, n x {u8 slot, u8 flag, u8 bytes, UTF-16 text};
		// the ACK (sub_342062B0) reads nothing.
		macros, err := ParseSetMacroRequest(frame.Payload)
		if err == nil {
			if macroStore, ok := s.users.(store.MacroStore); ok {
				_, _, userID, _ := session.identity()
				if err := macroStore.SaveMacros(ctx, userID, macros); err != nil {
					s.logBackendFallback("save macros", userID, err)
				}
			}
		}
		return session.Write(ReplySetMacroMessage, []byte{0})
	case RequestGiftItemCount:
		_, _, userID, _ := session.identity()
		count, err := s.users.GiftCount(ctx, userID)
		if err != nil {
			s.logBackendFallback("count gifts", userID, err)
			count = 0
		}
		return session.Write(ReplyGiftItemCount, GiftItemCountPayload(GiftMailboxCapacity, count))
	case RequestGiftPeriodEnd:
		return session.Write(ReplyGiftPeriodEnd, GiftPeriodEndPayload())
	case RequestSendGift:
		request, err := ParseGiftSendRequest(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		currency, price := 0, uint32(0)
		days, quantity := request.Days, 1
		var components []store.PurchaseComponent
		if s.shopCatalog != nil {
			option, validationErr := s.shopCatalog.ValidateGift(
				request.ItemID, request.Days, request.Price, request.WolfCoinReward,
			)
			if validationErr != nil {
				return s.rejectGiftPurchase(ctx, session, userID, request, validationErr)
			}
			currency, price = option.Currency, option.Price
			days, quantity = purchaseGrant(option)
			components = purchaseComponents(option.Components)
		} else {
			currency, price, err = GiftSendPayment(request)
			if err != nil {
				return s.rejectGiftPurchase(ctx, session, userID, request, err)
			}
		}
		result, err := s.users.SendGift(ctx, store.GiftSendRequest{
			SenderID: userID, ReceiverCallsign: request.ReceiverCallsign,
			ItemID: request.ItemID, ItemType: request.ItemType, Days: days, Quantity: quantity,
			Currency: currency, Price: price, Message: request.Message, Components: components,
		})
		if err != nil {
			s.logBackendFallback("send gift", userID, err)
			result = store.GiftSendResult{}
		}
		status := byte(1)
		if result.Success {
			status = 0
		}
		s.logger.Debug("gift sent", "sender_user_id", userID, "receiver", request.ReceiverCallsign,
			"item", request.ItemID, "item_type", request.ItemType, "days", request.Days,
			"packet_price", request.Price, "wolf_coin_reward", request.WolfCoinReward,
			"currency", currency, "charged_price", price, "success", result.Success, "reason", result.Error)
		if err := session.Write(ReplySendGift, SendGiftPayload(status, result)); err != nil {
			return err
		}
		if result.Success {
			s.recordVIPSpend(ctx, session, currency, int(price))
			s.awardCrestEvent(ctx, session, crestdata.GroupGift)
			if recipient := s.sessionByCallsign(request.ReceiverCallsign); recipient != nil {
				_ = recipient.Write(ReplyNewGift, []byte{0})
			}
		}
		return nil
	case RequestGiftItemList:
		request, err := ParseGiftListRequest(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		page, err := s.users.Gifts(ctx, userID, request.Kind, int(request.Page), int(request.PageSize))
		if err != nil {
			s.logBackendFallback("load gift list", userID, err)
			page = store.GiftPage{}
		}
		payload, err := GiftItemListPayload(request, page)
		if err != nil {
			return err
		}
		return session.Write(ReplyGiftItemList, payload)
	case RequestGiftDetail:
		request, err := ParseGiftDetailRequest(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		gift, found, err := s.users.GiftDetail(ctx, userID, request.GiftID, request.Kind)
		if err != nil {
			s.logBackendFallback(fmt.Sprintf("load gift %d", request.GiftID), userID, err)
			found = false
		}
		wc, gold := s.giftRefundValue(gift)
		payload, err := GiftDetailPayload(request.Kind, gift, found, wc, gold)
		if err != nil {
			return err
		}
		return session.Write(ReplyGiftDetail, payload)
	case RequestGiftUse:
		giftID, err := ParseGiftClaimRequest(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		result, err := s.users.ClaimGift(ctx, userID, giftID)
		if err != nil {
			s.logBackendFallback(fmt.Sprintf("claim gift %d", giftID), userID, err)
			result = store.GiftClaimResult{}
		}
		if !result.Success {
			if info, infoErr := s.users.AccountSnapshot(ctx, userID); infoErr == nil {
				result.Gold = info.Gold
				result.Cash = info.Cash
			}
		} else {
			session.invalidateRosterState()
		}
		return session.Write(ReplyGiftUse, GiftUsePayload(result))
	case RequestGiftNickCheck:
		receiver, err := ParseGiftReceiverCheckRequest(frame.Payload)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		found := false
		if lookups, ok := s.users.(store.BuddyLookupStore); ok {
			_, found, err = lookups.BuddyIdentityByCallsign(ctx, receiver)
			if err != nil {
				s.logBackendFallback("look up gift receiver", userID, err)
				found = false
			}
		}
		if !found {
			_, found = s.onlineBuddyIdentity(receiver)
		}
		s.logger.Debug("gift receiver nickname check", "receiver", receiver, "found", found)
		return session.Write(ReplyGiftNickCheck, GiftReceiverCheckPayload(found))
	case RequestEventRewardList:
		return session.Write(ReplyEventRewardList, EventRewardListPayload(s.eventBonuses(ctx)))
	case RequestItemLimited:
		return session.Write(ReplyItemLimited, LimitedItemsPayload())
	case RequestWPackSubItems:
		// 2012 CS_IN_W_PACK_SUB_ITEM_REQ (sub_341CBC90): u16 pack item ID only.
		var items []WPackSubItem
		if len(frame.Payload) >= 2 {
			packID := int(binary.LittleEndian.Uint16(frame.Payload[:2]))
			for _, component := range s.shopCatalog.WPackComponents(packID) {
				items = append(items, WPackSubItem{ItemID: uint16(component.ItemID), Value: uint16(component.Days)})
			}
		}
		payload, err := WPackSubItemsPayload(items)
		if err != nil {
			return err
		}
		return session.Write(ReplyWPackSubItems, payload)
	case RequestItemList, RequestCharItemList:
		_, _, userID, _ := session.identity()
		items, err := s.users.Items(ctx, userID)
		if err != nil {
			s.logBackendFallback("load user inventory", userID, err)
			items = nil
		}
		if frame.MessageID == RequestItemList {
			var temporary []PowerRoomItem
			if room := s.sessionRoom(session); room != nil && room.Snapshot().PowerMaster {
				temporary = powerRoomItems[:]
			}
			payload, buildErr := ItemListPayloadWithPowerRoomItems(items, temporary)
			if buildErr != nil {
				return buildErr
			}
			return session.Write(ReplyItemList, payload)
		}
		payload, buildErr := CharacterItemListPayload(items)
		if buildErr != nil {
			return buildErr
		}
		return session.Write(ReplyCharItemList, payload)
	case RequestEquipList:
		_, _, userID, _ := session.identity()
		equipment, err := s.users.Equipment(ctx, userID)
		if err != nil {
			s.logBackendFallback("load user equipment", userID, err)
		}
		powerSlots, err := s.users.PowerSlots(ctx, userID)
		if err != nil {
			s.logBackendFallback("load power slots", userID, err)
		}
		return session.Write(ReplyEquipList, EquipmentListPayload(equipment, powerSlots))
	case RequestCharInfo:
		return s.handleCharacterInfo(ctx, session)
	case RequestGetGoldCash:
		_, _, userID, _ := session.identity()
		info, err := s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load currency", userID, err)
			info = store.AccountSnapshot{}
		}
		return session.Write(ReplyGetGoldCash, CurrencyPayload(info))
	case RequestBuyItem:
		request, err := ParseBuyRequest(frame.Payload)
		if err != nil {
			return session.Write(ReplyBuyItem, BuyItemReplyPayload(BuyRequest{}, store.PurchaseReceipt{}, 1, false))
		}
		_, _, userID, _ := session.identity()
		currency := 0
		days, quantity := request.Days, 1
		var components []store.PurchaseComponent
		if s.shopCatalog != nil {
			option, validationErr := s.shopCatalog.ValidatePurchase(
				request.ItemID, request.Days, request.PurchaseMode, request.Price, request.WolfCoinReward,
			)
			if validationErr != nil {
				if premium, ok := s.premiumOffer(ctx, userID, request); ok {
					option, validationErr = premium, nil
				}
			}
			if validationErr != nil {
				s.logger.Warn("shop purchase rejected", "user_id", userID, "item", request.ItemID,
					"days", request.Days, "purchase_mode", request.PurchaseMode, "price", request.Price,
					"wolf_coin_reward", request.WolfCoinReward, "error", validationErr)
				return session.Write(ReplyBuyItem, BuyItemReplyPayload(request, store.PurchaseReceipt{}, 1, false))
			}
			currency = option.Currency
			days, quantity = purchaseGrant(option)
			components = purchaseComponents(option.Components)
		} else {
			currency, _, err = BuyRequestPayment(request)
			if err != nil {
				return session.Write(ReplyBuyItem, BuyItemReplyPayload(request, store.PurchaseReceipt{}, 1, false))
			}
		}
		itemType := 0
		if request.ItemID >= 30000 {
			itemType = 1
		}
		var before []store.Item
		if len(components) != 0 {
			if before, err = s.users.Items(ctx, userID); err != nil {
				s.logBackendFallback("load inventory before set purchase", userID, err)
				before = nil
			}
		}
		result, err := s.users.CommitPurchase(ctx, store.PurchaseOrder{
			UserID: userID, ItemID: request.ItemID, ItemType: itemType,
			Days: days, Quantity: quantity, Currency: currency, Price: request.Price, Components: components,
		})
		if err != nil {
			s.logBackendFallback(fmt.Sprintf("buy item %d", request.ItemID), userID, err)
			return session.Write(ReplyBuyItem, BuyItemReplyPayload(request, store.PurchaseReceipt{}, 1, false))
		}
		status := byte(0)
		if !result.Success {
			status = 1
		}
		if result.Success && (request.ItemID >= 10050 && request.ItemID <= 10059 || request.ItemID >= 10061 && request.ItemID <= 10064) {
			if err := s.users.SyncSlotVouchers(ctx, userID); err != nil {
				s.logBackendFallback("sync purchased slot voucher", userID, err)
			}
		}
		if result.Success {
			session.invalidateRosterState()
		}
		if err := session.Write(ReplyBuyItem, BuyItemReplyPayload(request, result, status, false)); err != nil {
			return err
		}
		if result.Success && len(components) != 0 {
			// 2013 TR BUYITEM_ACK (sub_3418A1F0) adds only its one record; sets and
			// W-Packs grant several rows, so resend the inventory like a package use.
			if err := s.pushInventory(ctx, session, userID, before, map[int]bool{request.ItemID: true}); err != nil {
				return err
			}
		}
		if result.Success {
			s.recordVIPSpend(ctx, session, currency, int(request.Price))
			s.grantEventBonus(ctx, session, userID, request.ItemID, request.Days)
		}
		return nil
	case RequestSellItem:
		uniqueID, itemID, err := ItemIDFromUniquePayload(frame.Payload)
		if err != nil {
			return session.Write(ReplySellItem, SellItemReplyPayload(store.SellResult{}, 0))
		}
		_, _, userID, _ := session.identity()
		result, err := s.users.SellItem(ctx, userID, itemID)
		if err != nil {
			s.logBackendFallback("sell inventory item", userID, err)
			result = store.SellResult{}
		}
		if result.Success {
			session.invalidateRosterState()
		}
		return session.Write(ReplySellItem, SellItemReplyPayload(result, uniqueID))
	case RequestUpgradeItem:
		_, _, userID, _ := session.identity()
		request, err := ParseItemUpgradeRequest(frame.Payload, false)
		if err != nil {
			s.logger.Warn("parse item upgrade failed", "user_id", userID, "error", err, "payload", fmt.Sprintf("%x", frame.Payload))
			info, loadErr := s.users.AccountSnapshot(ctx, userID)
			if loadErr != nil {
				s.logBackendFallback("load balances after invalid item upgrade", userID, loadErr)
			}
			return session.Write(ReplyUpgradeItem, ItemUpgradeReplyPayload(info, ItemUpgradeRequest{}, false))
		}
		success, err := s.users.SetItemUpgrade(ctx, userID, int(request.ItemID), request.Slot, request.UpgradeID)
		if err != nil {
			s.logBackendFallback("upgrade inventory item", userID, err)
			success = false
		}
		s.logger.Info("item upgrade processed", "user_id", userID, "item_id", request.ItemID, "slot", request.Slot, "upgrade_id", request.UpgradeID, "success", success)
		info, err := s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load balances after item upgrade", userID, err)
		}
		return session.Write(ReplyUpgradeItem, ItemUpgradeReplyPayload(info, request, success))
	case RequestDowngradeItem:
		request, err := ParseItemUpgradeRequest(frame.Payload, true)
		if err != nil {
			s.logger.Warn("parse item downgrade failed", "error", err)
			return session.Write(ReplyDowngradeItem, ItemDowngradeReplyPayload(ItemUpgradeRequest{}, false))
		}
		_, _, userID, _ := session.identity()
		success, err := s.users.SetItemUpgrade(ctx, userID, int(request.ItemID), request.Slot, 0)
		if err != nil {
			s.logBackendFallback("downgrade inventory item", userID, err)
			success = false
		}
		s.logger.Info("item downgrade processed", "user_id", userID, "item_id", request.ItemID, "slot", request.Slot, "success", success)
		return session.Write(ReplyDowngradeItem, ItemDowngradeReplyPayload(request, success))
	case RequestEquipTitle:
		request, err := ParseEquipTitleRequest(frame.Payload)
		if err != nil {
			return session.Write(ReplyEquipTitle, InventoryStatusPayload(0))
		}
		_, _, userID, _ := session.identity()
		if err := s.users.SetEquippedTitle(ctx, userID, request.Slot, request.Title); err != nil {
			s.logBackendFallback("set equipped title", userID, err)
		}
		return session.Write(ReplyEquipTitle, InventoryStatusPayload(0))
	case RequestDeletedItems:
		_, _, userID, _ := session.identity()
		var itemIDs []int
		if expirations, ok := s.users.(store.ExpiredInventoryStore); ok {
			loaded, loadErr := expirations.TakeExpiredItems(ctx, userID, 8)
			if loadErr != nil {
				s.logBackendFallback("load expired inventory notifications", userID, loadErr)
				itemIDs = nil
			} else {
				itemIDs = loaded
			}
		}
		if len(itemIDs) == 0 {
			// The 2012 client (sub_341F9750) opens the "Süre Dolumu" (item
			// expired) message box for every 0x1419, even an empty one. With
			// nothing expired there is nothing to report, so no reply is sent.
			return nil
		}
		payload, err := DeletedItemsPayload(itemIDs)
		if err != nil {
			return err
		}
		return session.Write(ReplyDeletedItems, payload)
	case RequestUseItem:
		return s.handleGenericUseItem(ctx, session, frame.Payload)
	case RequestDailyUseMoney:
		// sub_34229A60: u32 used Cash (Premium Shop MyCurrentUsedCash,
		// sub_342C4B50), then 8 skipped bytes.
		_, _, userID, _ := session.identity()
		used := uint32(0)
		if shop, ok := s.premiumShop(ctx, userID); ok {
			used = shop.Used
		}
		return session.Write(ReplyDailyUseMoney, DailyUseMoneyPayload(used))
	case RequestUseFullUpItem:
		return s.handleFullUpItem(ctx, session, frame.Payload)
	case RequestUseCountItem:
		return s.handleCountedItemUse(ctx, session, frame.Payload)
	case RequestThrowItem:
		uniqueID, itemID, err := inventoryItemFromRequest(frame.Payload)
		if err != nil {
			return session.Write(ReplyThrowItem, InventoryStatusPayload(1))
		}
		_, _, userID, _ := session.identity()
		removed, err := s.users.RemoveInventoryItem(ctx, userID, itemID)
		if err != nil {
			s.logBackendFallback(fmt.Sprintf("delete item %d", itemID), userID, err)
			removed = false
		}
		if removed {
			session.invalidateRosterState()
		}
		s.logger.Info("inventory item delete", "user_id", userID, "unique", uniqueID, "item", itemID, "removed", removed)
		status := byte(1)
		if removed {
			status = 0
		}
		if err := session.Write(ReplyThrowItem, InventoryStatusPayload(status)); err != nil || !removed {
			return err
		}
		s.awardCrestEvent(ctx, session, crestdata.GroupItemThrow)
		return nil
	case RequestUseBigHitBox:
		return s.handleUseBigHitBox(ctx, session, frame.Payload)
	case RequestGetBigHitBox:
		return s.handleTakeLuckyBox(ctx, session)
	case RequestEquip:
		_, _, userID, _ := session.identity()
		if err := ApplyEquipment(ctx, s.users, userID, frame.Payload); err != nil {
			s.logBackendFallback("save equipment", userID, err)
		}
		session.invalidateRosterState()
		if err := session.Write(ReplyEquip, InventoryStatusPayload(0)); err != nil {
			return err
		}
		// The match is peer to peer: every client (the owner included) takes a
		// player's power items (tattoos, ...) and character parts from the room
		// member record (0x1607 ROOMCHARS, power region + character lists).
		// Equipping inside a room must therefore resend the records, as the
		// Pride and Power Master refreshes already do; otherwise the match runs
		// with the equipment the player had when entering the room.
		if room := s.sessionRoom(session); room != nil && !room.Snapshot().GameStarted {
			if err := s.broadcastRoster(ctx, room); err != nil {
				s.logger.Warn("broadcast equipment roster refresh", "room", room.Number, "error", err)
			}
		}
		return nil
	case RequestChangeItem:
		payload, err := ChangeInventoryPayload(frame.Payload)
		if err != nil {
			return err
		}
		return session.Write(ReplyChangeItem, payload)
	case RequestCheckBuyNowOpen:
		// Sent by the "ŞİMDİ SATIN AL" buttons (sub_34182F90, empty body).
		return session.Write(ReplyCheckBuyNowOpen, CheckBuyNowOpenPayload(true))
	case RequestBuyNowList:
		items, err := s.users.DailyShopItems(ctx, time.Now())
		if err != nil {
			_, _, userID, _ := session.identity()
			s.logBackendFallback("load daily shop", userID, err)
			items = [2]uint16{10038, 10087}
		}
		return session.Write(ReplyBuyNowList, BuyNowListPayload(items))
	case RequestBuyBasket:
		_, _, userID, _ := session.identity()
		// The 2012 client (sub_341C3F80) always stores the trailing Gold/WC/Cash,
		// so failed baskets carry the current balances instead of zeros.
		failed := func(request BasketPurchaseRequest) error {
			result := store.BasketPurchaseResult{}
			if info, infoErr := s.users.AccountSnapshot(ctx, userID); infoErr == nil {
				result.Gold, result.WC, result.Cash = info.Gold, info.WC, info.Cash
			}
			return session.Write(ReplyBuyBasket, BasketPurchaseReplyPayload(request, result))
		}
		request, err := ParseBasketPurchaseRequest(frame.Payload)
		if err != nil {
			return failed(BasketPurchaseRequest{})
		}
		purchases := make([]store.PurchaseOrder, 0, len(request.Items))
		for _, item := range request.Items {
			currency := 0
			days, quantity := item.Days, 1
			var components []store.PurchaseComponent
			if s.shopCatalog != nil {
				option, validationErr := s.shopCatalog.ValidatePurchase(
					item.ItemID, item.Days, item.PurchaseMode, item.Price, item.WolfCoinReward,
				)
				if validationErr != nil {
					s.logger.Warn("shop basket rejected", "user_id", userID, "item", item.ItemID,
						"days", item.Days, "purchase_mode", item.PurchaseMode, "price", item.Price,
						"wolf_coin_reward", item.WolfCoinReward, "error", validationErr)
					return failed(request)
				}
				currency = option.Currency
				days, quantity = purchaseGrant(option)
				components = purchaseComponents(option.Components)
			} else {
				currency, _, err = BuyRequestPayment(item)
				if err != nil {
					return failed(request)
				}
			}
			itemType := 0
			if item.ItemID >= 30000 {
				itemType = 1
			}
			purchases = append(purchases, store.PurchaseOrder{
				UserID: userID, ItemID: item.ItemID, ItemType: itemType,
				Days: days, Quantity: quantity, Currency: currency, Price: item.Price, Components: components,
			})
		}
		var before []store.Item
		for _, purchase := range purchases {
			if len(purchase.Components) != 0 {
				if before, err = s.users.Items(ctx, userID); err != nil {
					s.logBackendFallback("load inventory before basket purchase", userID, err)
					before = nil
				}
				break
			}
		}
		result, err := s.users.PurchaseBasket(ctx, purchases)
		if err != nil {
			s.logBackendFallback("purchase basket", userID, err)
			return failed(request)
		}
		if result.Success {
			session.invalidateRosterState()
		}
		if err := session.Write(ReplyBuyBasket, BasketPurchaseReplyPayload(request, result)); err != nil {
			return err
		}
		if result.Success {
			for _, purchase := range purchases {
				if len(purchase.Components) != 0 {
					bought := make(map[int]bool, len(purchases))
					for _, purchase := range purchases {
						bought[purchase.ItemID] = true
					}
					if err := s.pushInventory(ctx, session, userID, before, bought); err != nil {
						return err
					}
					break
				}
			}
			spent := map[int]int{}
			for _, purchase := range purchases {
				spent[purchase.Currency] += int(purchase.Price)
			}
			for _, currency := range []int{1, 4, 2} {
				s.recordVIPSpend(ctx, session, currency, spent[currency])
			}
			for _, item := range request.Items {
				s.grantEventBonus(ctx, session, userID, item.ItemID, item.Days)
			}
		}
		return nil
	case RequestForgeItemList:
		category := byte(0)
		if len(frame.Payload) > 0 {
			category = frame.Payload[0]
		}
		recipes, err := s.users.ForgeRecipes(ctx, category)
		if err != nil {
			_, _, userID, _ := session.identity()
			s.logBackendFallback("load forge recipes", userID, err)
			// 2012 result 97: "item list could not be obtained" (string 9457).
			return session.Write(ReplyForgeItemList, []byte{forgeResultFailed})
		}
		if len(recipes) == 0 {
			// 2012 result 99: "EDS Kapalı" (forge closed, string 9458).
			return session.Write(ReplyForgeItemList, []byte{forgeResultClosed})
		}
		payload, err := ForgeItemListPayload(category, recipes)
		if err != nil {
			return err
		}
		return session.Write(ReplyForgeItemList, payload)
	case RequestForgeChallenge:
		request, err := ParseForgeChallengeRequest(frame.Payload)
		if err != nil {
			return session.Write(ReplyForgeChallenge, []byte{forgeResultFailed})
		}
		_, _, userID, _ := session.identity()
		attempt := store.ForgeAttempt{
			UserID: userID, Category: request.Category, Round: request.Challenge, Action: request.Action,
		}
		validState := true
		if request.Action == 0 {
			attempt.RewardItem, attempt.RewardDays, validState = session.pendingForge(request.Category, request.Challenge)
		} else if request.Challenge == 0 {
			session.clearPendingForge()
		} else {
			_, _, validState = session.pendingForge(request.Category, request.Challenge-1)
		}
		if !validState {
			session.clearPendingForge()
			return session.Write(ReplyForgeChallenge, []byte{forgeResultFailed})
		}
		result, err := s.users.RunForgeChallenge(ctx, attempt)
		if err != nil {
			s.logBackendFallback("run forge challenge", userID, err)
			session.clearPendingForge()
			return session.Write(ReplyForgeChallenge, []byte{forgeResultFailed})
		}
		if result.Status != 0 {
			// 98 = not enough currency, 97 = generic failure (result-only).
			session.clearPendingForge()
			return session.Write(ReplyForgeChallenge, []byte{result.Status})
		}
		{
			switch result.Outcome {
			case 1:
				session.setPendingForge(request.Category, request.Challenge, result.RewardItem, result.RewardDays)
			case 0, 2:
				session.clearPendingForge()
			}
			if validState {
				session.invalidateRosterState()
			}
		}
		return session.Write(ReplyForgeChallenge, ForgeChallengePayload(request, result))
	case RequestForgeWinners:
		category := byte(0)
		if len(frame.Payload) > 0 {
			category = frame.Payload[0]
		}
		winners, err := s.users.ForgeWinners(ctx, category)
		if err != nil {
			_, _, userID, _ := session.identity()
			s.logBackendFallback("load forge winners", userID, err)
			winners = nil
		}
		payload, err := ForgeWinnersPayload(category, winners)
		if err != nil {
			return err
		}
		return session.Write(ReplyForgeWinners, payload)
	case RequestVIPRankList, RequestVIPTicketCount, RequestVIPItemList, RequestVIPBuy:
		return s.handleVIPLounge(ctx, session, frame)
	case RequestItemPriceInfo:
		return session.Write(ReplyItemPriceInfo, ItemPriceInfoPayload(s.shopCatalog.WPackPriceOverrides()))
	case RequestItemPriceVer:
		// 2013 TR: the client asks for the price version once at login; the W-Pack
		// list (0x146F) is handled by the same lobby dispatcher (sub_3415FFE0), so
		// it follows here. The client never sends CS_CN_ENTER_REQ (0x1506).
		messages := []transport.OutboundMessage{{MessageID: ReplyItemPriceVer, Payload: ItemPriceVersionPayload(
			ItemPriceOverrideVersions(s.shopCatalog.WPackPriceOverrides()))}}
		if packs := s.wpackListIDs(); len(packs) != 0 {
			messages = append(messages, transport.OutboundMessage{MessageID: ReplyWPackList, Payload: WPackListPayload(packs)})
		}
		return session.WriteMessages(messages)
	case RequestTutorialGive:
		// 2012: sent by the tutorial result screen (sub_341E08E0) after a
		// chapter was cleared. The chapter is the next one of the room's track
		// (the Battle Room only unlocks chapters in order, sub_342526F0).
		var granted []store.Item
		advanced := false
		_, _, userID, _ := session.identity()
		if room := s.sessionRoom(session); room != nil {
			if track := tutorialTrack(byte(room.Snapshot().Config.Mode)); track != 0 {
				if progress, err := s.users.TutorialProgress(ctx, userID); err != nil {
					s.logBackendFallback("load tutorial progress", userID, err)
				} else {
					current := progress.HumanLevel
					if track == 2 {
						current = progress.WolfLevel
					}
					if current < tutorialMaxLevel(track) {
						items, after, claimErr := s.users.ClaimTutorialReward(ctx, userID, track, current+1)
						if claimErr != nil {
							s.logBackendFallback("claim tutorial reward", userID, claimErr)
						} else {
							granted = items
							advanced = after != progress
							s.logger.Info("tutorial chapter cleared", "user_id", userID, "track", track,
								"level", current+1, "items", len(items))
						}
					}
				}
			}
		}
		if len(granted) > tutorialMaxGiveItems {
			granted = granted[:tutorialMaxGiveItems]
		}
		if len(granted) > 0 {
			session.invalidateRosterState()
		}
		payload, err := TutorialRewardGivePayload(granted)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyTutorialGive, payload); err != nil {
			return err
		}
		// With reward items the client re-requests 0x1344 itself; without
		// them it does not, so push the new progress directly.
		if advanced && len(granted) == 0 {
			return s.writeTutorialProgress(ctx, session)
		}
		return nil
	case RequestEventGold:
		return session.Write(ReplyEventGold, EventGoldPayload())
	case RequestPopup:
		return session.Write(ReplyPopup, GamePopupPayload())
	case RequestAdvertAll:
		s.broadcastChannelRosterNow()
		return session.Write(ReplyAdvertAll, AdvertAllPayload())
	case RequestChannelEnter:
		return s.handleChannelEnter(session, frame.Payload)
	case RequestInviteUserList:
		payload, err := InviteUserListPayload(s.inviteUsers(session))
		if err != nil {
			return err
		}
		return session.Write(ReplyInviteUserList, payload)
	case RequestInviteSelect:
		request, err := ParseInviteSelectRequest(frame.Payload)
		if err != nil {
			return err
		}
		return s.handleInviteSelect(session, request)
	case RequestInviteSend:
		return s.handleInviteSend(session)
	case RequestChangeSheet:
		return s.handleChangeChannelSheet(session, frame.Payload)
	case RequestRefundItem:
		// CS_GI_REFUND_REQ (sub_342084E0): u32 gift ID, sent by the gift box
		// convert button (0x2711) after the 9459/9460 confirmation.
		if len(frame.Payload) < 4 {
			return session.Write(ReplyRefundItem, GiftRefundReplyPayload(0, 0))
		}
		giftID := binary.LittleEndian.Uint32(frame.Payload[:4])
		_, _, userID, _ := session.identity()
		refunds, ok := s.users.(store.GiftRefundStore)
		if !ok {
			return session.Write(ReplyRefundItem, GiftRefundReplyPayload(0, 0))
		}
		result, err := refunds.RefundGift(ctx, userID, giftID, s.giftRefundValue)
		if err != nil {
			s.logBackendFallback(fmt.Sprintf("convert gift %d", giftID), userID, err)
			result = store.GiftRefundResult{}
		}
		if err := session.Write(ReplyRefundItem, GiftRefundReplyPayload(result.WC, result.Gold)); err != nil {
			return err
		}
		if !result.Success {
			return nil
		}
		s.logger.Info("gift converted", "user_id", userID, "gift", giftID, "wc", result.WC, "gold", result.Gold)
		// The ACK carries no balances; 0x141D is handled by the manager
		// (sub_34136C10 -> sub_34206D40) in every section.
		info, err := s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load balances after gift conversion", userID, err)
			return nil
		}
		return session.Write(ReplyGetGoldCash, CurrencyPayload(info))
	case RequestPermanentInfo:
		ticketUnique, ticketItemID, err := ItemIDFromUniquePayload(frame.Payload)
		if err != nil {
			payload, _ := PermanentTicketInfoPayload(0, store.PermanentTicketInfo{})
			return session.Write(ReplyPermanentInfo, payload)
		}
		_, _, userID, _ := session.identity()
		info, err := s.users.PermanentItemTicketInfo(ctx, userID, ticketItemID)
		if err != nil {
			s.logBackendFallback("load permanent item ticket info", userID, err)
			info = store.PermanentTicketInfo{}
		}
		session.mu.Lock()
		if info.Valid {
			session.PendingPermanentTicket = ticketItemID
		} else {
			session.PendingPermanentTicket = 0
		}
		session.mu.Unlock()
		payload, err := PermanentTicketInfoPayload(ticketUnique, info)
		if err != nil {
			return err
		}
		return session.Write(ReplyPermanentInfo, payload)
	case RequestPermanentUse:
		targetUnique, targetItemID, err := ItemIDFromUniquePayload(frame.Payload)
		if err != nil || len(frame.Payload) < 5 || frame.Payload[4] > 1 {
			return session.Write(ReplyPermanentUse, PermanentTicketUsePayload(0, 0, 0, false))
		}
		_, _, userID, _ := session.identity()
		session.mu.Lock()
		ticketItemID := session.PendingPermanentTicket
		session.PendingPermanentTicket = 0
		session.mu.Unlock()
		ticketUnique := uint32(0)
		if ticketItemID > 0 {
			ticketUnique = 2_000_000_000 + uint32(ticketItemID)
		}
		success, err := s.users.UsePermanentItemTicket(ctx, userID, ticketItemID, targetItemID)
		if err != nil {
			s.logBackendFallback("use permanent item ticket", userID, err)
			success = false
		}
		if success {
			session.invalidateRosterState()
		}
		return session.Write(ReplyPermanentUse, PermanentTicketUsePayload(ticketUnique, targetUnique, uint16(targetItemID), success))
	case RequestFieldList:
		// Treat a field-list request as authoritative lobby state so a room
		// cannot retain a ghost member. Remember the departed slot so a later
		// Return Lobby action
		// can replay it through a section that does.
		if room := s.sessionRoom(session); room != nil {
			slot, found := room.MemberSlot(session)
			if err := s.leaveRoom(session, true); err != nil {
				return err
			}
			if found {
				session.deferClientRoomExit(slot)
			}
		}
		rooms := s.roomSnapshots()
		payload, err := FieldListPayload(rooms)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyFieldList, payload); err != nil {
			return err
		}
		_, _, _, level := session.identity()
		joinAllow, err := JoinAllowPayload(rooms, level)
		if err != nil {
			return err
		}
		return session.Write(ReplyJoinAllow, joinAllow)
	case RequestCreateRoom:
		config, err := ParseRoomConfigForChannel(frame.Payload, session.channelType())
		if err != nil {
			return err
		}
		if session.channelType() == store.ChannelTypePrideBattle {
			config = prideBattleFixedRules(config)
		}
		room, err := s.allocateRoom(config)
		if err != nil {
			return err
		}
		_, _, userID, _ := session.identity()
		room.SetPowerMaster(s.hasActiveItem(ctx, userID, powerMasterItemID))
		room.SetSideSwitchChannel(session.channelType() == store.ChannelTypeSpat || session.channelType() == store.ChannelTypePrideBattle)
		if session.channelType() == store.ChannelTypeLadder {
			if search, searching := s.ladderSearchOf(session); searching {
				room.SetLadderSearch(session, search)
			}
		}
		if _, ok := room.Add(session); !ok {
			return fmt.Errorf("new room rejected its creator")
		}
		// Pride Battle rooms are named after the creator's Pride ("A", then
		// "A vs B" once another Pride joins, syncPrideBattleTitle).
		if session.channelType() == store.ChannelTypePrideBattle {
			if pride, _ := session.prideDisplay(); strings.TrimSpace(pride) != "" {
				config.Title = strings.TrimSpace(pride)
				room.SetTitle(config.Title)
			}
		}
		s.refreshSessionLobby(session)
		s.removePrivateLadderSearch(session)
		if err := session.Write(ReplyCreateRoom, CreateRoomPayload(room.Number, config)); err != nil {
			return err
		}
		rosterMessages, err := s.roomRosterMessages(ctx, room)
		if err != nil {
			return err
		}
		if err := session.WriteMessages(rosterMessages); err != nil {
			return err
		}
		if room.Snapshot().PowerMaster {
			if err := s.sendPowerRoomItems(session, 1); err != nil {
				return err
			}
		}
		s.broadcastChannelRoster()
		s.broadcastFieldCreate(room)
		s.broadcastJoinAllow()
		return s.sendFieldList(session)
	case RequestEnterRoom:
		return s.handleEnterRoom(ctx, session, frame.Payload)
	case RequestQuickEnter:
		return s.handleQuickEnter(ctx, session)
	case RequestChangeTeam:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		// Result 184 (-72) is stringtable 4184 "you cannot change team while
		// ready" and 181 (-75) is 4181 for a member in the match (manager
		// error handler sub_34231D40). The client sends the request whatever
		// its state (sub_3424D1B0 case 10).
		if status := room.TeamChangeRefusal(session); status != 0 {
			return session.Write(ReplyChangeTeam, ChangeTeamFailurePayload(status))
		}
		slot, team, teamSlot, ok := room.MoveToOtherTeam(session)
		if !ok {
			return session.Write(ReplyChangeTeam, ChangeTeamFailurePayload(182))
		}
		room.Broadcast(ReplyChangeTeam, ChangeTeamPayload(slot, team, teamSlot))
		return nil
	case RequestAddAISlot:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		column := byte(1)
		if len(frame.Payload) > 0 {
			column = frame.Payload[0]
		}
		difficulty := byte(1)
		if len(frame.Payload) > 2 && frame.Payload[2] >= 1 && frame.Payload[2] <= 3 {
			difficulty = frame.Payload[2]
		}
		// 2013 TR ADD_AISLOT_REQ (sub_34189A00) sends 4 bytes again (2012 sent
		// 3); the fourth selects the wolf form as in the 2013 reference.
		wolf := len(frame.Payload) > 3 && frame.Payload[3] == 2
		requestedRow := -1
		if len(frame.Payload) > 1 {
			requestedRow = int(frame.Payload[1])
		}
		team := byte(0)
		switch column {
		case 6:
			team = 2
		case 5:
			team = 1
		default:
			return nil
		}
		bot := room.ProvisionBot(team, difficulty, wolf, s.botPeerPortBase, requestedRow)
		if bot == nil {
			return nil
		}
		botPayload, ok := botAISlotPayload(bot)
		if !ok {
			return nil
		}
		room.Broadcast(ReplyAddAISlot, botPayload)
		return nil
	case RequestDeleteAISlot:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 1 || !room.IsMaster(session) {
			return nil
		}
		slot := frame.Payload[0]
		if room.RemoveBot(slot) {
			room.Broadcast(ReplyDeleteAISlot, DeleteAISlotPayload(slot))
		}
		return nil
	case RequestChangeRule:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		payload, config, err := ChangeRulePayload(frame.Payload)
		if err != nil {
			return err
		}
		if session.channelType() == store.ChannelTypePrideBattle {
			config = prideBattleFixedRules(config)
			payload = ChangeRuleConfigPayload(config)
		}
		oldConfig := room.Snapshot().Config
		config.ClassLimitMin = oldConfig.ClassLimitMin
		config.ClassLimitMax = oldConfig.ClassLimitMax
		config.classLimitsSet = oldConfig.classLimitsSet
		room.Broadcast(ReplyChangeRule, payload)
		room.UpdateConfig(config)
		s.logger.Info("room config updated", "room", room.Number, "gameFlags", config.GameFlags, "ruleSetting", config.RuleSetting, "mapSetting", config.MapSetting, "weaponFlags", config.WeaponFlags, "wins", config.Wins, "time", config.Time)
		if config.Title != oldConfig.Title {
			fieldName, buildErr := PBFieldNamePayload(config.Title)
			if buildErr != nil {
				return buildErr
			}
			room.Broadcast(ReplyPBFieldName, fieldName)
		}
		s.broadcastFieldState(room)
		s.broadcastJoinAllow()
		return nil
	case RequestStartGame:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		if _, ok := room.SetStatus(session, 1); !ok {
			return nil
		}
		team1, ready1, team2, ready2 := room.TeamCounts()
		if team1 > 0 && team2 > 0 && (ready1 < 1 || ready2 < 1) {
			if err := session.Write(ReplyStartGame, StartGamePayload(0xab)); err != nil {
				return err
			}
			_, _ = room.SetStatus(session, 0)
			return nil
		}
		changed, _, _ := room.BeginMatch()
		for _, member := range room.Snapshot().Members {
			s.clearLadderSearch(member)
		}
		for _, slot := range changed {
			room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(slot, 3))
		}
		// 0x1619 is an order to load the field: the room section arms a 3 s
		// countdown and changes section whatever the receiver's own status
		// (0x34250C90 case 0x1619 -> sub_34249E70). Only the slots the match
		// actually took may receive it. Everyone else in the room switches
		// the button to Start from the master's 0x1617 above (sub_3424AA30
		// sets room +7 = 2 for the master's slot).
		started := StartGamePayload(0)
		for _, slot := range changed {
			member := room.SessionAt(slot)
			if member == nil || member.IsBot {
				continue
			}
			if err := member.Write(ReplyStartGame, started); err != nil {
				s.logger.Debug("write start-game order", "room", room.Number, "slot", slot, "error", err)
			}
		}
		s.broadcastGameState(room, true)
		return nil
	case RequestStartRound:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		snap := room.Snapshot()
		slot, round, seconds := byte(0xff), byte(0), uint16(0)
		wasRoundStarted := snap.RoundStarted
		isRoundBased := snap.IsRoundBased()

		session.mu.RLock()
		playerSlot := byte(session.RoomSlot)
		session.mu.RUnlock()

		targetStatus := byte(4)
		if isRoundBased && wasRoundStarted && int(playerSlot) < len(snap.LateJoined) && snap.LateJoined[playerSlot] {
			// Late joiners in round-based modes (Ice Hold, Destruction, Wolf Hunt) become spectators until next round
			targetStatus = 3
		}

		var ok bool
		slot, ok = room.SetStatus(session, targetStatus)
		if !ok {
			return nil
		}
		if targetStatus == activeRoomStatus {
			// He plays this round, so the next round boundary must not
			// announce him again (a second "joined" line).
			room.ClearLateJoined(slot)
		}
		round, seconds, _ = room.BeginRound(time.Now())
		room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(slot, targetStatus))

		special, selected := room.PrepareSpecialRound()
		if selected {
			s.awardWolfKingCrests(ctx, room, special)
		}
		stateSnapshot := room.Snapshot()
		roundPlayers := roundPlayerStates(room.MatchPlayers())
		if special.WolfHunt {
			s.logger.Debug("WolfHunt round state", "room", room.Number, "round", round, "teams", special.Players, "kings", special.Kings, "wolves", special.Wolves)
		}

		if special.WolfHunt || special.Hero {
			if err := s.sendSpecialRound(session, room, special); err != nil {
				return err
			}
		}
		if err := session.Write(ReplyStartRound, StartRoundPayload(round, seconds, stateSnapshot.RedScore, stateSnapshot.BlueScore, roundPlayers)); err != nil {
			return err
		}
		for _, botSlot := range room.PromoteBots() {
			room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(botSlot, 3))
		}
		return nil
	case RequestPlayerDead:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 8 {
			return nil
		}
		killer := frame.Payload[0]
		victimSlot := frame.Payload[1]
		victim := room.SessionAt(victimSlot)
		if victim == nil {
			return nil
		}
		killerSession := room.SessionAt(killer)
		result := room.RecordKill(killerSession, victim)
		if !result.Accepted {
			return nil
		}
		// Byte [3] is the headshot flag (sub_342E0D70 copies kill record +9,
		// the death-message bit the client's crest hook reports as group 0).
		if frame.Payload[3] != 0 && killerSession != nil && killerSession != victim && !room.UsesAIDeathmatchRules() {
			s.awardCrestEvent(ctx, killerSession, crestdata.GroupHeadshot)
		}
		room.Broadcast(ReplyPlayerDead, PlayerDeadPayload(killer, victimSlot, result.RedScore, result.BlueScore, result.FinishRound))
		if result.WolfHuntConverted {
			// Apply the live team/role transition before team sorting to keep the
			// roster and scoreboard synchronized.
			room.Broadcast(ReplyChangeCreatureTeam, ChangeCreatureTeamPayload(victimSlot, result.WolfHuntTeam))
			s.logger.Debug("WolfHunt conversion", "room", room.Number, "killer", killer, "victim", victimSlot, "team", result.WolfHuntTeam, "teams", result.SpecialRound.Players)
		}
		if result.WolfHuntLifeChanged {
			life, err := NoticeRemainLifePayload([]RemainLifeNotice{result.WolfHuntLife})
			if err == nil {
				room.Broadcast(ReplyNoticeRemainLife, life)
			}
		}
		// Score-gap selection occurs on death; the selection message announces a
		// newly promoted player and the remaining-life message applies the role.
		if !result.FinishRound {
			if life, selected, ok := room.RecordHeroDeath(victim); ok {
				if selected {
					selection, err := HeroSelectPayload(life.Team, []byte{life.Slot})
					if err == nil {
						room.Broadcast(ReplyHeroSelect, selection)
					}
				}
				// Refresh only the numeric Hero counters. Full option records
				// Avoid rebuilding the victim during death; the remaining-life
				// update owns the role and revival gate.
				heroState, _ := room.PrepareSpecialRound()
				if options, err := buildHeroCountPayload(heroState); err == nil {
					room.Broadcast(ReplyHeroOptionState, options)
				}
				room.Broadcast(ReplyHeroRemainLife, HeroRemainLifePayload(
					life.Slot, life.Team, life.Active, life.Lives, life.Kind, 0,
				))
			}
		}
		if result.FinishRound {
			s.finishRound(room, result.WinTeam, result.EndGame)
		}
		if killerSession != nil && killerSession != victim {
			_, _, killerID, _ := killerSession.identity()
			_, _, deadID, _ := victim.identity()
			if !room.UsesAIDeathmatchRules() && killerID > 0 && deadID > 0 {
				if err := s.users.RecordKill(ctx, killerID, deadID); err != nil {
					s.logBackendFallback("register kill", killerID, err)
				}
			}
		}
		return nil
	case RequestRevival:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 2 {
			return nil
		}
		// The payload carries the reviver/source slot at index 0 and the dead
		// target's room slot at index 1.
		slot := frame.Payload[1]
		if room.SessionAt(slot) != session {
			return nil
		}
		team, teamSlot, ok := room.ReviveSlot(slot)
		if !ok {
			return nil
		}
		// An Ice Hold late joiner sleeps on the spectator status; the revival
		// puts him into the running round, so every client (his own included)
		// has to cache him as active.
		if room.PromoteSpectator(slot) {
			room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(slot, activeRoomStatus))
		}
		special, _ := room.PrepareSpecialRound()
		if special.WolfHunt {
			s.logger.Debug("WolfHunt revival", "room", room.Number, "slot", slot, "team", team, "team_row", teamSlot, "kings", special.Kings, "wolves", special.Wolves)
		}
		room.Broadcast(ReplyRevival, RevivalPayload(slot, team, teamSlot, frame.Payload[0]))
		return nil
	case RequestPlantBomb:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 1 {
			return nil
		}
		if room.RecordBombPlant(session, frame.Payload[0], time.Now()) {
			room.Broadcast(ReplyPlantBomb, PlantBombPayload(bombFuseSeconds))
			if !room.UsesAIDeathmatchRules() {
				s.awardCrestEvent(ctx, session, crestdata.GroupBombPlant)
			}
		}
		return nil
	case RequestRemoveBomb:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 2 {
			return nil
		}
		winTeam, endGame, ok := room.ResolveBombRemoval(session, frame.Payload[0], frame.Payload[1], time.Now())
		if !ok {
			return nil
		}
		room.Broadcast(ReplyRemoveBomb, RemoveBombPayload())
		s.finishRound(room, winTeam, endGame)
		return nil
	case RequestLastPlayerInfo:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		redSlot, red, blueSlot, blue, ok := room.LastPlayers()
		if !ok {
			return nil
		}
		redRate := s.lastPlayerRate(ctx, red)
		blueRate := s.lastPlayerRate(ctx, blue)
		return session.Write(ReplyLastPlayerInfo, LastPlayerInfoPayload(redSlot, blueSlot, redRate, blueRate))
	case RequestGamingKick:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 2 {
			return nil
		}
		// 2013 TR GAMINGKICK_ACK (sub_342F7770): initiator, target, reason.
		initiator, ok := room.StartKickVote(session, frame.Payload[0], frame.Payload[1], time.Now())
		if ok {
			room.Broadcast(ReplyGamingKick, GamingKickPayload(initiator, frame.Payload[0], frame.Payload[1]))
			// The initiator's client requires a Kick Vote ticket (10039) to send
			// 0x1666 (sub_342DE5B0) and removes one unit on this ACK.
			if units, ok := s.users.(store.ItemUnitStore); ok {
				_, _, userID, _ := session.identity()
				if used, err := units.ConsumeItemUnit(ctx, userID, kickVoteItemID); err != nil {
					s.logBackendFallback("consume kick vote ticket", userID, err)
				} else if used {
					session.invalidateRosterState()
				}
			}
		}
		return nil
	case RequestGamingKickVote:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 2 {
			return nil
		}
		status, accepted := room.CastKickVote(session, frame.Payload[0], frame.Payload[1], time.Now())
		if accepted {
			room.Broadcast(ReplyGamingKickVote, GamingKickVotePayload(status))
		}
		if status.Resolved {
			s.completeGamingKickVote(room, status)
		}
		return nil
	case RequestWeaponKill:
		if s.sessionRoom(session) == nil {
			return nil
		}
		return session.Write(ReplyWeaponKill, WeaponKillPayload())
	case RequestDestroyTower:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 1 {
			return nil
		}
		redScore, blueScore, ok := room.RecordTowerDestroyed(session, frame.Payload[0])
		if ok {
			room.Broadcast(ReplyDestroyTower, DestroyTowerPayload(redScore, blueScore))
		}
		return nil
	case RequestLadderBossOut:
		room := s.sessionRoom(session)
		if room == nil || !room.IsMaster(session) {
			return nil
		}
		room.EnablePrivateLadder()
		room.Broadcast(ReplyLadderBossOut, LadderBossOutPayload())
		return nil
	case RequestTutorialReward:
		// 2012: sent when the Battle Room screen opens (sub_34252CC0) to show
		// every chapter's reward; it does not claim anything.
		_, _, userID, _ := session.identity()
		var chapters [tutorialChapterCount][]store.TutorialReward
		for index := range chapters {
			track, level := tutorialChapter(index)
			rewards, err := s.users.TutorialRewards(ctx, track, level)
			if err != nil {
				s.logBackendFallback("load tutorial rewards", userID, err)
				continue
			}
			chapters[index] = rewards
		}
		payload, err := TutorialRewardListPayload(chapters)
		if err != nil {
			return err
		}
		return session.Write(ReplyTutorialReward, payload)
	case RequestFlagPointGet:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 2 {
			return nil
		}
		personal, team, total, reachedTarget, ok := room.RecordFlagPoint(session, frame.Payload[0], frame.Payload[1])
		_, _, reporterID, _ := session.identity()
		s.logger.Info("flag point", "room", room.Number, "user_id", reporterID, "slot", frame.Payload[0],
			"event", frame.Payload[1], "accepted", ok, "team", team, "personal", personal, "total", total)
		if ok {
			room.Broadcast(ReplyFlagPointGet, FlagPointGetPayload(frame.Payload[0], personal, team, total))
			if reachedTarget {
				s.finishRound(room, team, true)
			}
		}
		return nil
	case RequestCrestSink:
		// Bodyless; the client's crest hook asks in and out of rooms.
		_, _, userID, _ := session.identity()
		payload, err := s.crestSinkPayload(ctx, userID)
		if err != nil {
			return err
		}
		return session.Write(ReplyCrestSink, payload)
	case RequestGameRecord:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		record, err := ParseGameRecordRequest(frame.Payload)
		if err != nil || !room.ValidateGameRecord(session, record) {
			return nil
		}
		s.logger.Debug("game record",
			"killer_slot", record.Killer,
			"killer_kind", record.KillerKind,
			"victim_slot", record.Victim,
			"victim_kind", record.VictimKind,
			"text", record.Text)
		return nil
	case RequestEndRound, RequestEndGame:
		// These are lifecycle notifications. Round outcome and result generation
		// stay server-authoritative, so untrusted reason bytes
		// never choose a winner or trigger a second reward.
		return nil
	case RequestChangeFlag:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		request, err := ParseChangeFlagRequest(frame.Payload)
		if err != nil {
			return nil
		}
		room.Broadcast(ReplyChangeFlag, ChangeFlagPayload())
		if endRound, endGame := room.UpdateFlagControl(request.Flag, request.Owner); endRound {
			s.finishRound(room, request.Owner, endGame)
		}
		return nil
	case RequestGamePoints:
		if s.sessionRoom(session) == nil {
			return nil
		}
		_, _, userID, _ := session.identity()
		info, err := s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load user points", userID, err)
			info = store.AccountSnapshot{}
		}
		// Mid-match: no bonus/winner yet; keep the own team so the client's
		// own-slot team field (+94) is not cleared.
		session.mu.RLock()
		team := session.RoomTeam
		session.mu.RUnlock()
		return session.Write(ReplyGamePoints, GamePointsPayload(info, GamePointSlot{Team: team}))
	case RequestExitRoom:
		return s.leaveRoom(session, true)
	case RequestKick:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) < 1 || !room.IsMaster(session) {
			return nil
		}
		target := room.SessionAt(frame.Payload[0])
		if target == nil || target == session || target.IsBot {
			return nil
		}
		if err := session.Write(ReplyKick, KickPayload()); err != nil {
			return err
		}
		return s.leaveRoom(target, true)
	case RequestUDPStart:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		session.mu.RLock()
		slot := byte(session.RoomSlot)
		session.mu.RUnlock()
		room.Broadcast(ReplyUDPStart, UDPStartPayload(slot))
		return nil
	case RequestUDPReady:
		room := s.sessionRoom(session)
		if room == nil || len(frame.Payload) == 0 {
			return nil
		}
		ready := frame.Payload[0]
		slot, ok := room.SetStatus(session, ready)
		if ok {
			room.Broadcast(ReplyUDPReady, ReadyPayload(slot, ready))
		}
		return nil
	case RequestEnterGame:
		room := s.sessionRoom(session)
		if room == nil {
			return nil
		}
		session.mu.Lock()
		slot := byte(session.RoomSlot)
		previousStatus := session.RoomStatus
		session.RoomStatus = spectatorRoomStatus
		session.mu.Unlock()
		if !inFieldStatus(previousStatus) {
			// A field client adds the scoreboard row and "joined" line for a
			// remote slot only from 0x1617 while that slot's cached status is
			// not yet 3 (sub_342E1B60). ENTERGAME (sub_342E3B10) writes the same
			// status and creates the slot without the row, so it has to come
			// second.
			room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(slot, spectatorRoomStatus))
			// Joining a round that is already running: sit it out (and in Ice
			// Hold arrive as one of the downed) until the next round.
			if room.Snapshot().IsRoundBased() {
				room.MarkLateJoined(slot)
			}
		}
		// 2012 ENTERGAME_ACK (sub_341CB490): slot, slot status, current round.
		room.Broadcast(ReplyEnterGame, EnterGamePayload(slot, spectatorRoomStatus, room.Snapshot().Round))
		return nil
	case RequestInitField:
		if err := session.Write(ReplyInitField, InitFieldPayload()); err != nil {
			return err
		}
		// The room screen sends INITFIELD every time it opens (sub_3424F2A0),
		// also when a member comes back from the field inventory or shop. A
		// member who was there when the match started never saw the master's
		// 0x1617 - those sections only store the slot status (sub_341831D0) -
		// so his room state is still "waiting" and the button reads Ready.
		// The master's slot status is what the room section turns into room
		// +7 (sub_3424AA30), so repeat it to him.
		if room := s.sessionRoom(session); room != nil && room.Snapshot().GameStarted && !room.InField(session) {
			if masterSlot, masterStatus, ok := room.MasterFieldStatus(); ok {
				return session.Write(ReplyChangeSlot, ChangeSlotPayload(masterSlot, masterStatus))
			}
		}
		return nil
	case RequestTunneling:
		return session.Write(ReplyTunneling, TunnelingPayload(true))
	case RequestCharSave:
		if err := s.saveCharacterSlots(ctx, session, frame.Payload); err != nil {
			_, _, userID, _ := session.identity()
			s.logBackendFallback("save character slots", userID, err)
		}
		session.invalidateRosterState()
		if err := session.Write(ReplyCharSave, InventoryStatusPayload(0)); err != nil {
			return err
		}
		// Character and parts changes made inside a room reach the match only
		// through the room member records (see RequestEquip).
		if room := s.sessionRoom(session); room != nil && !room.Snapshot().GameStarted {
			if err := s.broadcastRoster(ctx, room); err != nil {
				s.logger.Warn("broadcast character roster refresh", "room", room.Number, "error", err)
			}
		}
		return nil
	case RequestDefaultChar:
		return s.handleDefaultCharacter(ctx, session, frame.Payload)
	default:
		s.logger.Debug("unimplemented game message", "message_id", fmt.Sprintf("0x%04x", frame.MessageID))
		return nil
	}
}

func (s *Server) finishRound(room *Room, winTeam byte, endGame bool) {
	players := room.MatchPlayers()
	playedSeconds := room.MatchDurationSeconds(time.Now())
	roundSeconds := room.Snapshot().Config.Time
	round, redScore, blueScore, ok := room.FinishRound(endGame)
	if !ok {
		return
	}
	room.Broadcast(ReplyEndRound, EndRoundPayload(round, winTeam, redScore, blueScore))
	if ladderPlayers, enabled := room.LadderRoundPlayers(); enabled {
		payload, err := LadderRoundEndPayload(ladderPlayers)
		if err != nil {
			s.logger.Warn("build private-ladder round-end payload", "room", room.Number, "error", err)
		} else {
			room.Broadcast(ReplyLadderRoundEnd, payload)
		}
	}
	time.AfterFunc(s.roundEndDelay, func() {
		promoted := room.CompleteRoundDelay(endGame, time.Now())
		if endGame {
			s.sendEndGame(room, winTeam, players, playedSeconds)
			s.broadcastGameState(room, false)
			return
		}
		if switched, redScore, blueScore := room.CheckAndTriggerSwitchTeam(); switched {
			s.logger.Info("destruction halftime side switch triggered", "room", room.Number, "redScore", redScore, "blueScore", blueScore)
			// The side change goes out with the restart, after the round-end
			// result and scoreboard. 0x1659 comes first: its handler closes the
			// round HUD (sub_3423CD60) that the 0x1625 round entry brings back.
			// A gap between them showed the swap and the round entry as two
			// separate transitions.
			room.Broadcast(ReplySwitchTeam, SwitchTeamPayload(true))
		}
		s.restartRound(room, round, roundSeconds, promoted...)
	})
}

func (s *Server) restartRound(room *Room, round byte, roundSeconds uint16, promotedSlots ...byte) {
	// Broadcast ChangeSlot(slot, 4) ONLY for players who were spectators and are now joining active play
	for _, slot := range promotedSlots {
		room.Broadcast(ReplyChangeSlot, ChangeSlotPayload(slot, 4))
	}
	special, selected := room.PrepareSpecialRound()
	if selected {
		s.awardWolfKingCrests(context.Background(), room, special)
	}
	if special.WolfHunt || special.Hero {
		s.broadcastSpecialRound(room, special)
	}
	room.Broadcast(ReplyRestartRound, RestartRoundPayload(round, roundSeconds, roundPlayerStates(room.MatchPlayers())))
}

func roundPlayerStates(players []MatchPlayer) []RoundPlayerState {
	states := make([]RoundPlayerState, 0, len(players))
	for _, player := range players {
		states = append(states, RoundPlayerState{Slot: player.Slot, Team: player.Team})
	}
	return states
}

func roundActivePlayerStates(players []MatchPlayer, lateJoined [roomSlotCount]bool, isRoundBased, wasRoundStarted bool) []RoundPlayerState {
	states := make([]RoundPlayerState, 0, len(players))
	for _, player := range players {
		if player.Session != nil {
			if isRoundBased && wasRoundStarted && int(player.Slot) < len(lateJoined) && lateJoined[player.Slot] {
				continue
			}
		}
		states = append(states, RoundPlayerState{Slot: player.Slot, Team: player.Team})
	}
	return states
}

func (s *Server) sendSpecialRound(session *Session, room *Room, state SpecialRoundState) error {
	if state.WolfHunt {
		if err := s.sendWolfHuntRoundState(session, room, state); err != nil {
			return err
		}
		// Preserve the dedicated King flag during initialization.
	}
	if state.Hero {
		return s.sendHeroRoundState(session, room, state)
	}
	return nil
}

func (s *Server) sendHeroRoundState(session *Session, room *Room, state SpecialRoundState) error {
	modeState, _ := HeroModeStatePayload(nil)
	heroSlotsByTeam := heroSlotsByTeam(state)
	options, err := buildHeroOptionStatePayload(state)
	if err != nil {
		return fmt.Errorf("build hero-option payload for room %d: %w", room.Number, err)
	}
	life, err := NoticeRemainLifePayload(state.Life)
	if err != nil {
		return fmt.Errorf("build hero-life payload for room %d: %w", room.Number, err)
	}
	for _, message := range []struct {
		id      uint16
		payload []byte
	}{
		{ReplyHeroModeState, modeState},
		{ReplyHeroOptionState, options},
		{ReplyNoticeRemainLife, life},
	} {
		if err := session.Write(message.id, message.payload); err != nil {
			return err
		}
	}
	for _, team := range []byte{redTeam, blueTeam} {
		if len(heroSlotsByTeam[team]) == 0 {
			continue
		}
		selection, err := HeroSelectPayload(team, heroSlotsByTeam[team])
		if err != nil {
			return fmt.Errorf("build hero-select payload for room %d: %w", room.Number, err)
		}
		if err := session.Write(ReplyHeroSelect, selection); err != nil {
			return err
		}
	}
	return nil
}

func heroSlotsByTeam(state SpecialRoundState) map[byte][]byte {
	slots := map[byte][]byte{redTeam: {}, blueTeam: {}}
	for _, hero := range state.Heroes {
		if hero.Team != redTeam && hero.Team != blueTeam {
			continue
		}
		slots[hero.Team] = append(slots[hero.Team], hero.Slot)
	}
	return slots
}

func buildHeroOptionStatePayload(state SpecialRoundState) ([]byte, error) {
	slots := heroSlotsByTeam(state)
	// These values are the numeric Hero counts for the two teams. Individual
	// roles are carried separately by state.Options.
	return HeroOptionStatePayload(
		byte(len(slots[redTeam])),
		byte(len(slots[blueTeam])),
		state.Options,
	)
}

func buildHeroCountPayload(state SpecialRoundState) ([]byte, error) {
	slots := heroSlotsByTeam(state)
	return HeroOptionStatePayload(
		byte(len(slots[redTeam])),
		byte(len(slots[blueTeam])),
		nil,
	)
}

// sendWolfHuntRoundState applies roles before scoreboard rows.
func (s *Server) sendWolfHuntRoundState(session *Session, room *Room, state SpecialRoundState) error {
	roles, err := WolfKingPayload(state.Kings, state.Wolves)
	if err != nil {
		return fmt.Errorf("build wolf-role payload for room %d: %w", room.Number, err)
	}
	teamSort, err := WolfHuntTeamSortPayload(state.Players)
	if err != nil {
		return fmt.Errorf("build wolf-hunt team-sort payload for room %d: %w", room.Number, err)
	}
	if err := session.Write(ReplyWolfKing, roles); err != nil {
		return err
	}
	if err := session.Write(ReplyWolfHuntTeamSort, teamSort); err != nil {
		return err
	}
	life, err := NoticeRemainLifePayload(state.Life)
	if err != nil {
		return fmt.Errorf("build wolf-hunt life payload for room %d: %w", room.Number, err)
	}
	return session.Write(ReplyNoticeRemainLife, life)
}

func (s *Server) broadcastSpecialRound(room *Room, state SpecialRoundState) {
	for _, session := range room.Snapshot().Members {
		if err := s.sendSpecialRound(session, room, state); err != nil {
			s.logger.Warn("send special-round state", "room", room.Number, "error", err)
		}
	}
}

func (s *Server) sendEndGame(room *Room, winTeam byte, players []MatchPlayer, playedSeconds uint32) {
	records := make([]EndGamePlayer, len(players))
	winnerUserIDs := make([]int, 0, len(players))
	loserUserIDs := make([]int, 0, len(players))
	prideMatchUpdated := false
	// Ladder points: private-ladder rooms, and every match played in a
	// Ladder channel (type 3 rooms are created by the searching client, so
	// they never carry the private-ladder flag).
	privateLadderMatch := room.IsPrivateLadder() || isLadderChannelMatch(players)
	aiDeathMatch := room.UsesAIDeathmatchRules()
	weaponFlags := room.Snapshot().Config.WeaponFlags
	awardCtx, cancelAwards := context.WithTimeout(context.Background(), 5*time.Second)
	burning := s.channelBurningBonus(awardCtx)
	for index, player := range players {
		outcome := matchOutcome(player.Team, winTeam)
		record := EndGamePlayer{
			Slot:   player.Slot,
			Team:   player.Team,
			Winner: outcome == store.MatchWin,
		}
		if !player.Session.IsBot {
			_, _, userID, _ := player.Session.identity()
			reward := s.calculateMatchReward(awardCtx, player, record.Winner, userID, burning)
			totalGold := addUint32(reward.gold, reward.bonusGold)
			totalGP := addUint32(reward.gp, reward.bonusGP)
			err := s.users.CreditMatchRewards(awardCtx, userID, int(totalGold), int(totalGP))
			if err != nil {
				s.logBackendFallback("add match points", userID, err)
			} else {
				record.GoldReward = reward.gold
				record.GPReward = reward.gp
				record.BonusGold = reward.bonusGold
				record.BonusGP = reward.bonusGP
				record.BonusFlags = reward.bonusFlags
			}
			if !aiDeathMatch {
				var resultErr error
				if outcomes, ok := s.users.(store.MatchOutcomeStore); ok {
					resultErr = outcomes.RegisterMatchOutcome(awardCtx, userID, outcome)
				} else if outcome != store.MatchDraw {
					resultErr = s.users.RegisterMatchResult(awardCtx, userID, outcome == store.MatchWin)
				}
				if resultErr != nil {
					s.logBackendFallback("register match result", userID, resultErr)
				}
				if outcome == store.MatchWin {
					winnerUserIDs = append(winnerUserIDs, userID)
				} else if outcome == store.MatchLoss {
					loserUserIDs = append(loserUserIDs, userID)
				}
			}
		}
		records[index] = record
	}
	if recorder, ok := s.users.(store.PrideBattleRecorder); ok && isPrideBattleMatch(players) && len(winnerUserIDs) > 0 && len(loserUserIDs) > 0 {
		if err := recorder.RecordPrideBattle(awardCtx, prideBattleRecord(room, winTeam, players, winnerUserIDs, loserUserIDs)); err != nil {
			s.logger.Warn("record pride battle", "error", err)
		} else {
			prideMatchUpdated = true
		}
	} else if prides, ok := s.users.(store.PrideStore); ok && isPrideBattleMatch(players) && len(winnerUserIDs) > 0 && len(loserUserIDs) > 0 {
		if err := prides.RegisterPrideMatch(awardCtx, winnerUserIDs, loserUserIDs); err != nil {
			s.logger.Warn("register pride match", "error", err)
		} else {
			prideMatchUpdated = true
		}
	}
	if ladder, ok := s.users.(store.PrivateLadderStore); ok && privateLadderMatch && len(winnerUserIDs) > 0 && len(loserUserIDs) > 0 {
		if err := ladder.RegisterPrivateLadderMatch(awardCtx, winnerUserIDs, loserUserIDs); err != nil {
			s.logger.Warn("register private-ladder match", "error", err)
		}
	}
	cancelAwards()

	loadCtx, cancelLoads := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLoads()
	for index, player := range players {
		if player.Session.IsBot {
			continue
		}
		_, callsign, userID, level := player.Session.identity()
		playTimeSeconds := player.Session.addPlayTime(playedSeconds)
		if playTimes, ok := s.users.(store.PlayTimeStore); ok {
			total, err := playTimes.AddPlayTime(loadCtx, userID, playedSeconds)
			if err != nil {
				s.logBackendFallback("add play time", userID, err)
			} else {
				playTimeSeconds = total
				player.Session.setPlayTime(total)
			}
		}
		// P.T. bonus: the day's play time pays PTBonusGold.dat tiers.
		ptReward, ptGold := 0, 0
		if daily, ok := s.users.(store.DailyPlayStore); ok {
			result, err := daily.AddDailyPlayTime(loadCtx, userID, playedSeconds, func(paid int, seconds uint32) (int, int) {
				return ptbonus.Reward(s.ptTiers, paid, seconds)
			})
			if err != nil {
				s.logBackendFallback("add daily play time", userID, err)
			} else {
				playTimeSeconds = result.DailySeconds
				player.Session.setPlayTime(result.DailySeconds)
				ptReward, ptGold = result.RewardGold, result.Gold
			}
		}
		info := store.AccountSnapshot{Callsign: callsign, Level: int(level)}
		loaded, err := s.users.AccountSnapshot(loadCtx, userID)
		if err != nil {
			s.logBackendFallback("load end-game user", userID, err)
		} else {
			info = loaded
		}
		// The match result shows the match Gold only; the P.T. bonus has its
		// own notice (0x16A7) below.
		info.Gold = max(info.Gold-ptReward, 0)
		player.Session.mu.Lock()
		oldLevel := player.Session.UserLevel
		player.Session.UserLevel = byte(info.Level)
		player.Session.mu.Unlock()
		if byte(info.Level) != oldLevel {
			for _, recipient := range players {
				_ = recipient.Session.Write(ReplyChangeTotalGrade, ChangeTotalGradePayload(player.Slot, byte(info.Level)))
			}
		}
		own := GamePointSlot{
			BonusGold: records[index].BonusGold, BonusGP: records[index].BonusGP,
			BonusFlags: records[index].BonusFlags, Winner: records[index].Winner, Team: records[index].Team,
		}
		if err := player.Session.Write(ReplyGamePoints, GamePointsPayload(info, own)); err != nil {
			s.logger.Debug("write final game points", "user_id", userID, "error", err)
			continue
		}
		currentGP := nonNegativeU32(info.GP)
		awardedGP := addUint32(records[index].GPReward, records[index].BonusGP)
		previousGP := uint32(0)
		if currentGP >= awardedGP {
			previousGP = currentGP - awardedGP
		}
		payload, err := EndGamePayload(EndGameResult{
			Info:            info,
			PreviousGP:      previousGP,
			PlayTimeSeconds: playTimeSeconds,
			WeaponFlags:     weaponFlags,
			Players:         records,
		})
		if err != nil {
			s.logger.Warn("build end-game payload", "user_id", userID, "error", err)
			continue
		}
		if err := player.Session.Write(ReplyEndGame, payload); err != nil {
			s.logger.Debug("write end-game result", "user_id", userID, "error", err)
		}
		// CS_FD_REWARDPLAYTIME_ACK (sub_34177A30): Gold balance and the P.T.
		// bonus paid, shown with string 1366.
		if ptReward > 0 {
			s.logger.Info("P.T. bonus paid", "user_id", userID, "gold", ptReward, "daily_seconds", playTimeSeconds)
			_ = player.Session.Write(ReplyRewardPlaytime, RewardPlaytimePayload(uint32(max(ptGold, 0)), uint32(ptReward)))
		}
	}
	if prideMatchUpdated {
		for _, player := range players {
			if !player.Session.IsBot {
				_ = player.Session.Write(ReplyPrideBattleList, PrideBattleListPayload(nil, 0))
			}
		}
	}
}

func matchOutcome(team, winTeam byte) store.MatchOutcome {
	if winTeam != redTeam && winTeam != blueTeam {
		return store.MatchDraw
	}
	if team == winTeam {
		return store.MatchWin
	}
	return store.MatchLoss
}

func (s *Server) runRoomProcessor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.processRooms(now)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) processRooms(now time.Time) {
	s.mu.RLock()
	rooms := make([]*Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	s.mu.RUnlock()
	for _, room := range rooms {
		if status, expired := room.ExpireKickVote(now); expired {
			s.completeGamingKickVote(room, status)
		}
		if winTeam, endGame, triggered := room.RoundTimeout(now); triggered {
			s.finishRound(room, winTeam, endGame)
			continue
		}
		if winTeam, endGame, triggered := room.BombTimeout(now); triggered {
			// The remove-bomb reply is the synchronization barrier before the
			// end-round reply.
			room.Broadcast(ReplyRemoveBomb, RemoveBombPayload())
			s.finishRound(room, winTeam, endGame)
		}
	}
}

func (s *Server) lastPlayerRate(ctx context.Context, session *Session) byte {
	if session == nil || session.IsBot || s.users == nil {
		return 0
	}
	_, _, userID, _ := session.identity()
	if userID <= 0 {
		return 0
	}
	info, err := s.users.AccountSnapshot(ctx, userID)
	if err != nil {
		s.logBackendFallback("load last-player rate", userID, err)
		return 0
	}
	return playerWinRate(info.Wins, info.Losses)
}

func (s *Server) completeGamingKickVote(room *Room, status KickVoteStatus) {
	room.Broadcast(ReplyGamingKickResult, GamingKickResultPayload(status))
	if !status.Passed || status.TargetSession == nil {
		return
	}
	room.Broadcast(ReplyGamingUserKick, GamingUserKickPayload(status.Target))
	if err := s.leaveRoom(status.TargetSession, true); err != nil {
		s.logger.Debug("remove successful kick-vote target", "slot", status.Target, "error", err)
	}
}

func (s *Server) handleLogin(ctx context.Context, session *Session, payload []byte) error {
	credentials, err := ParseLoginCredentials(payload)
	if err != nil {
		return err
	}
	userID, loginErr := s.users.Login(ctx, credentials.Username, credentials.Password)
	status := byte(0)
	info := store.AccountSnapshot{}
	channelType := store.ChannelTypeNormal
	var superseded []*Session
	if loginErr != nil || userID <= 0 {
		status = 23
		userID = 0
		if loginErr != nil {
			s.logger.Warn("user authentication backend failed", "username", credentials.Username, "error", loginErr)
		}
	} else {
		info, err = s.users.AccountSnapshot(ctx, userID)
		if err != nil {
			s.logBackendFallback("load authenticated user", userID, err)
			status = 23
			userID = 0
			info = store.AccountSnapshot{}
		} else if refusal := s.prideChannelRefusal(ctx, userID); refusal != 0 {
			// Result-only failure: the server-list section shows the refusal
			// and returns to the list without reading a body.
			s.logger.Info("channel login refused", "username", credentials.Username, "user_id", userID, "status", refusal)
			return session.Write(ReplyChannelLogin, []byte{refusal})
		} else {
			superseded, err = s.setSessionAuthenticated(
				session, credentials.Username, userID, info.Callsign, byte(info.Level), info.Authority,
			)
			if err != nil {
				s.logger.Warn("channel login rejected", "username", credentials.Username, "error", err)
				status = 23
				userID = 0
				info = store.AccountSnapshot{}
			} else {
				session.setPlayTime(info.PlayTimeSeconds)
				if powerUsers, ok := s.users.(store.PowerUserStore); ok {
					if color, label, err := powerUsers.PowerUserSettings(ctx, userID); err == nil {
						session.setPowerUserSettings(color, label)
					}
				}
				session.setMutedUntil(info.Muted, info.MuteExpiresAt)
				channelType = s.configuredChannelType(ctx)
				session.setChannelType(channelType)
				session.setLoginOrder(s.loginOrder.Add(1))
				s.refreshSessionPrideDisplay(ctx, session)
			}
		}
	}
	defer s.disconnectSupersededSessions(superseded)
	payload, err = LoginReplyPayload(status, session.SessionID(), uint32(userID), channelType, info)
	if err != nil {
		return err
	}
	s.logger.Info("channel login processed", "username", credentials.Username, "user_id", userID, "status", status)
	if err := session.Write(ReplyChannelLogin, payload); err != nil {
		return err
	}
	if status == 0 {
		if s.buddyPort != 0 {
			buddyPayload, buildErr := s.buddyServerEndpointPayload()
			if buildErr != nil {
				return buildErr
			}
			if writeErr := session.Write(ReplyBuddyServerEndpoint, buddyPayload); writeErr != nil {
				return writeErr
			}
		}
		if s.rankingWebPort != 0 {
			rankingPayload, buildErr := RankingWebEndpointPayload(s.advertisedIP, s.rankingWebPort)
			if buildErr != nil {
				return buildErr
			}
			if writeErr := session.Write(ReplyRankingWebEndpoint, rankingPayload); writeErr != nil {
				return writeErr
			}
		}
		return s.sendPendingRecommendations(ctx, session, userID)
	}
	return nil
}

// prideChannelNoPrideStatus is the CS_CH_LOGIN_ACK result for an account
// without a Pride entering a Pride Battle channel. The server-list section's
// result handler (2013 sub_3408CA10 after sub_3415FA70, which handles only 97-107/-9 (2011 sub_341CDBE0, 2012 sub_34162920)) shows stringtable 4037 "Bu sunucuya girmek için bir
// Klan'a kayıtlı olmak durumundasınız." for result 38, with the button that
// drops the connection and refreshes the server list.
const prideChannelNoPrideStatus byte = 38

// prideChannelRefusal returns the login result that keeps an account out of
// this channel, or 0. Only Pride Battle channels are restricted: the account
// must be a member of a Pride. A backend failure refuses with the generic
// login failure (23) rather than letting the account in unchecked.
func (s *Server) prideChannelRefusal(ctx context.Context, userID int) byte {
	if s.configuredChannelType(ctx) != store.ChannelTypePrideBattle {
		return 0
	}
	prides, ok := s.users.(store.PrideStore)
	if !ok {
		return 0
	}
	_, _, found, err := prides.PrideForUser(ctx, userID)
	if err != nil {
		s.logBackendFallback("check pride for pride channel login", userID, err)
		return 23
	}
	if !found {
		return prideChannelNoPrideStatus
	}
	return 0
}

func isPrideBattleMatch(players []MatchPlayer) bool {
	foundHuman := false
	for _, player := range players {
		if player.Session == nil || player.Session.IsBot {
			continue
		}
		foundHuman = true
		switch player.Session.channelType() {
		case store.ChannelTypePrideBattle, store.ChannelTypeLadder, store.ChannelTypePrivateLadder:
		default:
			return false
		}

	}
	return foundHuman
}

// refreshSessionPrideDisplay keeps the cache used by room/channel roster
// packets in sync with the authoritative membership row. PrideMarkID is
// intentionally zero: the current schema and PrideForUser contract expose no
// crest/mark value that could be sent safely.
func (s *Server) refreshSessionPrideDisplay(ctx context.Context, session *Session) {
	if session == nil || session.IsBot {
		return
	}
	_, _, userID, _ := session.identity()
	name := ""
	markID := uint32(0)
	if prides, ok := s.users.(store.PrideStore); ok && userID > 0 {
		pride, _, found, err := prides.PrideForUser(ctx, userID)
		if err != nil {
			s.logBackendFallback("load pride display", userID, err)
		} else if found {
			name = pride.Name
			markID = pride.ID
		}
	}
	session.setPrideDisplay(name, markID)
}

// refreshPrideDisplaysAndRosters applies a membership change to live sessions
// and immediately rebuilds the affected channel/room rosters. The maps avoid
// duplicate work when the actor and target are the same live connection.
func (s *Server) refreshPrideDisplaysAndRosters(ctx context.Context, sessions ...*Session) {
	seen := make(map[*Session]struct{}, len(sessions))
	rooms := make(map[*Room]struct{})
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if _, duplicate := seen[session]; duplicate {
			continue
		}
		seen[session] = struct{}{}
		s.refreshSessionPrideDisplay(ctx, session)
		if room := s.sessionRoom(session); room != nil {
			rooms[room] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return
	}
	s.broadcastChannelRosterNow()
	for room := range rooms {
		rosterCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if err := s.broadcastRoster(rosterCtx, room); err != nil && s.logger != nil {
			s.logger.Warn("broadcast pride roster refresh", "room", room.Number, "error", err)
		}
		cancel()
	}
}

func (s *Server) logBackendFallback(operation string, userID int, err error) {
	if err == nil {
		return
	}
	s.logger.Warn("user backend operation failed; sending protocol fallback",
		"operation", operation, "user_id", userID, "error", err)
}

func (s *Server) hasActiveItem(ctx context.Context, userID, itemID int) bool {
	items, ok := s.users.(store.ActiveItemStore)
	if !ok {
		return false
	}
	active, err := items.HasActiveItem(ctx, userID, itemID)
	if err != nil {
		s.logBackendFallback(fmt.Sprintf("load active item %d", itemID), userID, err)
		return false
	}
	return active
}

func (s *Server) sendPowerRoomItems(session *Session, mode byte) error {
	payload, err := PowerMasterItemsPayload(mode, powerRoomItems[:])
	if err != nil {
		return err
	}
	return session.Write(ReplyPowerMasterItems, payload)
}

func (s *Server) saveCharacterSlots(ctx context.Context, session *Session, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	count := min(int(payload[0]), (len(payload)-1)/69)
	if count == 0 {
		return nil
	}
	_, _, userID, _ := session.identity()
	items, err := s.users.Items(ctx, userID)
	if err != nil {
		return fmt.Errorf("load characters for save: %w", err)
	}
	ownedList := characterIDs(items)
	owned := make(map[int]struct{}, len(ownedList))
	for _, characterID := range ownedList {
		owned[characterID] = struct{}{}
	}
	saved, err := s.users.CharacterSlots(ctx, userID)
	if err != nil {
		return fmt.Errorf("load character slots for save: %w", err)
	}
	selector := canonicalCharacterSlots(saved, ownedList)
	type slotUpdate struct {
		slot        int
		characterID int
		equipment   store.CharacterEquipment
	}
	updates := make([]slotUpdate, 0, count)
	used := make(map[int]struct{})
	for index := 0; index < count; index++ {
		offset := 1 + index*69
		slot := int(payload[offset])
		if slot < 1 || slot > 10 {
			continue
		}
		uniqueID := binary.LittleEndian.Uint32(payload[offset+1 : offset+5])
		if uniqueID < 2_000_000_000 || uniqueID > 2_147_483_647 {
			continue
		}
		characterID := int(uniqueID - 2_000_000_000)
		if _, ok := owned[characterID]; !ok {
			continue
		}
		if _, duplicate := used[characterID]; duplicate {
			continue
		}
		var equipment store.CharacterEquipment
		for equipmentSlot := range equipment {
			equipmentOffset := offset + 5 + equipmentSlot*4
			equipment[equipmentSlot] = int(int32(binary.LittleEndian.Uint32(payload[equipmentOffset : equipmentOffset+4])))
		}
		updates = append(updates, slotUpdate{slot: slot, characterID: characterID, equipment: equipment})
		used[characterID] = struct{}{}
	}
	if len(updates) == 0 {
		return nil
	}
	// Character-save messages contain only changed character-card equipment.
	// Merge those records into the persisted selector; the default-character
	// message owns the equipped red/blue pair.
	for _, update := range updates {
		for slot, characterID := range selector {
			if characterID == update.characterID || slot == update.slot {
				delete(selector, slot)
			}
		}
	}
	for _, update := range updates {
		selector[update.slot] = update.characterID
	}
	selector = canonicalCharacterSlots(selector, ownedList)
	if err := s.users.ReplaceCharacterSlots(ctx, userID, selector); err != nil {
		return fmt.Errorf("replace character slots: %w", err)
	}
	if equipmentStore, ok := s.users.(store.CharacterEquipmentStore); ok {
		for _, update := range updates {
			if err := equipmentStore.SetCharacterEquipment(ctx, userID, update.characterID, update.equipment); err != nil {
				return fmt.Errorf("save character %d equipment: %w", update.characterID, err)
			}
		}
	}
	session.setCharacterSelector(selector)
	session.invalidateRosterState()
	return nil
}

func (s *Server) handleCharacterInfo(ctx context.Context, session *Session) error {
	_, _, userID, _ := session.identity()
	items, err := s.users.Items(ctx, userID)
	if err != nil {
		s.logBackendFallback("load characters", userID, err)
		items = nil
	}
	ownedList := characterIDs(items)
	owned := characterOwnership(items)
	slots, err := s.users.CharacterSlots(ctx, userID)
	if err != nil {
		s.logBackendFallback("load character slots", userID, err)
		slots = make(map[int]int)
	}
	canonical := canonicalCharacterSlots(slots, ownedList)
	if len(canonical) >= 2 && !sameCharacterSlots(canonical, slots) {
		if err := s.users.ReplaceCharacterSlots(ctx, userID, canonical); err != nil {
			s.logBackendFallback("repair character slots", userID, err)
		} else {
			slots = canonical
		}
	}
	loadout, err := s.users.CharacterSelection(ctx, userID)
	if err != nil {
		s.logBackendFallback("load character loadout", userID, err)
		loadout = store.CharacterSelection{}
	}
	red := resolveCharacter(2, loadout, owned)
	blue := resolveCharacter(1, loadout, owned)
	characterEquipment := make(map[int]store.CharacterEquipment)
	if equipmentStore, ok := s.users.(store.CharacterEquipmentStore); ok {
		characterEquipment, err = equipmentStore.CharacterEquipment(ctx, userID)
		if err != nil {
			s.logBackendFallback("load character equipment", userID, err)
			characterEquipment = make(map[int]store.CharacterEquipment)
		}
	}
	session.expectInitialDefaultCharacter()
	return session.Write(ReplyCharInfo, CharacterInfoPayload(red, blue, slots, characterEquipment))
}

func (s *Server) handleDefaultCharacter(ctx context.Context, session *Session, payload []byte) error {
	changed := false
	if len(payload) >= 2 {
		_, _, userID, _ := session.identity()
		loadout, err := s.users.CharacterSelection(ctx, userID)
		if err != nil {
			s.logBackendFallback("load character loadout", userID, err)
			loadout = store.CharacterSelection{}
		}
		slots := session.characterSelector()
		if len(slots) == 0 {
			slots, err = s.users.CharacterSlots(ctx, userID)
			if err != nil {
				s.logBackendFallback("load character selector", userID, err)
				slots = make(map[int]int)
			}
		}
		selected, first, second := resolveCharacterSelection(slots, int(payload[0]), int(payload[1]), loadout)
		initialEcho := automaticBasicEcho(session.consumeInitialDefaultCharacter(), first, second)
		if validCharacterPair(first, second) && !initialEcho {
			if err := s.users.SaveCharacterSelection(ctx, userID, selected); err != nil {
				s.logBackendFallback("save selected characters", userID, err)
			} else {
				session.invalidateRosterState()
				changed = true
			}
		}
	}
	if err := session.Write(ReplyDefaultChar, InventoryStatusPayload(0)); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	room := s.sessionRoom(session)
	if room == nil {
		return nil
	}
	// A roster snapshot initializes a room and rejects occupied slots. An enter
	// update can replace the existing member without taking the new-join path.
	member := s.enterMember(session, s.loadRosterState(ctx, session, true))
	update, err := EnterRoomPayload(0, member)
	if err != nil {
		return err
	}
	room.Broadcast(ReplyEnterRoom, update)
	return nil
}

func inventoryItemFromRequest(payload []byte) (uniqueID, itemID int, err error) {
	if len(payload) < 4 {
		return 0, 0, fmt.Errorf("inventory action payload is too short")
	}
	rawUnique := binary.LittleEndian.Uint32(payload[:4])
	uniqueID = int(int32(rawUnique))
	if uniqueID >= 2_000_000_000 {
		itemID = uniqueID - 2_000_000_000
	}
	if itemID <= 0 {
		return uniqueID, 0, fmt.Errorf("inventory unique %d has no item ID", uniqueID)
	}
	return uniqueID, itemID, nil
}

func (s *Server) handleGenericUseItem(ctx context.Context, session *Session, payload []byte) error {
	request, err := ParseGenericUseItemRequest(payload)
	if err != nil {
		return session.Write(ReplyUseItem, GenericUseItemReplyPayload(1, GenericUseItemRequest{}, 0, store.AccountSnapshot{}))
	}
	_, _, userID, _ := session.identity()
	used := false
	countedRemaining := -1
	packageItem := request.ItemID >= 14132 && request.ItemID <= 14137 || request.ItemID == 14032
	effect, exItem := exItemEffect(request)
	exItems, exStore := s.users.(store.ExItemStore)
	if exItem && exStore {
		used, err = exItems.UseExItem(ctx, userID, request.ItemID, effect)
		s.logger.Info("EX item use", "user_id", userID, "item", request.ItemID, "type", effect.Type, "used", used, "error", err)
		if used && effect.NewCallsign != "" {
			s.setSessionCallsign(session, effect.NewCallsign)
		}
	} else if packageItem {
		used, err = s.users.ClaimPackageContents(ctx, userID, request.ItemID)
	} else {
		used, err = s.users.ConsumeInventoryItem(ctx, userID, request.ItemID)
		if err == nil && !used && request.UniqueID == 2_000_000_000+request.ItemID {
			// Medkits, injections, and other equipped quantity items use the
			// ordinary USEITEM route at the moment they are activated. The
			// batch USECNTITEM route is not the only consumer notification.
			if countedItems, ok := s.users.(store.CountedItemStore); ok {
				var results []store.CountedItemUse
				results, err = countedItems.ConsumeCountedItems(ctx, userID, []int{request.UniqueID})
				if err == nil && len(results) == 1 && results[0].UniqueID == request.UniqueID {
					used = true
					countedRemaining = results[0].Remaining
				}
			}
		}
	}
	if err != nil {
		s.logBackendFallback(fmt.Sprintf("use item %d", request.ItemID), userID, err)
		used = false
	}
	if used {
		session.invalidateRosterState()
	}

	items, itemsErr := s.users.Items(ctx, userID)
	if itemsErr != nil {
		s.logBackendFallback("reload inventory after item use", userID, itemsErr)
		items = nil
	}
	info, infoErr := s.users.AccountSnapshot(ctx, userID)
	if infoErr != nil {
		s.logBackendFallback("reload account after item use", userID, infoErr)
	}
	remaining := 0
	for _, item := range items {
		if item.ID == request.ItemID {
			quantity := item.Quantity
			if quantity < 1 {
				quantity = 1
			}
			remaining += quantity
		}
	}
	if countedRemaining >= 0 {
		remaining = countedRemaining
	}
	status := byte(1)
	if used && infoErr == nil {
		status = 0
	}
	if err := session.Write(ReplyUseItem, GenericUseItemReplyPayload(status, request, remaining, info)); err != nil {
		return err
	}
	if !used || !packageItem {
		return nil
	}
	inventory, buildErr := ItemListPayload(items)
	if buildErr != nil {
		return buildErr
	}
	return session.Write(ReplyItemList, inventory)
}

func (s *Server) handleUseBigHitBox(ctx context.Context, session *Session, payload []byte) error {
	uniqueID, itemID, err := inventoryItemFromRequest(payload)
	if err != nil {
		return session.Write(ReplyUseBigHitBox, InventoryStatusPayload(1))
	}
	_, _, userID, _ := session.identity()
	result, err := s.users.DrawLuckyBox(ctx, userID, itemID, uniqueID)
	if err != nil {
		s.logBackendFallback("open lucky box", userID, err)
		return session.Write(ReplyUseBigHitBox, InventoryStatusPayload(1))
	}
	if result.Success {
		session.invalidateRosterState()
		session.setPendingBox(uniqueID, result.RewardItem, result.Days, result.Remaining)
		if err := session.Write(ReplyUseBigHitBox, LuckyBoxPreviewPayload(uniqueID, result.RewardItem, result.Days)); err != nil {
			return err
		}
		s.awardCrestEvent(ctx, session, crestdata.GroupLuckyBox)
		return nil
	}
	return session.Write(ReplyUseBigHitBox, InventoryStatusPayload(1))
}

func (s *Server) handleTakeLuckyBox(ctx context.Context, session *Session) error {
	uniqueID, reward, days, remaining, ok := session.takePendingBox()
	if !ok {
		return session.Write(ReplyGetBigHitBox, InventoryStatusPayload(1))
	}
	return session.Write(ReplyGetBigHitBox, LuckyBoxTakePayload(uniqueID, remaining, reward, days))
}

func (s *Server) addSessionLocked(session *Session) {
	if _, exists := s.sessions[session]; exists {
		return
	}
	s.sessions[session] = struct{}{}
	s.online.add(session)
	if err := s.syncChannelMemberLocked(session); err != nil && s.logger != nil {
		s.logger.Warn("assign channel member", "error", err)
	}
}

func (s *Server) addSession(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addSessionLocked(session)
}

func (s *Server) addConnectionSession(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.nextSessionID
	if candidate == 0 {
		candidate = 1
	}
	for checked := 0; checked < 0xffff; checked++ {
		set := s.online.bySessionID[candidate]
		if !reservedBotSessionID(candidate) && (set == nil || set.empty()) {
			session.sessionID = candidate
			s.nextSessionID = candidate + 1
			if s.nextSessionID == 0 {
				s.nextSessionID = 1
			}
			s.addSessionLocked(session)
			return nil
		}
		candidate++
		if candidate == 0 {
			candidate = 1
		}
	}
	return fmt.Errorf("all 16-bit game session IDs are in use")
}

func reservedBotSessionID(sessionID uint16) bool {
	return sessionID >= 0x08e0 && sessionID <= 0x08ff
}

func (s *Server) setSessionAuthenticated(session *Session, username string, userID int, callsign string, level, authority byte) ([]*Session, error) {
	_, oldCallsign, oldUserID, _ := session.identity()
	var superseded []*Session
	s.mu.Lock()
	if _, tracked := s.sessions[session]; tracked && !session.channelMemberAssigned {
		if err := s.assignAnyChannelMemberLocked(session); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	session.setAuthenticatedWithAuthority(username, userID, callsign, level, authority)
	session.mu.Lock()
	if session.RoomNumber == -1 {
		session.lobbySince = roomMembershipVersion.Add(1)
	}
	session.mu.Unlock()
	if _, tracked := s.sessions[session]; tracked {
		s.online.replaceIdentity(session, oldUserID, oldCallsign)
		if set := s.online.byUserID[userID]; set != nil {
			superseded = set.except(session)
		}
		for _, previous := range superseded {
			_, previousCallsign, previousUserID, _ := previous.identity()
			s.online.removeIdentityOnly(previous, previousUserID, previousCallsign)
			s.releaseChannelMemberLocked(previous)
		}
		if err := s.syncChannelMemberLocked(session); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.mu.Unlock()
	return superseded, nil
}

func (s *Server) setSessionCallsign(session *Session, callsign string) {
	_, oldCallsign, oldUserID, _ := session.identity()
	s.mu.Lock()
	session.updateCallsign(callsign)
	if _, tracked := s.sessions[session]; tracked {
		s.online.replaceIdentity(session, oldUserID, oldCallsign)
		if err := s.syncChannelMemberLocked(session); err != nil && s.logger != nil {
			s.logger.Warn("refresh channel member identity", "error", err)
		}
	}
	s.mu.Unlock()
}

func (s *Server) disconnectSupersededSessions(sessions []*Session) {
	for _, previous := range sessions {
		previousUsername, _, previousUserID, _ := previous.identity()
		s.logger.Info("game session replaced by a newer account login",
			"user_id", previousUserID, "username", previousUsername)
		if previous.wire != nil {
			_ = previous.wire.Close()
		}
	}
}

func (s *Server) removeSession(session *Session) bool {
	loginName, callsign, userID, _ := session.identity()
	session.mu.RLock()
	wasInLobby := session.RoomNumber == -1 && userID > 0 && loginName != "" && strings.TrimSpace(callsign) != ""
	session.mu.RUnlock()
	s.removePrivateLadderSession(session)
	s.mu.Lock()
	s.releaseChannelMemberLocked(session)
	delete(s.sessions, session)
	s.online.remove(session)
	s.mu.Unlock()
	_ = s.leaveRoom(session, false)
	s.reclaimRelayPort(session)
	return wasInLobby
}

func (s *Server) inviteUsers(requester *Session) []InviteUser {
	sessions := s.channelSessions()
	users := make([]InviteUser, 0, len(sessions))
	seen := make(map[int]struct{}, len(sessions))
	for _, candidate := range sessions {
		if candidate == requester {
			continue
		}
		loginName, callsign, userID, level := candidate.identity()
		if loginName == "" || strings.TrimSpace(callsign) == "" || userID < 1 || uint64(userID) > uint64(^uint32(0)) {
			continue
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		users = append(users, InviteUser{Level: level, UserID: uint32(userID), Callsign: callsign})
	}
	return users
}

func (s *Server) channelSessions() []*Session {
	s.mu.RLock()
	result := s.online.lobby.sessions()
	s.mu.RUnlock()
	return result
}

func (s *Server) broadcastChannel(messageID uint16, payload []byte) {
	s.broadcastChannelTo(s.channelSessions(), messageID, payload)
}

func (s *Server) broadcastChannelTo(targets []*Session, messageID uint16, payload []byte) {
	for _, session := range targets {
		_ = session.Write(messageID, payload)
	}
}

func (s *Server) broadcastChannelRoster() {
	s.scheduleChannelRoster()
}

func (s *Server) broadcastChannelRosterNow() {
	s.broadcastChannelRosterTo(s.channelSessions())
}

func (s *Server) broadcastChannelRosterTo(targets []*Session) {
	s.channelRosterMu.Lock()
	defer s.channelRosterMu.Unlock()
	messagesBySession, err := s.channelRosterMessages(targets)
	if err != nil {
		s.logger.Warn("build channel roster broadcast", "error", err)
		return
	}
	for _, session := range targets {
		messages, ok := messagesBySession[session]
		if !ok {
			continue
		}
		if writeErr := session.WriteMessages(messages); writeErr != nil {
			s.logger.Debug("channel roster broadcast failed", "remote", session.wire.RemoteAddr(), "error", writeErr)
		}
	}
}

// channelRosterMessages returns one logical roster replacement per target.
// The first chunk replaces the roster; remaining chunks append records.
func (s *Server) channelRosterMessages(targets []*Session) (map[*Session][]transport.OutboundMessage, error) {
	var users [channelSheetCount][]ChannelUser
	targetSheets := make(map[*Session]byte, len(targets))

	s.mu.RLock()
	for _, session := range s.online.lobby.sessions() {
		if !session.channelMemberAssigned || session.channelSheet >= channelSheetCount {
			continue
		}
		session.mu.RLock()
		user := ChannelUser{
			MemberID:    session.channelMemberID,
			SessionID:   session.SessionID(),
			LoginName:   session.LoginName,
			Callsign:    session.Callsign,
			Authority:   session.UserAuthority,
			Level:       session.UserLevel,
			PrideMarkID: session.PrideMarkID,
			PrideName:   session.PrideName,
		}
		session.mu.RUnlock()
		users[session.channelSheet] = append(users[session.channelSheet], user)
	}
	for _, session := range targets {
		if session.channelMemberAssigned && session.channelSheet < channelSheetCount {
			targetSheets[session] = session.channelSheet
		}
	}
	s.mu.RUnlock()

	var sheetMessages [channelSheetCount][]transport.OutboundMessage
	for sheet := byte(0); sheet < channelSheetCount; sheet++ {
		if len(users[sheet]) == 0 {
			continue
		}
		messages, err := channelRosterOutboundMessages(sheet, users[sheet])
		if err != nil {
			return nil, err
		}
		sheetMessages[sheet] = messages
	}

	messagesBySession := make(map[*Session][]transport.OutboundMessage, len(targetSheets))
	for session, sheet := range targetSheets {
		if messages := sheetMessages[sheet]; messages != nil {
			messagesBySession[session] = messages
		}
	}
	return messagesBySession, nil
}

func channelRosterOutboundMessages(sheet byte, users []ChannelUser) ([]transport.OutboundMessage, error) {
	payloads, err := ChannelUsersPayloads(sheet, users, transport.MaxEncryptedPayload)
	if err != nil {
		return nil, err
	}
	messages := make([]transport.OutboundMessage, len(payloads))
	for index, payload := range payloads {
		messageID := ReplyChannelCharsMore
		if index == 0 {
			messageID = ReplyChannelChars
		}
		messages[index] = transport.OutboundMessage{MessageID: messageID, Payload: payload}
	}
	return messages, nil
}

func (s *Server) allocateRoom(config RoomConfig) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	number, available := lowestFreeRoom(&s.occupiedRooms)
	if !available {
		return nil, fmt.Errorf("no free room number below %d", maxRoomCount)
	}
	room := newRoom(number, config)
	s.rooms[number] = room
	s.indexRoomLocked(room)
	s.logger.Info("room created", "room", number, "title", config.Title, "mode", fmt.Sprintf("0x%08x", config.Mode), "capacity", config.Capacity, "gameFlags", config.GameFlags, "ruleSetting", config.RuleSetting, "mapSetting", config.MapSetting)
	return room, nil
}

func (s *Server) roomSnapshots() []RoomSnapshot {
	s.mu.RLock()
	rooms := make([]*Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	s.mu.RUnlock()
	snapshots := make([]RoomSnapshot, 0, len(rooms))
	for _, room := range rooms {
		snapshots = append(snapshots, room.Snapshot())
	}
	return snapshots
}

func (s *Server) sessionRoom(session *Session) *Room {
	session.mu.RLock()
	number := session.RoomNumber
	session.mu.RUnlock()
	if number < 0 {
		return nil
	}
	s.mu.RLock()
	room := s.rooms[number]
	s.mu.RUnlock()
	return room
}

func (s *Server) roomByNumber(number int16) *Room {
	s.mu.RLock()
	room := s.rooms[number]
	s.mu.RUnlock()
	return room
}

func (s *Server) handleEnterRoom(ctx context.Context, session *Session, payload []byte) error {
	request, err := ParseEnterRoomRequest(payload)
	if err != nil {
		return err
	}
	number := request.Number
	room := s.roomByNumber(number)
	state := s.loadRosterState(ctx, session, true)
	member := s.enterMember(session, state)
	status := byte(0x7e)
	reentering := false
	if room != nil {
		if _, exists := room.MemberSlot(session); exists {
			// Keep retried room entry idempotent and synchronize only the caller;
			// broadcasting a duplicate join/roster disrupts existing members.
			status = 0
			reentering = true
			member = s.enterMember(session, state)
		} else {
			snapshot := room.Snapshot()
			switch {
			case room.AtCapacity():
				status = 0x7d
			case snapshot.Config.Password != request.Password && !session.hasObserverAuthority():
				status = 0x7f
			case !snapshot.Open:
				status = 0x8b
			case !session.hasStaffAuthority() && !roomAllowsLevel(snapshot.Config, member.Level):
				classMinimum, classMaximum := roomClassLimits(snapshot.Config)
				s.logger.Info("room class limit rejected",
					"room", number, "level", member.Level,
					"class_minimum", classMinimum, "class_maximum", classMaximum)
				status = 0x80
			default:
				if _, ok := room.Add(session); !ok {
					status = 0x7d
				} else {
					status = 0
					member = s.enterMember(session, state)
				}
			}
		}
	}
	enterPayload, err := EnterRoomPayload(status, member)
	if err != nil {
		return err
	}
	if status != 0 || room == nil {
		return session.Write(ReplyEnterRoom, enterPayload)
	}
	s.refreshSessionLobby(session)
	s.removePrivateLadderSearch(session)
	if reentering {
		room.Broadcast(ReplyEnterRoom, enterPayload)
		rosterMessages, err := s.roomRosterMessages(ctx, room)
		if err != nil {
			return err
		}
		return session.WriteMessages(rosterMessages)
	}
	session.mu.RLock()
	observedIP, observedPort := session.UDPIP, session.UDPPort
	reportedIP, reportedPort := session.LocalUDPIP, session.LocalUDPPort
	relayPort := session.RelayPort
	session.mu.RUnlock()
	username, _, userID, _ := session.identity()
	s.logger.Info("room peer endpoints announced",
		"message_id", "0x1607", "room", number, "user_id", userID, "username", username,
		"global_ip", member.GlobalIP, "global_port", member.GlobalPort,
		"local_ip", member.LocalIP, "local_port", member.LocalPort,
		"observed_ip", observedIP, "observed_port", observedPort,
		"reported_ip", reportedIP, "reported_port", reportedPort, "relay_port", relayPort)
	room.Broadcast(ReplyEnterRoom, enterPayload)
	rosterMessages, err := s.roomRosterMessages(ctx, room)
	if err != nil {
		return err
	}
	if err := session.WriteMessages(rosterMessages); err != nil {
		return err
	}
	if room.Snapshot().PowerMaster {
		if err := s.sendPowerRoomItems(session, 1); err != nil {
			return err
		}
	}
	s.syncPrideBattleTitle(room)
	s.broadcastChannelRoster()
	s.broadcastFieldUsers(room)
	s.broadcastJoinAllow()
	return nil
}

func (s *Server) handleQuickEnter(ctx context.Context, session *Session) error {
	_, _, _, level := session.identity()
	selected := s.quickEnterRoomNumberForLevel(level)
	payload := []byte{0xff, 0xff}
	if selected >= 0 {
		binary.LittleEndian.PutUint16(payload, uint16(selected))
	}
	return s.handleEnterRoom(ctx, session, payload)
}

func (s *Server) rosterPayload(ctx context.Context, room *Room) ([]byte, error) {
	return s.rosterPayloadFromSnapshot(ctx, room.Snapshot())
}

func (s *Server) rosterPayloadFromSnapshot(ctx context.Context, snapshot RoomSnapshot) ([]byte, error) {
	members := make([]RosterMember, 0, len(snapshot.Members))
	for _, session := range snapshot.Members {
		if session.IsBot {
			continue
		}
		members = append(members, s.rosterMember(session, s.loadRosterState(ctx, session, true)))
	}
	return RoomRosterPayload(snapshot, members)
}

func (s *Server) roomRosterMessages(ctx context.Context, room *Room) ([]transport.OutboundMessage, error) {
	snapshot := room.Snapshot()
	roster, err := s.rosterPayloadFromSnapshot(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	messages := make([]transport.OutboundMessage, 1, 1+len(snapshot.Members))
	messages[0] = transport.OutboundMessage{MessageID: ReplyRoomChars, Payload: roster}
	for _, member := range snapshot.Members {
		if payload, ok := botAISlotPayload(member); ok {
			messages = append(messages, transport.OutboundMessage{MessageID: ReplyAddAISlot, Payload: payload})
		}
	}
	return messages, nil
}

func botAISlotPayload(session *Session) ([]byte, bool) {
	session.mu.RLock()
	defer session.mu.RUnlock()
	if !session.IsBot || session.RoomSlot < 0 || (session.RoomTeam != 1 && session.RoomTeam != 2) {
		return nil, false
	}
	column := byte(5)
	clientTeam := byte(0)
	if session.RoomTeam == 2 {
		column = 6
		clientTeam = 1
	}
	form := byte(1)
	if session.BotWolf {
		form = 2
	}
	return AddAISlotPayload(
		byte(session.RoomSlot), column, session.RoomTeamSlot, clientTeam,
		session.BotDifficulty, session.BotNumber, form,
	), true
}

type rosterState struct {
	RedCharacter  uint16
	BlueCharacter uint16
	RedCharItems  [16]uint16
	BlueCharItems [16]uint16
	PowerItems    [15]uint16
	Crests        []store.CrestDelegateSlot
}

func (s *Server) loadRosterState(ctx context.Context, session *Session, includePower bool) rosterState {
	if session.IsBot {
		return defaultRosterState()
	}
	return session.cachedRosterState(includePower, func() rosterState {
		return s.loadRosterStateUncached(ctx, session, includePower)
	})
}

func defaultRosterState() rosterState {
	return rosterState{RedCharacter: 30006, BlueCharacter: 30005}
}

func (s *Server) loadRosterStateUncached(ctx context.Context, session *Session, includePower bool) rosterState {
	state := defaultRosterState()
	_, _, userID, _ := session.identity()
	items, itemsErr := s.users.Items(ctx, userID)
	loadout, loadoutErr := s.users.CharacterSelection(ctx, userID)
	if itemsErr == nil && loadoutErr == nil {
		owned := characterOwnership(items)
		state.RedCharacter = uint16(resolveCharacter(2, loadout, owned))
		state.BlueCharacter = uint16(resolveCharacter(1, loadout, owned))
	} else {
		if itemsErr != nil {
			s.logger.Warn("character ownership unavailable for roster; using defaults", "user_id", userID, "error", itemsErr)
		}
		if loadoutErr != nil {
			s.logger.Warn("character loadout unavailable for roster; using defaults", "user_id", userID, "error", loadoutErr)
		}
	}
	if crests, ok := s.crestStore(); ok {
		crestState, err := crests.CrestState(ctx, userID)
		if err != nil {
			s.logger.Warn("crest emblems unavailable for roster", "user_id", userID, "error", err)
		}
		state.Crests = rosterCrests(crestState.Delegates)
	}
	if equipmentStore, ok := s.users.(store.CharacterEquipmentStore); ok {
		characterEquipment, err := equipmentStore.CharacterEquipment(ctx, userID)
		if err != nil {
			s.logger.Warn("character equipment unavailable for roster", "user_id", userID, "error", err)
		} else {
			state.RedCharItems = characterItemIDs(characterEquipment[int(state.RedCharacter)])
			state.BlueCharItems = characterItemIDs(characterEquipment[int(state.BlueCharacter)])
		}
	}
	if !includePower {
		return state
	}
	equipment, err := s.users.Equipment(ctx, userID)
	if err != nil {
		s.logger.Warn("power equipment unavailable for roster", "user_id", userID, "error", err)
		return state
	}
	for index := 0; index < 10; index++ {
		itemID := equipmentItemID(equipment[24+index])
		if itemID > 0 && itemID <= 0xffff {
			state.PowerItems[index] = uint16(itemID)
		}
	}
	return state
}

func characterItemIDs(equipment store.CharacterEquipment) [store.CharacterEquipmentSlots]uint16 {
	var result [store.CharacterEquipmentSlots]uint16
	for slot, uniqueID := range equipment {
		itemID := equipmentItemID(uniqueID)
		if itemID > 0 && itemID <= 0xffff {
			result[slot] = uint16(itemID)
		}
	}
	return result
}

func (s *Server) rosterMember(session *Session, state rosterState) RosterMember {
	session.mu.RLock()
	status := session.RoomStatus
	if status > 0 {
		status = 1
	}
	member := RosterMember{
		Slot:          byte(session.RoomSlot),
		Team:          session.RoomTeam,
		TeamSlot:      session.RoomTeamSlot,
		Status:        status,
		RoomStatus:    session.RoomStatus,
		SessionID:     session.SessionID(),
		Authority:     session.UserAuthority,
		Level:         session.UserLevel,
		LoginName:     session.LoginName,
		Callsign:      session.Callsign,
		PrideMarkID:   session.PrideMarkID,
		PrideName:     session.PrideName,
		RedCharacter:  state.RedCharacter,
		BlueCharacter: state.BlueCharacter,
		RedCharItems:  state.RedCharItems,
		BlueCharItems: state.BlueCharItems,
		PowerItems:    state.PowerItems,
		Crests:        state.Crests,
	}
	session.mu.RUnlock()
	member.GlobalIP, member.GlobalPort, member.LocalIP, member.LocalPort = s.peerEndpoints(session)
	return member
}

func (s *Server) enterMember(session *Session, state rosterState) RosterMember {
	member := s.rosterMember(session, state)
	s.relayMu.Lock()
	mode := s.peerEndpointMode
	s.relayMu.Unlock()
	if mode != 1 {
		return member
	}
	session.mu.RLock()
	member.GlobalIP = session.PeerIP
	member.GlobalPort = session.UDPPort
	if session.LocalUDPIP.Is4() && session.LocalUDPPort != 0 {
		member.LocalIP = session.LocalUDPIP
		member.LocalPort = session.LocalUDPPort
	} else if session.UDPIP.Is4() {
		member.LocalIP = session.UDPIP
		member.LocalPort = session.UDPPort
	}
	session.mu.RUnlock()
	if !member.GlobalIP.Is4() {
		member.GlobalIP = netip.IPv4Unspecified()
	}
	return member
}

func (s *Server) broadcastRoster(ctx context.Context, room *Room) error {
	messages, err := s.roomRosterMessages(ctx, room)
	if err != nil {
		return err
	}
	for _, session := range room.Snapshot().Members {
		_ = session.WriteMessages(messages)
	}
	return nil
}

func (s *Server) leaveRoom(session *Session, notify bool) error {
	room := s.sessionRoom(session)
	if room == nil {
		s.reclaimRelayPort(session)
		session.invalidateRosterState()
		slot, pending := session.pendingClientRoomDeparture()
		if notify {
			if !pending {
				slot = 0
			}
			if err := session.Write(ReplyExitRoom, ExitRoomPayload(slot)); err != nil {
				return err
			}
		}
		if pending {
			session.clearPendingClientRoomExit(slot)
		}
		return nil
	}
	beforeSnapshot := room.Snapshot()
	before := beforeSnapshot.Master
	session.mu.RLock()
	departingTeam := session.RoomTeam
	session.mu.RUnlock()
	slot, master, empty := room.Remove(session)
	s.reclaimRelayPort(session)
	session.invalidateRosterState()
	s.refreshSessionLobby(session)
	if slot != 0xff {
		// Broadcast before removal for both explicit exits and disconnects so
		// the departing slot remains identifiable to recipients.
		// After removal, send the departing copy separately and broadcast to the survivors.
		_ = session.Write(ReplyExitRoom, ExitRoomPayload(slot))
		room.Broadcast(ReplyExitRoom, ExitRoomPayload(slot))
		if beforeSnapshot.PowerMaster {
			_ = s.sendPowerRoomItems(session, 0)
		}
	}
	powerRoomEnded := beforeSnapshot.PowerMaster && slot == before
	if powerRoomEnded && !empty {
		room.SetPowerMaster(false)
		removePayload, payloadErr := PowerMasterItemsPayload(0, powerRoomItems[:])
		if payloadErr == nil {
			room.Broadcast(ReplyPowerMasterItems, removePayload)
		}
		rosterCtx, cancelRoster := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.broadcastRoster(rosterCtx, room); err != nil {
			s.logger.Warn("broadcast Power Master removal", "room", room.Number, "error", err)
		}
		cancelRoster()
	}
	if master != before && !empty {
		room.Broadcast(ReplyChangeBoss, ChangeBossPayload(master))
	}
	if !empty {
		s.syncPrideBattleTitle(room)
	}
	if !empty {
		if winTeam, endGame, forfeited := room.ForfeitEmptyTeam(departingTeam); forfeited {
			s.finishRound(room, winTeam, endGame)
		}
	}
	if empty {
		s.deleteRoom(room)
		s.broadcastFieldExit(room)
	} else {
		s.broadcastFieldUsers(room)
	}
	if powerRoomEnded {
		_ = s.broadcastFieldList()
	}
	s.broadcastJoinAllow()
	if !notify {
		return nil
	}
	s.broadcastChannelRoster()
	return nil
}

func (s *Server) broadcastFieldList(extra ...*Session) error {
	payload, err := FieldListPayload(s.roomSnapshots())
	if err != nil {
		return err
	}
	targets := make(map[*Session]struct{})
	for _, session := range extra {
		targets[session] = struct{}{}
	}
	s.mu.RLock()
	for session := range s.sessions {
		session.mu.RLock()
		inChannel := session.RoomNumber == -1
		session.mu.RUnlock()
		if inChannel {
			targets[session] = struct{}{}
		}
	}
	s.mu.RUnlock()
	for session := range targets {
		if writeErr := session.Write(ReplyFieldList, payload); writeErr != nil {
			s.logger.Debug("field-list broadcast failed", "remote", session.wire.RemoteAddr(), "error", writeErr)
		}
	}
	return nil
}

func (s *Server) sendFieldList(session *Session) error {
	payload, err := FieldListPayload(s.roomSnapshots())
	if err != nil {
		return err
	}
	return session.Write(ReplyFieldList, payload)
}

func (s *Server) broadcastFieldCreate(room *Room) {
	s.scheduleFieldCreate(room)
}

func (s *Server) broadcastFieldState(room *Room) {
	s.scheduleFieldState(room)
}

func (s *Server) broadcastFieldUsers(room *Room) {
	s.scheduleFieldUsers(room)
}

func (s *Server) broadcastGameState(room *Room, started bool) {
	s.scheduleGameState(room, started)
}

func (s *Server) broadcastFieldExit(room *Room) {
	s.scheduleFieldExit(room)
}

func (s *Server) broadcastJoinAllow() {
	s.scheduleJoinAllow()
}

func (s *Server) connectionAdvertisedIP(connection net.Conn) netip.Addr {
	return s.connectionAdvertisedIPForAddresses(connection.LocalAddr(), connection.RemoteAddr())
}

func (s *Server) connectionAdvertisedIPForAddresses(localAddress, remoteAddress net.Addr) netip.Addr {
	local, localOK := tcpAddress(localAddress)
	remote, remoteOK := tcpAddress(remoteAddress)
	// A non-loopback peer may be reaching us through Docker, a reverse proxy,
	// or a public interface. The listener's local address is then an internal
	// container-only interface, not an address the peer can use for
	// match UDP. Prefer the explicitly configured external address.
	if remoteOK && !remote.IsLoopback() && s.publicIP.Is4() {
		return s.publicIP
	}
	// Loopback peers can use the listener address directly.
	if localOK && local.Is4() && !local.IsUnspecified() {
		return local
	}
	return s.advertisedIP
}

func tcpAddress(address net.Addr) (netip.Addr, bool) {
	tcp, ok := address.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	addr, ok := netip.AddrFromSlice(tcp.IP)
	return addr.Unmap(), ok
}

// pushInventory sends a fresh CS_IN_ITEMLIST_ACK (0x1431). The shop and
// W-Pack dispatchers (sub_34226E90, sub_3422EB90) both handle it. before is
// the inventory loaded before the grant: character items it lacks (and that
// the purchase ACK did not already add, skip) are appended for the client's
// character list.
func (s *Server) pushInventory(ctx context.Context, session *Session, userID int, before []store.Item, skip map[int]bool) error {
	items, err := s.users.Items(ctx, userID)
	if err != nil {
		s.logBackendFallback("reload inventory after purchase", userID, err)
		return nil
	}
	inventory, err := ItemListPayloadWithNewCharacters(items, newCharacterItems(before, items, skip))
	if err != nil {
		return err
	}
	return session.Write(ReplyItemList, inventory)
}

// newCharacterItems returns one record per character item ID (client range
// sub_3405AD30) present in after but not in before, excluding skip. A nil
// before means the previous state is unknown and nothing is returned.
func newCharacterItems(before, after []store.Item, skip map[int]bool) []store.Item {
	if before == nil {
		return nil
	}
	held := make(map[int]bool, len(before))
	for _, item := range before {
		held[item.ID] = true
	}
	var added []store.Item
	for _, item := range after {
		if item.Type != 1 || item.ID < 30001 || item.ID > 34000 || held[item.ID] || skip[item.ID] {
			continue
		}
		held[item.ID] = true
		added = append(added, item)
	}
	return added
}

// kickVoteItemID is the "Kick Vote" ticket (BuyNowData 10039).
const kickVoteItemID = 10039

// giftRefundValue prices the gift box conversion. The client offers only two
// outcomes, KP (stringtable 9459) and Gold (9460), and hides the button on
// 0/0 (DETAILLIST_ACK sub_3425B990). The ratio is server policy (UNVERIFIED):
//   - Gold and WC items: half of the shop price, in that currency (the same
//     ratio as selling an inventory item);
//   - Cash items: the KP reward the shop grants for that option.
//
// A duration or quantity the shop does not list (event and admin gifts, e.g.
// 3 days) is valued pro rata from the nearest listed option.
func (s *Server) giftRefundValue(gift store.Gift) (wc, gold int) {
	if s.shopCatalog == nil || gift.ItemID < 1 {
		return 0, 0
	}
	value := gift.Days
	if value == 0 {
		value = max(gift.Quantity, 1)
	}
	option, ok := nearestShopOption(s.shopCatalog.Options(gift.ItemID), value)
	if !ok || option.Days < 1 {
		return 0, 0
	}
	scale := func(amount uint32) int {
		if amount == 0 {
			return 0
		}
		return max(int(uint64(amount)*uint64(value)/uint64(option.Days)), 1)
	}
	switch option.Currency {
	case 1:
		return scale(option.WolfCoinReward), 0
	case 2:
		return scale(option.Price / 2), 0
	case 4:
		return 0, scale(option.Price / 2)
	}
	return 0, 0
}

// nearestShopOption picks the option matching value, else the smallest one
// above it, else the largest one below it.
func nearestShopOption(options []shopdata.Offer, value int) (shopdata.Offer, bool) {
	var best shopdata.Offer
	found := false
	for _, option := range options {
		if option.Days == value {
			return option, true
		}
		switch {
		case !found:
			best, found = option, true
		case option.Days > value && (best.Days < value || option.Days < best.Days):
			best = option
		case option.Days < value && best.Days < value && option.Days > best.Days:
			best = option
		}
	}
	return best, found
}

// premiumShop loads the Premium Shop for a user; ok is false without a backend.
func (s *Server) premiumShop(ctx context.Context, userID int) (store.PremiumShop, bool) {
	premium, ok := s.users.(store.PremiumShopStore)
	if !ok {
		return store.PremiumShop{}, false
	}
	shop, err := premium.PremiumShop(ctx, userID)
	if err != nil {
		s.logBackendFallback("load premium shop", userID, err)
		return store.PremiumShop{}, false
	}
	return shop, true
}

// premiumOffer accepts a CS_IN_BUYITEM_REQ made from the Premium Shop: the
// client builds it from the 0x146D entry (Cash, pricing/purchase mode 2) with
// the premium price, which differs from shopdata. The goal must be reached.
func (s *Server) premiumOffer(ctx context.Context, userID int, request BuyRequest) (shopdata.Offer, bool) {
	shop, ok := s.premiumShop(ctx, userID)
	if !ok || !shop.Reached() || request.PurchaseMode != 2 {
		return shopdata.Offer{}, false
	}
	item, ok := shop.Find(request.ItemID, request.Days)
	if !ok || item.Price != request.Price || item.WolfCoinReward != request.WolfCoinReward {
		return shopdata.Offer{}, false
	}
	return shopdata.Offer{
		ItemID: item.ItemID, Days: item.Days, PricingMode: 2, PurchaseMode: 2,
		Currency: 1, Price: item.Price, WolfCoinReward: item.WolfCoinReward,
	}, true
}

// eventBonuses loads the active 1+1 event rows (migration 046).
func (s *Server) eventBonuses(ctx context.Context) []store.EventBonus {
	events, ok := s.users.(store.EventBonusStore)
	if !ok {
		return nil
	}
	bonuses, err := events.EventBonusItems(ctx)
	if err != nil {
		s.logBackendFallback("load event bonus items", 0, err)
		return nil
	}
	return bonuses
}

// grantEventBonus sends the 1+1 bonus for a purchased shop option. The client
// offers the bonus only when the bought option value equals the row's required
// value (sub_3427D180, sub_34189790), so the server applies the same match
// against its own table instead of trusting the echoed request fields.
func (s *Server) grantEventBonus(ctx context.Context, session *Session, userID, itemID, value int) {
	events, ok := s.users.(store.EventBonusStore)
	if !ok {
		return
	}
	for _, bonus := range s.eventBonuses(ctx) {
		if bonus.ItemID != itemID || bonus.RequiredValue != value {
			continue
		}
		granted, err := events.GrantEventBonus(ctx, userID, bonus)
		if err != nil {
			s.logBackendFallback("grant event bonus", userID, err)
			return
		}
		s.logger.Info("event bonus granted", "user_id", userID, "item", itemID, "bonus_item", bonus.BonusItemID,
			"bonus_amount", bonus.BonusAmount, "granted", granted)
		if granted {
			_ = session.Write(ReplyNewGift, []byte{0})
		}
		return
	}
}

// prideBattleRecord builds the Pride Battle record of a finished match: mode,
// map, both sides' round/point scores and every player's kills, deaths and
// score (kills, plus objectives worth two kills).
func prideBattleRecord(room *Room, winTeam byte, players []MatchPlayer, winners, losers []int) store.PrideBattleRecord {
	snapshot := room.Snapshot()
	record := store.PrideBattleRecord{
		WinnerUserIDs: winners, LoserUserIDs: losers,
		Mode: int(snapshot.Config.Mode), Map: int(snapshot.Config.Map),
	}
	red, blue := int(snapshot.RedScore), int(snapshot.BlueScore)
	if winTeam == blueTeam {
		record.WinnerScore, record.LoserScore = blue, red
	} else {
		record.WinnerScore, record.LoserScore = red, blue
	}
	for _, player := range players {
		if player.Session == nil || player.Session.IsBot {
			continue
		}
		_, _, userID, _ := player.Session.identity()
		stats := player.Stats
		record.Members = append(record.Members, store.PrideBattleMemberEntry{
			UserID: userID, Kills: int(stats.Kills), Deaths: int(stats.Deaths),
			Score: int(stats.Kills) + 2*int(stats.Plants+stats.Defuses),
		})
	}
	return record
}

// isLadderChannelMatch reports whether every human player is in a Ladder
// channel.
func isLadderChannelMatch(players []MatchPlayer) bool {
	foundHuman := false
	for _, player := range players {
		if player.Session == nil || player.Session.IsBot {
			continue
		}
		foundHuman = true
		switch player.Session.channelType() {
		case store.ChannelTypeLadder, store.ChannelTypePrivateLadder:
		default:
			return false
		}
	}
	return foundHuman
}
