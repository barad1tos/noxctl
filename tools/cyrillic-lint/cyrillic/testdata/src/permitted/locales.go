package permitted

import "fmt"

// Demonstrates the //cyrillic:permit directive. No diagnostic expected.
func _() {
	//cyrillic:permit
	s := "Поезії"
	fmt.Println(s)
}
