package types

import "testing"

func TestDifficulty(t *testing.T) {
	hexDiff := "000000000000000000000000683a8b1c"
	diff, err := DifficultyFromString(hexDiff)
	if err != nil {
		t.Fatal(err)
	}

	if diff.String() != hexDiff {
		t.Fatalf("expected %s, got %s", hexDiff, diff)
	}
}

func TestDifficulty_UnmarshalJSON(t *testing.T) {
	hexDiff := "\"0x4970d\""
	var diff Difficulty
	err := diff.UnmarshalJSON([]byte(hexDiff))
	if err != nil {
		t.Fatal(err)
	}

	if diff.Lo != 0x4970d {
		t.Fatalf("expected %d, got %d", 0x4970d, diff.Lo)
	}
}
