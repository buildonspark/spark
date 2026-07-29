package a

type stats struct {
	got  int // want `"got" names the side of a comparison`
	want int // want `"want" names the side of a comparison`
	rows int
}

type gotUtxo struct { // want `"gotUtxo" names the side of a comparison`
	txid string
}

func compare(want int) int { // want `"want" names the side of a comparison`
	got := want + 1 // want `"got" names the side of a comparison`
	return got
}

func suffixed() (string, error) {
	gotHex := "ff"    // want `"gotHex" names the side of a comparison`
	var wantErr error // want `"wantErr" names the side of a comparison`
	return gotHex, wantErr
}

func snakeCase() int {
	want_seq := 3 // want `"want_seq" names the side of a comparison`
	return want_seq
}

func ranged(items []int) int {
	total := 0
	for _, want := range items { // want `"want" names the side of a comparison`
		total += want
	}
	return total
}

// Ordinary words that merely begin with the same letters are left alone, as are names that say what they hold.
func allowed(numItems int) bool {
	gotten := 1
	wanted := 2
	expectedNumItems := 3
	return gotten+wanted+expectedNumItems == numItems
}
