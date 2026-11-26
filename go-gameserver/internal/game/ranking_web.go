package game

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

// Ranking web module (2013 TR RankingSection, "Ranking Web Module").
//
// The client learns the host from 0x050E (u32 IPv4 + u16 big-endian port, the
// 0x050C layout; handler sub_3408F3E0) and sends WinINet HTTP/1.0 GETs with a
// relative object name ("RankList.aspx?..."; builders sub_341737D0..
// sub_34173D00, table byte_343D56F0 index 9..14). It reads the reply with a
// single 4096-byte InternetReadFile (sub_34175610), finds the first line
// starting with '+' ("+result;type", sub_34175170) and parses the rest by
// type. Lines end with '\n' and fields are ';'-separated; strtok skips empty
// fields, so no field may be empty. Names and comments are URL-encoded UTF-8.
//
// Used pages: 9 RankList, 10 RankListMyRank, 11 CurrentDay, 13
// UpdateCommentTop3, 14 CommentListTop3. The Insert*.aspx (1-8) and
// CalculateRank.aspx (12) builders have no caller in the 2013 client.

const (
	rankingTypeRankList   = 9
	rankingTypeMyRank     = 10
	rankingTypeCurrentDay = 11
	rankingTypeComment    = 13
	rankingTypeTop3       = 14

	// rankingNone is the client's "no rank" value (sub_34173E60 fills it,
	// sub_34234020 draws a new/none marker for it).
	rankingNone = 100000000
	// rankingPageSize is the RankList row count (15 WRankingListButton).
	rankingPageSize = 15
	// rankingBoardLimit caps one board query.
	rankingBoardLimit = 5000
	// rankingAccountMax: the account field is strcpy_s'd into 17 bytes.
	rankingAccountMax = 16
	// rankingCommentMax keeps a comment inside its 624-byte top-3 slot.
	rankingCommentMax = 100
)

// Period types (strings 9525-9528 Günlük/Haftalık/Aylık/Toplam).
const (
	rankingDaily   = 0
	rankingWeekly  = 1
	rankingMonthly = 2
	rankingTotal   = 3
)

// rankingEpoch is date id 0 of WT_Attributes\RankingDateData.dat
// (852 days, 2012-09-01 .. 2014-12-31). The client only shows dates that
// exist in that table, so today is mapped onto it.
var rankingEpoch = time.Date(2012, time.September, 1, 0, 0, 0, 0, time.UTC)

// rankingDateID maps a real day onto the date table: the same month and day
// in 2013 (September-December) or 2014 (January-August), so the three days
// and three weeks before it exist too.
func rankingDateID(day time.Time) int {
	year := 2014
	if day.Month() >= time.September {
		year = 2013
	}
	dayOfMonth := day.Day()
	if day.Month() == time.February && dayOfMonth == 29 {
		dayOfMonth = 28
	}
	mapped := time.Date(year, day.Month(), dayOfMonth, 0, 0, 0, 0, time.UTC)
	return int(mapped.Sub(rankingEpoch).Hours() / 24)
}

// rankingTableWeek is the date table's week byte (record +7) of date id: the
// Sunday-based week of the year; the days of a week that holds 1 January
// belong to week 1 of the new year (the table keeps 53 in December 2014).
func rankingTableWeek(id int) int {
	day := rankingEpoch.AddDate(0, 0, id)
	jan1 := time.Date(day.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	week := (day.YearDay()-1+int(jan1.Weekday()))/7 + 1
	if day.Year() != 2014 {
		nextJan1 := time.Date(day.Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		weekStart := day.AddDate(0, 0, -int(day.Weekday()))
		if nextJan1.Sub(weekStart) < 7*24*time.Hour {
			week = 1
		}
	}
	return week
}

type rankingPeriod struct {
	from, to time.Time // zero from = all time
	id       int64     // the client's PERIOD value of this period
}

// rankingPeriods returns the requested period and the two before it. The
// client offers today and the two days before (daily), the three last whole
// weeks (weekly) and this month and the two before (monthly); sub_34233B20.
func rankingPeriods(periodType int, period int64, now time.Time) []rankingPeriod {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today := int64(rankingDateID(now))
	periods := make([]rankingPeriod, 3)
	switch periodType {
	case rankingDaily:
		offset := today - period
		if offset < 0 || offset > 60 {
			offset, period = 0, today
		}
		for k := range periods {
			from := midnight.AddDate(0, 0, -int(offset)-k)
			periods[k] = rankingPeriod{from: from, to: from.AddDate(0, 0, 1), id: period - int64(k)}
		}
	case rankingWeekly:
		weekStart := midnight.AddDate(0, 0, -int(midnight.Weekday()))
		offset := 1
		for k := 0; k <= 8; k++ {
			if int64(rankingTableWeek(int(today)-7*k)) == period {
				offset = k
				break
			}
		}
		for k := range periods {
			from := weekStart.AddDate(0, 0, -7*(offset+k))
			periods[k] = rankingPeriod{from: from, to: from.AddDate(0, 0, 7), id: int64(rankingTableWeek(int(today) - 7*(offset+k)))}
		}
	case rankingMonthly:
		offset := 0
		if period >= 1 && period <= 12 {
			offset = (int(now.Month()) - int(period) + 12) % 12
		}
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		for k := range periods {
			from := monthStart.AddDate(0, -(offset + k), 0)
			periods[k] = rankingPeriod{from: from, to: from.AddDate(0, 1, 0), id: int64(from.Month())}
		}
	default:
		return []rankingPeriod{{}}
	}
	return periods
}

// ConfigureRankingWeb sets the port advertised in 0x050E (0 disables it).
func (s *Server) ConfigureRankingWeb(port uint16) {
	s.rankingWebPort = port
}

// RankingWebEndpointPayload has the 0x050C layout: result byte, IPv4 address,
// big-endian port.
func RankingWebEndpointPayload(address netip.Addr, port uint16) ([]byte, error) {
	return BuddyServerEndpointPayload(address, port)
}

// RunRankingWeb serves the ranking web module.
func (s *Server) RunRankingWeb(ctx context.Context, address string) error {
	return transport.ServeTCP(ctx, address, "ranking web", func(listener net.Listener) {
		s.logger.Info("ranking web server started", "address", listener.Addr().String())
	}, func(connection net.Conn) {
		s.handleRankingConnection(ctx, connection)
	})
}

func (s *Server) handleRankingConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(connection, 4096)
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	// Drain the headers. WinINet sends a 1024-byte header buffer padded with
	// NULs (sub_34173C60), so stop at the blank line or when input pauses.
	_ = connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		line, err := reader.ReadString('\n')
		if err != nil || strings.Trim(line, "\r\n\x00") == "" {
			break
		}
	}
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	page, params, ok := parseRankingRequest(requestLine)
	status := "200 OK"
	body := ""
	if ok {
		remote := netip.Addr{}
		if tcp, isTCP := connection.RemoteAddr().(*net.TCPAddr); isTCP {
			remote, _ = netip.AddrFromSlice(tcp.IP)
			remote = remote.Unmap()
		}
		body = s.rankingResponse(ctx, page, params, remote, time.Now())
	}
	if body == "" {
		status, body = "404 Not Found", ""
	}
	s.logger.Debug("ranking web request", "page", page, "remote", connection.RemoteAddr(), "status", status)
	// Headers and body in one write: the client reads the reply once.
	response := fmt.Sprintf("HTTP/1.0 %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, len(body), body)
	_, _ = connection.Write([]byte(response))
}

// parseRankingRequest reads "GET <object> HTTP/1.x". The object is relative
// ("RankList.aspx?TYPE=0&..."); a leading path is tolerated.
func parseRankingRequest(line string) (string, map[string]string, bool) {
	fields := strings.Fields(strings.Trim(line, "\r\n\x00"))
	if len(fields) < 2 || fields[0] != "GET" {
		return "", nil, false
	}
	target := fields[1]
	query := ""
	if index := strings.IndexByte(target, '?'); index >= 0 {
		target, query = target[:index], target[index+1:]
	}
	if index := strings.LastIndexByte(target, '/'); index >= 0 {
		target = target[index+1:]
	}
	params := make(map[string]string)
	for _, pair := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		if key == "" {
			continue
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		params[strings.ToUpper(key)] = value
	}
	return strings.ToLower(target), params, true
}

func rankingParamInt(params map[string]string, key string) int64 {
	value, err := strconv.ParseInt(params[key], 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// rankingEscape URL-encodes a name or comment the way the client decodes it
// (sub_34173630: %XX and '+').
func rankingEscape(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out.WriteByte(c)
		case c == ' ':
			out.WriteByte('+')
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func rankingAccountField(account string) string {
	if account == "" {
		return "0"
	}
	if len(account) > rankingAccountMax {
		account = account[:rankingAccountMax]
	}
	return account
}

func rankingNameField(name string) string {
	if name == "" {
		return "0"
	}
	return rankingEscape(name)
}

func (s *Server) rankingResponse(ctx context.Context, page string, params map[string]string, remote netip.Addr, now time.Time) string {
	switch page {
	case "currentday.aspx":
		// sub_341753B0 keeps the last "id;month" line and must reach EOF, so
		// this reply has no trailing newline.
		return fmt.Sprintf("+1;%d\n%d;%d", rankingTypeCurrentDay, rankingDateID(now), int(now.Month()))
	case "ranklist.aspx":
		return s.rankingListResponse(ctx, params, now)
	case "ranklistmyrank.aspx":
		return s.rankingMyRankResponse(ctx, params, now)
	case "commentlisttop3.aspx":
		return s.rankingTop3Response(ctx, params, now)
	case "updatecommenttop3.aspx":
		result := 0
		if s.updateRankingComment(ctx, params, remote, now) {
			result = 1
		}
		return fmt.Sprintf("+%d;%d\n", result, rankingTypeComment)
	}
	return ""
}

type rankingRow struct {
	entry store.RankingEntry
	rank  int
}

// rankingBoard loads one board; nil on error or without a ranking store.
func (s *Server) rankingBoard(ctx context.Context, pride bool, kind int, period rankingPeriod) []store.RankingEntry {
	ranking, ok := s.users.(store.RankingStore)
	if !ok || kind < store.RankingKindKill || kind > store.RankingKindDeath {
		return nil
	}
	entries, err := ranking.RankingBoard(ctx, store.RankingQuery{Pride: pride, Kind: kind, From: period.from, To: period.to}, rankingBoardLimit)
	if err != nil {
		s.logger.Warn("load ranking board", "pride", pride, "kind", kind, "error", err)
		return nil
	}
	return entries
}

func rankingRanks(entries []store.RankingEntry) map[string]int {
	ranks := make(map[string]int, len(entries))
	for index, entry := range entries {
		ranks[strings.ToLower(entry.Account)] = index + 1
	}
	return ranks
}

func rankingPercent(entry store.RankingEntry, kind int) int64 {
	if kind != store.RankingKindRecord || entry.Value+entry.Losses == 0 {
		return 0
	}
	return entry.Value * 100 / (entry.Value + entry.Losses)
}

// rankingHistory returns the three period ranks of an account. On the total
// board there is no earlier period, so the previous rank repeats the current
// one (no change) and the oldest is none.
func rankingHistory(account string, ranks []map[string]int) [3]int64 {
	history := [3]int64{rankingNone, rankingNone, rankingNone}
	key := strings.ToLower(account)
	for index := range history {
		if index < len(ranks) {
			if rank, ok := ranks[index][key]; ok {
				history[index] = int64(rank)
			}
		}
	}
	if len(ranks) == 1 {
		history[1] = history[0]
	}
	return history
}

// rankingOwner is the board key of the requesting player or Pride.
func rankingOwner(pride bool, params map[string]string) string {
	if pride {
		if id := rankingParamInt(params, "ACCOUNT"); id > 0 {
			return strconv.FormatInt(id, 10)
		}
	}
	return params["ACCOUNT"]
}

// rankingListResponse answers RankList.aspx (parser sub_34173FB0):
//
//	=TYPE;KIND;PERIODTYPE;count;page;pages;myPage;PERIOD;id0;id1;id2;MAINTYPE;SUBTYPE
//	-rank;account;name;level;value;percent;rank0;rank1;rank2   (the player's own row)
//	rank;account;name;level;value;percent;rank0;rank1;rank2    (count rows, max 15)
//
// id0..id2 are the requested period and the two before; rank0..rank2 the
// row's rank in them (the client picks the pair by comparing PERIOD with the
// ids, sub_342CCC20, and draws the change with sub_34234020). On Pride boards
// the account field carries the Pride id (emblem and top-3 owner check).
func (s *Server) rankingListResponse(ctx context.Context, params map[string]string, now time.Time) string {
	rankType := int(rankingParamInt(params, "TYPE"))
	kind := int(rankingParamInt(params, "KIND"))
	periodType := int(rankingParamInt(params, "PERIODTYPE"))
	period := rankingParamInt(params, "PERIOD")
	pageNumber := int(rankingParamInt(params, "PAGE"))
	pride := rankType == 1
	periods := rankingPeriods(periodType, period, now)
	var board []store.RankingEntry
	ranks := make([]map[string]int, len(periods))
	for index, p := range periods {
		entries := s.rankingBoard(ctx, pride, kind, p)
		if index == 0 {
			board = entries
		}
		ranks[index] = rankingRanks(entries)
	}
	pages := max(1, (len(board)+rankingPageSize-1)/rankingPageSize)
	pageNumber = min(max(pageNumber, 1), pages)
	owner := rankingOwner(pride, params)
	myRank, found := ranks[0][strings.ToLower(owner)]
	myPage := 1
	if found {
		myPage = (myRank-1)/rankingPageSize + 1
	}
	ids := [3]int64{}
	for index := range ids {
		if index < len(periods) {
			ids[index] = periods[index].id
		}
	}
	start := (pageNumber - 1) * rankingPageSize
	end := min(start+rankingPageSize, len(board))
	if start > end {
		start = end
	}
	var out strings.Builder
	fmt.Fprintf(&out, "+1;%d\n", rankingTypeRankList)
	fmt.Fprintf(&out, "=%d;%d;%d;%d;%d;%d;%d;%d;%d;%d;%d;%d;%d\n",
		rankType, kind, periodType, end-start, pageNumber, pages, myPage, ids[0],
		ids[0], ids[1], ids[2], rankingParamInt(params, "MAINTYPE"), rankingParamInt(params, "SUBTYPE"))
	if found {
		entry := board[myRank-1]
		history := rankingHistory(entry.Account, ranks)
		fmt.Fprintf(&out, "-%d;%s;%s;%d;%d;%d;%d;%d;%d\n", myRank, rankingAccountField(entry.Account), rankingNameField(entry.Name),
			entry.Level, entry.Value, rankingPercent(entry, kind), history[0], history[1], history[2])
	} else {
		// A Pride-less player's own Pride row carries the name "0", which
		// the client clears (sub_34175610).
		name := params["NICKNAME"]
		if pride && name == "" {
			name = "0"
		}
		fmt.Fprintf(&out, "-%d;%s;%s;0;0;0;%d;%d;%d\n", rankingNone, rankingAccountField(owner), rankingNameField(name),
			rankingNone, rankingNone, rankingNone)
	}
	for index := start; index < end; index++ {
		entry := board[index]
		history := rankingHistory(entry.Account, ranks)
		fmt.Fprintf(&out, "%d;%s;%s;%d;%d;%d;%d;%d;%d\n", index+1, rankingAccountField(entry.Account), rankingNameField(entry.Name),
			entry.Level, entry.Value, rankingPercent(entry, kind), history[0], history[1], history[2])
	}
	return out.String()
}

// rankingMyRankResponse answers RankListMyRank.aspx (parser sub_34174860):
//
//	=account;name;slot;level;0;0;0;0
//	kind;value;losses;rankG1;rankG2;rankG3   x4 (kinds 0..3)
//
// slot is TYPE (0 personal, 1 Pride: the client stores the reply at
// 192*slot and asks for the Pride part after the personal one,
// sub_342CFAF0). value/losses are the requested period (GRAPH1); the ranks
// are the GRAPH1..3 periods. The four '=' tail values (+168, +60, +64, +68)
// were not seen read. The reply ends with a newline: exactly four rows plus
// the empty last line keep sub_341753B0 (run on the same buffer) from
// resetting the current day.
func (s *Server) rankingMyRankResponse(ctx context.Context, params map[string]string, now time.Time) string {
	rankType := int(rankingParamInt(params, "TYPE"))
	periodType := int(rankingParamInt(params, "PERIODTYPE"))
	pride := rankType == 1
	owner := rankingOwner(pride, params)
	key := strings.ToLower(owner)
	graphs := []int64{rankingParamInt(params, "GRAPH1"), rankingParamInt(params, "GRAPH2"), rankingParamInt(params, "GRAPH3")}
	name := params["NICKNAME"]
	level := 0
	var out strings.Builder
	var rows [4]string
	for kind := store.RankingKindKill; kind <= store.RankingKindDeath; kind++ {
		value, losses := int64(0), int64(0)
		var history [3]int64
		for index, graph := range graphs {
			p := rankingPeriods(periodType, graph, now)[0]
			entries := s.rankingBoard(ctx, pride, kind, p)
			history[index] = rankingNone
			for position, entry := range entries {
				if strings.ToLower(entry.Account) != key {
					continue
				}
				history[index] = int64(position + 1)
				if index == 0 {
					value, losses = entry.Value, entry.Losses
					if entry.Name != "" {
						name = entry.Name
					}
					level = entry.Level
				}
				break
			}
			if periodType == rankingTotal {
				history[1], history[2] = history[0], rankingNone
				break
			}
		}
		rows[kind] = fmt.Sprintf("%d;%d;%d;%d;%d;%d\n", kind, value, losses, history[0], history[1], history[2])
	}
	fmt.Fprintf(&out, "+1;%d\n", rankingTypeMyRank)
	fmt.Fprintf(&out, "=%s;%s;%d;%d;0;0;0;0\n", rankingAccountField(owner), rankingNameField(name), rankType&1, level)
	for _, row := range rows {
		out.WriteString(row)
	}
	return out.String()
}

func rankingCommentKey(params map[string]string) store.RankingCommentKey {
	return store.RankingCommentKey{
		Type:       int(rankingParamInt(params, "TYPE")),
		Kind:       int(rankingParamInt(params, "KIND")),
		PeriodType: int(rankingParamInt(params, "PERIODTYPE")),
		Period:     rankingParamInt(params, "PERIOD"),
		MainType:   rankingParamInt(params, "MAINTYPE"),
		SubType:    rankingParamInt(params, "SUBTYPE"),
	}
}

// rankingTop3Response answers CommentListTop3.aspx (parser sub_34174D60):
//
//	=TYPE;KIND;PERIODTYPE;PERIOD;MAINTYPE;SUBTYPE
//	position;account;name[;comment]   x3 ("0" alone for an empty place)
//
// The memo (sub_3428B740) shows the comment or "Buraya yazabilirsin." and
// offers editing to the ranker (personal: own callsign; Pride: own Pride id
// and the Pride master).
func (s *Server) rankingTop3Response(ctx context.Context, params map[string]string, now time.Time) string {
	key := rankingCommentKey(params)
	pride := key.Type == 1
	board := s.rankingBoard(ctx, pride, key.Kind, rankingPeriods(key.PeriodType, key.Period, now)[0])
	comments := map[string]string{}
	if ranking, ok := s.users.(store.RankingStore); ok {
		if loaded, err := ranking.RankingComments(ctx, key); err != nil {
			s.logger.Warn("load ranking comments", "error", err)
		} else {
			comments = loaded
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "+1;%d\n", rankingTypeTop3)
	fmt.Fprintf(&out, "=%d;%d;%d;%d;%d;%d\n", key.Type, key.Kind, key.PeriodType, key.Period, key.MainType, key.SubType)
	for position := 1; position <= 3; position++ {
		if position > len(board) {
			out.WriteString("0\n")
			continue
		}
		entry := board[position-1]
		fmt.Fprintf(&out, "%d;%s;%s", position, rankingAccountField(entry.Account), rankingNameField(entry.Name))
		if comment := comments[strings.ToLower(entry.Account)]; comment != "" {
			out.WriteString(";" + rankingEscape(comment))
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// updateRankingComment handles UpdateCommentTop3.aspx (builder
// sub_342CCB10): ACCOUNT is the owner (account, or Pride id), EDITOR the
// writer's account and COMMENT the URL-encoded text. Only a top-3 owner may
// write ("1. 2. veya 3. olan oyuncu yorum yazabilir", string 9504/9505); the
// editor must be logged in on this server from the requesting address.
func (s *Server) updateRankingComment(ctx context.Context, params map[string]string, remote netip.Addr, now time.Time) bool {
	ranking, ok := s.users.(store.RankingStore)
	if !ok {
		return false
	}
	key := rankingCommentKey(params)
	pride := key.Type == 1
	owner := rankingOwner(pride, params)
	editor := params["EDITOR"]
	comment := strings.TrimSpace(params["COMMENT"])
	if owner == "" || editor == "" || !utf8.ValidString(comment) || utf8.RuneCountInString(comment) > rankingCommentMax {
		return false
	}
	if !s.rankingEditorOnline(editor, remote) {
		return false
	}
	if pride {
		prideID, err := strconv.ParseUint(owner, 10, 32)
		if err != nil {
			return false
		}
		master, err := ranking.PrideMasterAccount(ctx, uint32(prideID))
		if err != nil || !strings.EqualFold(master, editor) {
			return false
		}
	} else if !strings.EqualFold(owner, editor) {
		return false
	}
	board := s.rankingBoard(ctx, pride, key.Kind, rankingPeriods(key.PeriodType, key.Period, now)[0])
	if rank, found := rankingRanks(board)[strings.ToLower(owner)]; !found || rank > 3 {
		return false
	}
	if err := ranking.SetRankingComment(ctx, key, owner, comment); err != nil {
		s.logger.Warn("save ranking comment", "error", err)
		return false
	}
	return true
}

func (s *Server) rankingEditorOnline(account string, remote netip.Addr) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for session := range s.sessions {
		login, _, userID, _ := session.identity()
		if userID < 1 || !strings.EqualFold(login, account) {
			continue
		}
		if !remote.IsValid() || session.PeerIP == remote {
			return true
		}
	}
	return false
}
