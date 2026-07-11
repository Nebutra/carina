package daemon

import "testing"

func TestPatchSuffixRejectsDivergedLineage(t *testing.T) {
	got, err := patchSuffix([]string{"p1", "p2", "p3"}, []string{"p1", "p2"})
	if err != nil || len(got) != 1 || got[0] != "p3" {
		t.Fatalf("suffix=%v err=%v", got, err)
	}
	if _, err := patchSuffix([]string{"p1", "other"}, []string{"p1", "p2"}); err == nil {
		t.Fatal("expected divergent lineage refusal")
	}
}
