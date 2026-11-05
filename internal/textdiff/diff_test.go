package textdiff

import "testing"

func TestUnified(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\n"
	b := "a\nb\nC\nd\ne\nf\ng\nh\n"
	got := Unified("running", "candidate", a, b, 1)
	want := "--- running\n+++ candidate\n@@ -2,3 +2,3 @@\n b\n-c\n+C\n d\n@@ -7,1 +7,2 @@\n g\n+h\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if Unified("x", "y", a, a, 3) != "" {
		t.Fatal("identical should be empty")
	}
}
