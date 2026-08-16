package daemon

import "testing"

func TestMaterializeEditUniqueSpan(t *testing.T) {
	got, err := materializeEdit("alpha", "beta", []byte("xx alpha yy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "xx beta yy" {
		t.Fatalf("got %q", got)
	}
}

func TestMaterializeEditRejectsEmptyMissingAndDuplicate(t *testing.T) {
	if _, err := materializeEdit("", "x", []byte("abc")); err == nil {
		t.Fatal("empty old must fail")
	}
	if _, err := materializeEdit("nope", "x", []byte("abc")); err == nil {
		t.Fatal("missing old must fail")
	}
	if _, err := materializeEdit("ab", "x", []byte("ab ab")); err == nil {
		t.Fatal("duplicate old must fail")
	}
}

func TestParseActionEditFields(t *testing.T) {
	act, err := parseAction(`{"tool":"edit","path":"hello.txt","old":"hello","new":"hi","intent":"greet"}`)
	if err != nil {
		t.Fatal(err)
	}
	if act.Tool != "edit" || act.Path != "hello.txt" || act.Old != "hello" || act.New != "hi" {
		t.Fatalf("action = %+v", act)
	}
}
