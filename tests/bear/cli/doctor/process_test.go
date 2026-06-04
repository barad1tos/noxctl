package doctor_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/doctor"
)

func TestProcessListContainsExactProcessName(t *testing.T) {
	processList := `/Applications/Bear.app/Contents/MacOS/Bear
/Applications/Bear.app/Contents/MacOS/Bear Helper
/usr/bin/grep Bear
`

	if !doctor.ProcessListContains(processList, "Bear") {
		t.Fatal("process list with Bear executable did not match Bear")
	}
	if doctor.ProcessListContains("/Applications/Bear.app/Contents/MacOS/Bear Helper\n", "Bear") {
		t.Fatal("process list matched Bear Helper as Bear")
	}
	if doctor.ProcessListContains("/usr/bin/grep Bear\n", "Bear") {
		t.Fatal("process list matched argv text instead of executable basename")
	}
	if doctor.ProcessListContains("\n   \n", "Bear") {
		t.Fatal("blank process list rows matched Bear")
	}
}
