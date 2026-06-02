package doctor

import "testing"

func TestProcessListContainsExactProcessName(t *testing.T) {
	processList := `/Applications/Bear.app/Contents/MacOS/Bear
/Applications/Bear.app/Contents/MacOS/Bear Helper
/usr/bin/grep Bear
`

	if !processListContains(processList, "Bear") {
		t.Fatal("process list with Bear executable did not match Bear")
	}
	if processListContains("/Applications/Bear.app/Contents/MacOS/Bear Helper\n", "Bear") {
		t.Fatal("process list matched Bear Helper as Bear")
	}
	if processListContains("/usr/bin/grep Bear\n", "Bear") {
		t.Fatal("process list matched argv text instead of executable basename")
	}
}
