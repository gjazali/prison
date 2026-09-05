package guest

import (
	"net/netip"
	"testing"
)

// TestSentinelAllocatorHandsOutSequentialStableAddresses checks
// sequential allocation starting at 127.99.0.1, stability on
// repeat, and reverse lookup.
func TestSentinelAllocatorHandsOutSequentialStableAddresses(t *testing.T) {
	allocator := newSentinelAllocator()
	first, ok := allocator.Allocate("a.test")
	if !ok || first != netip.MustParseAddr("127.99.0.1") {
		t.Fatalf("first allocation = %v, %v", first, ok)
	}
	second, _ := allocator.Allocate("b.test")
	if second != netip.MustParseAddr("127.99.0.2") {
		t.Fatalf("second allocation = %v", second)
	}
	again, _ := allocator.Allocate("a.test")
	if again != first {
		t.Fatalf("repeat allocation moved from %v to %v", first, again)
	}
	if name, ok := allocator.Lookup(second); !ok || name != "b.test" {
		t.Fatalf("Lookup(%v) = %q, %v", second, name, ok)
	}
	if _, ok := allocator.Lookup(netip.MustParseAddr("127.99.0.9")); ok {
		t.Fatal("an address never handed out looked up")
	}
}

// TestSentinelAllocatorCrossesOctetBoundary checks that the 256th
// address is 127.99.1.0, not a wrap inside the last octet.
func TestSentinelAllocatorCrossesOctetBoundary(t *testing.T) {
	allocator := newSentinelAllocator()
	var last netip.Addr
	for index := 0; index < 256; index++ {
		last, _ = allocator.Allocate(string(rune('a'+index%26)) +
			string(rune('a'+index/26)) + ".test")
	}
	if last != netip.MustParseAddr("127.99.1.0") {
		t.Fatalf("256th address = %v, want 127.99.1.0", last)
	}
}

// TestSentinelAllocatorReusesReleasedAddresses checks that released
// addresses are reused before fresh ones and that released names
// no longer look up.
func TestSentinelAllocatorReusesReleasedAddresses(t *testing.T) {
	allocator := newSentinelAllocator()
	first, _ := allocator.Allocate("a.test")
	allocator.Allocate("b.test")
	if !allocator.Release("a.test") {
		t.Fatal("Release of a held name reported false")
	}
	if allocator.Release("a.test") {
		t.Fatal("Release of a released name reported true")
	}
	if _, ok := allocator.Lookup(first); ok {
		t.Fatal("a released address still looked up")
	}
	reused, _ := allocator.Allocate("c.test")
	if reused != first {
		t.Fatalf("after release got %v, want reused %v", reused, first)
	}
	next, _ := allocator.Allocate("d.test")
	if next != netip.MustParseAddr("127.99.0.3") {
		t.Fatalf("fresh address after reuse = %v, want 127.99.0.3", next)
	}
}

// TestSentinelAllocatorExhausts checks that allocation past
// 127.99.255.254 is refused.
func TestSentinelAllocatorExhausts(t *testing.T) {
	allocator := newSentinelAllocator()
	allocator.nextOffset = 65533
	last, ok := allocator.Allocate("last.test")
	if !ok || last != lastSentinel {
		t.Fatalf("last allocation = %v, %v", last, ok)
	}
	if _, ok := allocator.Allocate("one-too-many.test"); ok {
		t.Fatal("an allocation past the range succeeded")
	}
}
