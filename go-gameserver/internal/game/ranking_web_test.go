package game

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type rankingUsers struct {
	*fakeUsers
	boards   map[string][]store.RankingEntry // "pride/kind/dated"
	queries  []store.RankingQuery
	comments map[string]string
	saved    []string
	masters  map[uint32]string
}

func rankingBoardKey(pride bool, kind int, dated bool) string {
	return fmt.Sprintf("%v/%d/%v", pride, kind, dated)
}

func (u *rankingUsers) RankingBoard(_ context.Context, query store.RankingQuery, limit int) ([]store.RankingEntry, error) {
	u.queries = append(u.queries, query)
	entries := u.boards[rankingBoardKey(query.Pride, query.Kind, !query.From.IsZero())]
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (u *rankingUsers) RankingComments(context.Context, store.RankingCommentKey) (map[string]string, error) {
	return u.comments, nil
}

func (u *rankingUsers) SetRankingComment(_ context.Context, key store.RankingCommentKey, owner, comment string) error {
	u.saved = append(u.saved, fmt.Sprintf("%d/%d/%d/%d:%s=%s", key.Type, key.Kind, key.PeriodType, key.Period, owner, comment))
	return nil
}

func (u *rankingUsers) PrideMasterAccount(_ context.Context, prideID uint32) (string, error) {
	return u.masters[prideID], nil
}

func newRankingTestServer(users *rankingUsers) *Server {
	loopback := netip.MustParseAddr("127.0.0.1")
	return NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// rankingFields mirrors the client: strtok on ";\r\n" drops empty fields.
func rankingFields(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool { return r == ';' || r == '\r' || r == '\n' })
}

var rankingTestNow = time.Date(2026, time.September, 25, 15, 0, 0, 0, time.UTC)

func TestRankingDateTableMapping(t *testing.T) {
	// Rows of WT_Attributes\RankingDateData.dat: id -> week byte (+7).
	for id, week := range map[int]int{0: 35, 1: 36, 8: 37, 120: 1, 121: 1, 365: 36, 484: 1, 486: 1, 846: 52, 848: 53, 851: 53} {
		if got := rankingTableWeek(id); got != week {
			t.Fatalf("week of date id %d = %d, want %d", id, got, week)
		}
	}
	// 2026-09-25 -> 2013-09-25 (id 389); 2026-03-10 -> 2014-03-10.
	if got := rankingDateID(rankingTestNow); got != 389 {
		t.Fatalf("date id = %d, want 389", got)
	}
	if got := rankingDateID(time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)); got != 555 {
		t.Fatalf("March date id = %d, want 555", got)
	}
	if got := rankingDateID(time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC)); got != 545 {
		t.Fatalf("29 February date id = %d, want 545 (28 February)", got)
	}
}

func TestRankingPeriodsFollowTheClientChoices(t *testing.T) {
	daily := rankingPeriods(rankingDaily, 388, rankingTestNow) // yesterday
	if !daily[0].from.Equal(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)) || daily[0].id != 388 || daily[2].id != 386 {
		t.Fatalf("daily periods = %+v", daily)
	}
	lastWeek := int64(rankingTableWeek(389 - 7))
	weekly := rankingPeriods(rankingWeekly, lastWeek, rankingTestNow)
	// 2026-09-25 is a Friday; its week starts on Sunday 2026-09-20.
	if !weekly[0].from.Equal(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)) || weekly[0].id != lastWeek {
		t.Fatalf("weekly periods = %+v", weekly)
	}
	monthly := rankingPeriods(rankingMonthly, 8, rankingTestNow)
	if !monthly[0].from.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) || monthly[1].id != 7 || monthly[2].id != 6 {
		t.Fatalf("monthly periods = %+v", monthly)
	}
	january := rankingPeriods(rankingMonthly, 1, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	if january[1].id != 12 || january[1].from.Year() != 2025 {
		t.Fatalf("January periods = %+v", january)
	}
	if total := rankingPeriods(rankingTotal, 0, rankingTestNow); len(total) != 1 || !total[0].from.IsZero() {
		t.Fatalf("total periods = %+v", total)
	}
}

func TestRankingCurrentDayHasNoTrailingNewline(t *testing.T) {
	server := newRankingTestServer(&rankingUsers{fakeUsers: &fakeUsers{}})
	body := server.rankingResponse(context.Background(), "currentday.aspx", nil, netip.Addr{}, rankingTestNow)
	if body != "+1;11\n389;9" {
		t.Fatalf("CurrentDay = %q", body)
	}
}

func TestRankingListMatchesTheClientParser(t *testing.T) {
	var board []store.RankingEntry
	for i := 0; i < 17; i++ {
		board = append(board, store.RankingEntry{Account: fmt.Sprintf("player%02d", i+1), Name: fmt.Sprintf("Oyuncu %d", i+1), Level: 20, Value: int64(100 - i), Losses: int64(i)})
	}
	board[16].Account = "a_very_long_account_name"
	users := &rankingUsers{fakeUsers: &fakeUsers{}, boards: map[string][]store.RankingEntry{
		rankingBoardKey(false, store.RankingKindRecord, false): board,
	}}
	server := newRankingTestServer(users)
	params := map[string]string{"TYPE": "0", "KIND": "1", "PERIODTYPE": "3", "PERIOD": "0", "PAGE": "2",
		"MAINTYPE": "0", "SUBTYPE": "0", "ACCOUNT": "player16", "NICKNAME": "Oyuncu 16"}
	body := server.rankingResponse(context.Background(), "ranklist.aspx", params, netip.Addr{}, rankingTestNow)
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("RankList does not end with a newline: %q", body)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if lines[0] != "+1;9" {
		t.Fatalf("header = %q", lines[0])
	}
	header := rankingFields(strings.TrimPrefix(lines[1], "="))
	if len(header) != 13 || header[3] != "2" || header[4] != "2" || header[5] != "2" || header[6] != "2" {
		t.Fatalf("'=' line = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "-") {
		t.Fatalf("own row missing: %q", lines[2])
	}
	own := rankingFields(strings.TrimPrefix(lines[2], "-"))
	// Total board: the previous rank repeats the current one, the oldest is none.
	if len(own) != 9 || own[0] != "16" || own[1] != "player16" || own[2] != "Oyuncu+16" || own[5] != "85" || own[6] != "16" || own[7] != "16" || own[8] != "100000000" {
		t.Fatalf("own row = %q", own)
	}
	if len(lines) != 5 {
		t.Fatalf("rows = %d, want 2 on page 2: %q", len(lines)-3, body)
	}
	last := rankingFields(lines[4])
	if len(last) != 9 || last[0] != "17" || len(last[1]) != rankingAccountMax {
		t.Fatalf("last row = %q", last)
	}
}

func TestRankingListOfAPrideLessPlayer(t *testing.T) {
	server := newRankingTestServer(&rankingUsers{fakeUsers: &fakeUsers{}})
	body := server.rankingResponse(context.Background(), "ranklist.aspx",
		map[string]string{"TYPE": "1", "KIND": "2", "PERIODTYPE": "0", "PERIOD": "389"}, netip.Addr{}, rankingTestNow)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	own := rankingFields(strings.TrimPrefix(lines[2], "-"))
	if len(own) != 9 || own[0] != "100000000" || own[2] != "0" {
		t.Fatalf("own row = %q", own)
	}
	header := rankingFields(strings.TrimPrefix(lines[1], "="))
	if header[3] != "0" || header[7] != "389" || header[8] != "389" || header[9] != "388" || header[10] != "387" {
		t.Fatalf("'=' line = %q", header)
	}
}

func TestRankingListRanksPreviousPeriods(t *testing.T) {
	now := []store.RankingEntry{{Account: "a", Name: "A", Value: 9}, {Account: "b", Name: "B", Value: 5}}
	users := &rankingUsers{fakeUsers: &fakeUsers{}, boards: map[string][]store.RankingEntry{
		rankingBoardKey(false, store.RankingKindKill, true): now,
	}}
	server := newRankingTestServer(users)
	body := server.rankingResponse(context.Background(), "ranklist.aspx",
		map[string]string{"TYPE": "0", "KIND": "0", "PERIODTYPE": "2", "PERIOD": "9", "ACCOUNT": "b"}, netip.Addr{}, rankingTestNow)
	if len(users.queries) != 3 || !users.queries[1].From.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("queries = %+v", users.queries)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	row := rankingFields(lines[4])
	if row[0] != "2" || row[6] != "2" || row[7] != "2" || row[8] != "2" {
		t.Fatalf("row = %q", row)
	}
}

func TestRankingMyRankHasFourRowsAndAnEmptyLastLine(t *testing.T) {
	users := &rankingUsers{fakeUsers: &fakeUsers{}, boards: map[string][]store.RankingEntry{
		rankingBoardKey(false, store.RankingKindRecord, true): {{Account: "other", Name: "O", Value: 7}, {Account: "Me", Name: "Ben", Level: 33, Value: 4, Losses: 2}},
	}}
	server := newRankingTestServer(users)
	body := server.rankingResponse(context.Background(), "ranklistmyrank.aspx", map[string]string{
		"TYPE": "0", "PERIODTYPE": "0", "PERIOD": "389", "GRAPH1": "389", "GRAPH2": "388", "GRAPH3": "387",
		"ACCOUNT": "me", "NICKNAME": "Ben",
	}, netip.Addr{}, rankingTestNow)
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("MyRank must end with a newline: %q", body)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 6 || lines[0] != "+1;10" {
		t.Fatalf("lines = %q", lines)
	}
	head := rankingFields(strings.TrimPrefix(lines[1], "="))
	if len(head) != 8 || head[0] != "me" || head[1] != "Ben" || head[2] != "0" || head[3] != "33" {
		t.Fatalf("'=' line = %q", head)
	}
	record := rankingFields(lines[3])
	if len(record) != 6 || record[0] != "1" || record[1] != "4" || record[2] != "2" || record[3] != "2" {
		t.Fatalf("record row = %q", record)
	}
	if kill := rankingFields(lines[2]); len(kill) != 6 || kill[3] != "100000000" {
		t.Fatalf("kill row = %q", kill)
	}
}

func TestRankingTop3ListsTheRankersAndComments(t *testing.T) {
	users := &rankingUsers{fakeUsers: &fakeUsers{},
		boards: map[string][]store.RankingEntry{
			rankingBoardKey(true, store.RankingKindPoints, false): {{Account: "12", Name: "Kurtlar", Value: 40}, {Account: "7", Name: "Av; Ekibi", Value: 30}},
		},
		comments: map[string]string{"12": "Birinciyiz!"},
	}
	server := newRankingTestServer(users)
	body := server.rankingResponse(context.Background(), "commentlisttop3.aspx",
		map[string]string{"TYPE": "1", "KIND": "2", "PERIODTYPE": "3", "PERIOD": "0"}, netip.Addr{}, rankingTestNow)
	want := "+1;14\n=1;2;3;0;0;0\n1;12;Kurtlar;Birinciyiz%21\n2;7;Av%3B+Ekibi\n0\n"
	if body != want {
		t.Fatalf("CommentListTop3 = %q, want %q", body, want)
	}
}

func TestRankingCommentNeedsATop3OwnerOnline(t *testing.T) {
	users := &rankingUsers{fakeUsers: &fakeUsers{},
		boards: map[string][]store.RankingEntry{
			rankingBoardKey(false, store.RankingKindKill, false): {{Account: "first"}, {Account: "second"}, {Account: "third"}, {Account: "fourth"}},
			rankingBoardKey(true, store.RankingKindKill, false):  {{Account: "12"}},
		},
		masters: map[uint32]string{12: "first"},
	}
	server := newRankingTestServer(users)
	loopback := netip.MustParseAddr("127.0.0.1")
	for _, account := range []string{"first", "fourth"} {
		client, peer := net.Pipe()
		defer client.Close()
		defer peer.Close()
		session, err := newSession(transport.NewEncryptedConn(peer, nil), loopback, loopback)
		if err != nil {
			t.Fatal(err)
		}
		session.setAuthenticated(account, 5, account, 10)
		server.mu.Lock()
		server.sessions[session] = struct{}{}
		server.mu.Unlock()
	}
	update := func(params map[string]string, remote netip.Addr) string {
		params["KIND"], params["PERIODTYPE"], params["PERIOD"] = "0", "3", "0"
		return server.rankingResponse(context.Background(), "updatecommenttop3.aspx", params, remote, rankingTestNow)
	}
	if got := update(map[string]string{"TYPE": "0", "ACCOUNT": "first", "EDITOR": "first", "COMMENT": "Merhaba"}, loopback); got != "+1;13\n" {
		t.Fatalf("top-3 comment = %q", got)
	}
	if got := update(map[string]string{"TYPE": "0", "ACCOUNT": "fourth", "EDITOR": "fourth", "COMMENT": "x"}, loopback); got != "+0;13\n" {
		t.Fatalf("rank 4 comment = %q", got)
	}
	if got := update(map[string]string{"TYPE": "0", "ACCOUNT": "first", "EDITOR": "first", "COMMENT": "x"}, netip.MustParseAddr("10.0.0.9")); got != "+0;13\n" {
		t.Fatalf("comment from another address = %q", got)
	}
	if got := update(map[string]string{"TYPE": "0", "ACCOUNT": "second", "EDITOR": "first", "COMMENT": "x"}, loopback); got != "+0;13\n" {
		t.Fatalf("comment for someone else = %q", got)
	}
	if got := update(map[string]string{"TYPE": "1", "ACCOUNT": "12", "EDITOR": "first", "COMMENT": "Klan"}, loopback); got != "+1;13\n" {
		t.Fatalf("Pride master comment = %q", got)
	}
	if got := update(map[string]string{"TYPE": "1", "ACCOUNT": "12", "EDITOR": "fourth", "COMMENT": "Klan"}, loopback); got != "+0;13\n" {
		t.Fatalf("Pride member comment = %q", got)
	}
	if len(users.saved) != 2 || users.saved[0] != "0/0/3/0:first=Merhaba" || users.saved[1] != "1/0/3/0:12=Klan" {
		t.Fatalf("saved = %q", users.saved)
	}
}

func TestRankingWebServesTheWinINetRequest(t *testing.T) {
	server := newRankingTestServer(&rankingUsers{fakeUsers: &fakeUsers{}})
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.handleRankingConnection(context.Background(), peer)
		close(done)
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	// Relative object name and the NUL-padded 1024-byte header (sub_34173C60).
	headers := "Content-Type: text/plain; charset=utf-8;" + strings.Repeat("\x00", 983) + "\r\n\r\n"
	go func() {
		_, _ = client.Write([]byte("GET CurrentDay.aspx? HTTP/1.0\r\n" + headers))
	}()
	response, err := io.ReadAll(bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	<-done
	text := string(response)
	if !strings.HasPrefix(text, "HTTP/1.0 200 OK\r\n") || !strings.HasSuffix(text, "\r\n\r\n+1;11\n"+fmt.Sprintf("%d;%d", rankingDateID(time.Now()), int(time.Now().Month()))) {
		t.Fatalf("response = %q", text)
	}
}

func TestRankingWebEndpointPayload(t *testing.T) {
	payload, err := RankingWebEndpointPayload(netip.MustParseAddr("192.168.1.20"), 40719)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 192, 168, 1, 20, 0x9f, 0x0f}; string(payload) != string(want) {
		t.Fatalf("0x050E payload = % x", payload)
	}
}
