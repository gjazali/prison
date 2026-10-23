package guest

import (
	"net/netip"
	"testing"
)

func TestSentinelAllocatorHandsOutSequentialStableAddresses(t *testing.T) {
	allocator := newSentinelAllocator()
	first, ok := allocator.Allocate("a.test")
	if !ok || first != netip.MustParseAddr("127.99.0.1") {
		t.Fatalf("Allocate(a.test) = %v, %v, want 127.99.0.1, true", first, ok)
	}
	second, _ := allocator.Allocate("b.test")
	if second != netip.MustParseAddr("127.99.0.2") {
		t.Fatalf("Allocate(b.test) = %v, want 127.99.0.2", second)
	}
	again, _ := allocator.Allocate("a.test")
	if again != first {
		t.Fatalf("Allocate(a.test) again = %v, want %v", again, first)
	}
	if name, ok := allocator.Lookup(second); !ok || name != "b.test" {
		t.Fatalf("Lookup(%v) = %q, %v, want b.test, true", second, name, ok)
	}
	if _, ok := allocator.Lookup(netip.MustParseAddr("127.99.0.9")); ok {
		t.Fatal("Lookup(127.99.0.9) = found, want not found")
	}
}

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

func TestSentinelAllocatorExhausts(t *testing.T) {
	allocator := newSentinelAllocator()
	allocator.nextOffset = 65533
	last, ok := allocator.Allocate("last.test")
	if !ok || last != lastSentinel {
		t.Fatalf("Allocate(last.test) = %v, %v, want %v, true", last, ok,
			lastSentinel)
	}
	if _, ok := allocator.Allocate("one-too-many.test"); ok {
		t.Fatal("Allocate(past range) = ok, want not ok")
	}
}
