package alloc_test

import (
	"fmt"

	"github.com/dd0wney/fault"
	"github.com/dd0wney/fault/alloc"
)

// Outstanding counts what the caller took and has not given back, so a sweep
// can assert that a failed operation released whatever it had already
// allocated. That assertion is the one most error paths get wrong, because the
// happy path frees and the error path returns early.
func ExampleFault_Outstanding() {
	// A zero Points arms nothing, so every allocation below succeeds. Sweep
	// supplies an armed one.
	a := alloc.New(&fault.Points{}, alloc.Go())

	first, err := a.Bytes(1024)
	if err != nil {
		panic(err)
	}
	second, err := a.Bytes(64)
	if err != nil {
		panic(err)
	}
	fmt.Println("while held:", a.Outstanding())

	a.Free(first)
	fmt.Println("after one free:", a.Outstanding())

	a.Free(second)
	fmt.Println("after both:", a.Outstanding())

	// Output:
	// while held: 2
	// after one free: 1
	// after both: 0
}
