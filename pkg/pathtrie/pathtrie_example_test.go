package pathtrie_test

import (
	"fmt"

	"github.com/omalloc/tavern/pkg/pathtrie"
)

func ExampleNewPathTrie() {

	trie := pathtrie.NewPathTrie[string, int64]()

	trie.Insert("/api/users", 0)
	trie.Insert("http://sendya.me.gslb.com/host/path/", 1768480300)

	value1, found1 := trie.Search("/api/users/123")
	value7, found7 := trie.Search("http://sendya.me.gslb.com/host/path/to/1M")

	fmt.Printf("value1: %d found: %t\n", value1, found1)
	fmt.Printf("value7: %d found: %t\n", value7, found7)

	// Output:
	// value1: 0 found: true
	// value7: 1768480300 found: true
}
