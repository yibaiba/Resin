package platform

import "testing"

func TestNormalizeSubscriptionIDs(t *testing.T) {
	got, err := NormalizeSubscriptionIDs(nil)
	if err != nil {
		t.Fatalf("nil: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil: got %v, want empty", got)
	}

	got, err = NormalizeSubscriptionIDs([]string{" sub-a ", "sub-b"})
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if len(got) != 2 || got[0] != "sub-a" || got[1] != "sub-b" {
		t.Fatalf("trim: got %v", got)
	}

	if _, err := NormalizeSubscriptionIDs([]string{"sub-a", "sub-a"}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, err := NormalizeSubscriptionIDs([]string{"sub-a", "  "}); err == nil {
		t.Fatal("expected empty error")
	}
}
