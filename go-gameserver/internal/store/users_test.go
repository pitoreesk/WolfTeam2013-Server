package store

import (
	"errors"
	"testing"
)

func TestNextLevelThreshold(t *testing.T) {
	tests := []struct {
		level int
		want  int
	}{{62, 0}, {61, 2000}, {60, 2500}, {59, 3500}, {34, 389000}, {2, 24922500}, {1, 24922500}}
	for _, test := range tests {
		if got := NextLevelThreshold(test.level); got != test.want {
			t.Errorf("NextLevelThreshold(%d) = %d, want %d", test.level, got, test.want)
		}
	}
}

func TestApplyRankProgress(t *testing.T) {
	tests := []struct {
		name      string
		level     int
		gp        int
		wantLevel int
		wantGP    int
	}{
		{name: "below threshold", level: 61, gp: 1999, wantLevel: 61, wantGP: 1999},
		{name: "at threshold", level: 61, gp: 2000, wantLevel: 60, wantGP: 2000},
		{name: "above threshold", level: 60, gp: 3000, wantLevel: 59, wantGP: 3000},
		{name: "several grades", level: 61, gp: 5000, wantLevel: 57, wantGP: 5000},
		{name: "reserved rank", level: 62, gp: 5000, wantLevel: 62, wantGP: 5000},
		{name: "maximum rank", level: 1, gp: 50000000, wantLevel: 1, wantGP: 50000000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			level, gp := applyRankProgress(test.level, test.gp)
			if level != test.wantLevel || gp != test.wantGP {
				t.Errorf("applyRankProgress(%d, %d) = %d/%d, want %d/%d", test.level, test.gp, level, gp, test.wantLevel, test.wantGP)
			}
		})
	}
}

func TestValidateAccount(t *testing.T) {
	if err := validateAccount("valid_user", "pass"); err != nil {
		t.Fatal(err)
	}
	if err := validateAccount("x", "pass"); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("short username error = %v", err)
	}
	if err := validateAccount("valid_user", "x"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password error = %v", err)
	}
}
