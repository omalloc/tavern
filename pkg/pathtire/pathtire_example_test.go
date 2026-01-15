package pathtire_test

import (
	"fmt"

	"github.com/omalloc/tavern/pkg/pathtire"
)

func ExampleNewPathTrie() {
	trie := pathtire.NewPathTrie[string]()

	trie.Insert("http://sendya.me.gslb.com/host/path/", "11")
	value7, found7 := trie.Search("http://sendya.me.gslb.com/host/path/to/1M")

	fmt.Printf("value7: %s found: %t\n", value7, found7)

	// Output:
	// value7: 11 found: true
}
