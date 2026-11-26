package game

import (
	"testing"
	"time"

	"wtserver-go/internal/store"
)

type clientUserInfo2013 struct {
	Name                    string
	Authority, B776         byte
	School, Gold, WC, Cash  uint32
	U32, GP, NextLevel      uint32
	B648, Level             byte
	Stats                   [12]uint32
	PTBonus                 byte
	PendingID               uint32
	PendingName             string
	PrideID                 uint32
	PrideName, PrideMaster  string
	PrideRank               uint32
	PrideCount              byte
	PrideCreated            string
	PrideGrade              uint16
	PrideIntro, PrideNotice string
	PrideClose              byte
	PrideCloseAt            string
	PrideRole, PrideB       byte
	PrideASCII              string
	Chains                  []string
	Crests                  []uint16
}

// client2013ParseUserInfo translates 2013 TR CS_CH_USERINFO_ACK (sub_34188610):
// 12 u32 stats (the 12th is play time), then a u8 P.T. bonus index.
func client2013ParseUserInfo(t *testing.T, payload []byte) clientUserInfo2013 {
	r := newReader2012(t, payload)
	var u clientUserInfo2013
	u.Name, u.Authority, u.B776 = r.utf16(), r.u8(), r.u8()
	u.School, u.Gold, u.WC, u.Cash = r.u32(), r.u32(), r.u32(), r.u32()
	u.U32, u.GP, u.NextLevel = r.u32(), r.u32(), r.u32()
	u.B648, u.Level = r.u8(), r.u8()
	for i := range u.Stats {
		u.Stats[i] = r.u32()
	}
	u.PTBonus = r.u8()
	u.PendingID, u.PendingName = r.u32(), r.utf16()
	u.PrideID = r.u32()
	if u.PrideID != 0 {
		u.PrideName, u.PrideMaster = r.utf16(), r.utf16()
		u.PrideRank, u.PrideCount = r.u32(), r.u8()
		u.PrideCreated, u.PrideGrade = r.ascii(), r.u16()
		u.PrideIntro, u.PrideNotice = r.utf16(), r.utf16()
		u.PrideClose = r.u8()
		if u.PrideClose >= 10 {
			u.PrideCloseAt = r.ascii()
		}
		u.PrideRole, u.PrideB, u.PrideASCII = r.u8(), r.u8(), r.ascii()
		for n := r.u8(); n > 0; n-- {
			u.Chains = append(u.Chains, r.ascii())
		}
		for n := r.u8(); n > 0; n-- {
			u.Crests = append(u.Crests, r.u16())
		}
	}
	r.done()
	return u
}

func TestUserInfo2013(t *testing.T) {
	info := store.AccountSnapshot{
		Authority: 13, Gold: 1234, WC: 5678, Cash: 9012, GP: 999999, Level: 34,
		Draws: 5, Wins: 9, Losses: 4, Kills: 7, Deaths: 6, HeadshotKills: 3,
		SpecialWeaponKills: 2, WolfKills: 1, PlayTimeSeconds: 3600,
	}
	payload, err := UserInfoPayload("Player", info)
	if err != nil {
		t.Fatal(err)
	}
	u := client2013ParseUserInfo(t, payload)
	if u.Name != "Player" || u.Authority != 13 || u.Gold != 1234 || u.WC != 5678 || u.Cash != 9012 ||
		u.GP != 999999 || u.Level != 34 || u.PrideID != 0 {
		t.Fatalf("user info = %+v", u)
	}
	want := [12]uint32{0, 0, 5, 9, 4, 0, 7, 6, 2, 3, 1, 3600}
	if u.Stats != want {
		t.Fatalf("stats = %v, want %v", u.Stats, want)
	}

	closeAt := time.Date(2012, 10, 1, 0, 0, 0, 0, time.UTC)
	pride := store.Pride{
		ID: 22, Name: "test", Master: "Admin", RankPoints: 44, MemberCount: 3,
		CreatedAt: time.Date(2012, 9, 11, 0, 0, 0, 0, time.UTC), CloseStatus: 10,
		CloseAt: &closeAt,
	}
	payload, err = UserInfoWithPridePayload("Player", info, pride, store.PrideMember{Role: 1})
	if err != nil {
		t.Fatal(err)
	}
	u = client2013ParseUserInfo(t, payload)
	if u.PrideID != 22 || u.PrideName != "test" || u.PrideMaster != "Admin" || u.PrideRank != 44 ||
		u.PrideClose != 10 || u.PrideCloseAt == "" || u.PrideRole != 1 || u.Stats[5] != 22 {
		t.Fatalf("pride user info = %+v", u)
	}
}
