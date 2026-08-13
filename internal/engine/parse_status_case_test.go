package engine

import "testing"

// Agents echo their prompt's casing ("outcome SUCCESS"); the parser must
// accept any case — run 9afacdba lost seven on-disk verdicts to "FAIL" vs
// "fail".
func TestParseStatusCaseInsensitive(t *testing.T) {
	cases := map[string]Status{
		"SUCCESS": StatusSuccess, "Success": StatusSuccess, "success": StatusSuccess,
		"FAIL": StatusFail, " fail ": StatusFail,
		"PARTIAL_SUCCESS": StatusPartialSuccess,
		"nonsense":        StatusUnknown, "": StatusUnknown,
	}
	for in, want := range cases {
		if got := ParseStatus(in); got != want {
			t.Errorf("ParseStatus(%q) = %v, want %v", in, got, want)
		}
	}
}
