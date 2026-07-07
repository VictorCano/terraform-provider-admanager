package main

import (
	"testing"
	"time"
)

func TestParseSoapVersion(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantYear  int
		wantMonth int
		wantErr   bool
	}{
		{
			name:      "canonical declaration",
			src:       `const soapVersion = "v202605"`,
			wantYear:  2026,
			wantMonth: 5,
		},
		{
			name:      "extra spacing around equals",
			src:       "const soapVersion   =\t\"v202511\"",
			wantYear:  2025,
			wantMonth: 11,
		},
		{
			name:      "embedded in real source",
			src:       "package soap\n\n// comment\nconst soapVersion = \"v202605\"\n\nconst other = 1\n",
			wantYear:  2026,
			wantMonth: 5,
		},
		{
			name:    "constant missing",
			src:     "package soap\n\nconst other = \"v202605\"\n",
			wantErr: true,
		},
		{
			name:    "malformed version value",
			src:     `const soapVersion = "v2026"`,
			wantErr: true,
		},
		{
			name:    "non-numeric version",
			src:     `const soapVersion = "vNEXT01"`,
			wantErr: true,
		},
		{
			// Regex matches (month is any two digits) but 13 is out of range.
			name:    "month above 12",
			src:     `const soapVersion = "v202613"`,
			wantErr: true,
		},
		{
			// Regex matches but month 00 is out of range.
			name:    "month zero",
			src:     `const soapVersion = "v202600"`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			y, m, err := parseSoapVersion([]byte(tt.src))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got year=%d month=%d", y, m)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if y != tt.wantYear || m != tt.wantMonth {
				t.Fatalf("got year=%d month=%d, want year=%d month=%d", y, m, tt.wantYear, tt.wantMonth)
			}
		})
	}
}

func TestAgeMonths(t *testing.T) {
	tests := []struct {
		name       string
		relYear    int
		relMonth   int
		now        string
		wantMonths int
	}{
		{name: "same month", relYear: 2026, relMonth: 5, now: "2026-05", wantMonths: 0},
		{name: "one month", relYear: 2026, relMonth: 5, now: "2026-06", wantMonths: 1},
		{name: "crosses year boundary", relYear: 2026, relMonth: 5, now: "2027-02", wantMonths: 9},
		{name: "full year", relYear: 2026, relMonth: 5, now: "2027-05", wantMonths: 12},
		{name: "future now before release", relYear: 2026, relMonth: 5, now: "2026-03", wantMonths: -2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now, err := parseNow(tt.now)
			if err != nil {
				t.Fatalf("parseNow: %v", err)
			}
			got := ageMonths(tt.relYear, tt.relMonth, now)
			if got != tt.wantMonths {
				t.Fatalf("ageMonths = %d, want %d", got, tt.wantMonths)
			}
		})
	}
}

func TestParseNow(t *testing.T) {
	got, err := parseNow("2027-02")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Year() != 2027 || got.Month() != time.February {
		t.Fatalf("got %v, want 2027-02", got)
	}
	if _, err := parseNow("not-a-date"); err == nil {
		t.Fatal("expected error for malformed -now")
	}
	if _, err := parseNow("2027-13"); err == nil {
		t.Fatal("expected error for out-of-range month")
	}
}

func TestEvaluateThreshold(t *testing.T) {
	src := `const soapVersion = "v202605"`
	// warn-months = 9; release 2026-05.
	cases := []struct {
		name     string
		now      string
		warn     int
		wantFail bool
	}{
		{name: "well within window", now: "2026-08", warn: 9, wantFail: false},
		{name: "exactly at threshold does not fail", now: "2027-02", warn: 9, wantFail: false},
		{name: "one month past threshold fails", now: "2027-03", warn: 9, wantFail: true},
		{name: "long past sunset fails", now: "2028-01", warn: 9, wantFail: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			now, err := parseNow(tt.now)
			if err != nil {
				t.Fatalf("parseNow: %v", err)
			}
			res, err := evaluate([]byte(src), now, tt.warn)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if res.stale != tt.wantFail {
				t.Fatalf("stale = %v, want %v (age=%d)", res.stale, tt.wantFail, res.ageMonths)
			}
		})
	}
}

func TestEvaluatePropagatesParseError(t *testing.T) {
	now, _ := parseNow("2027-01")
	if _, err := evaluate([]byte("no constant here"), now, 9); err == nil {
		t.Fatal("expected evaluate to propagate parse error")
	}
}
