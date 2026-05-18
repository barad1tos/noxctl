package positive

import "fmt"

func _() {
	s := "Поезії" // want `cyrillic literal forbidden`
	fmt.Println(s)
}
