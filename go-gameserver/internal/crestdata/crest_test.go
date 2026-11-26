package crestdata

import "testing"

func TestEmbeddedCatalogMatchesClientTable(t *testing.T) {
	catalog := Default()
	if len(catalog.All()) != 21 {
		t.Fatalf("crest count = %d, want 21", len(catalog.All()))
	}
	sf, ok := catalog.Lookup(1001)
	if !ok || sf.Group != GroupHeadshot || sf.Tier != 0 || sf.Cost != 5000 || sf.Target != 100 || sf.RewardWC != 50 ||
		sf.Icon != `WT_Interface\record\Crest\mm_cresticon_SF_1.dtx` {
		t.Fatalf("crest 1001 = %+v", sf)
	}
	jackpot, ok := catalog.Lookup(3009)
	if !ok || jackpot.Group != GroupLuckyBox || jackpot.Target != 50 || jackpot.RewardWC != 400 {
		t.Fatalf("crest 3009 = %+v", jackpot)
	}
}
