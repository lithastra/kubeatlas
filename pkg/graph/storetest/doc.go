// Package storetest provides the contract test suite that every
// graph.GraphStore implementation must satisfy. It lives in a separate
// package so importing pkg/graph does not pull in the standard testing
// package as a transitive dependency.
//
// Implementations call storetest.Run from their own _test.go file:
//
//	func TestStore_Contract(t *testing.T) {
//	    storetest.Run(t, func(t *testing.T) graph.GraphStore {
//	        return memory.New()
//	    })
//	}
package storetest
