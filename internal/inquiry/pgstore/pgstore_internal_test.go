package pgstore

import "testing"

func TestMarshalOrNil_EmptyReturnsNil(t *testing.T) {
	raw, err := marshalOrNil[int](nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("nil slice: got %q, want nil (SQL NULL)", raw)
	}

	raw, err = marshalOrNil([]int{})
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("empty slice: got %q, want nil (SQL NULL)", raw)
	}
}

func TestMarshalOrNil_Roundtrip(t *testing.T) {
	raw, err := marshalOrNil([]int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	if err := unmarshalIfPresent(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0] != 1 || out[2] != 3 {
		t.Errorf("roundtrip: got %v, want [1 2 3]", out)
	}
}

func TestUnmarshalIfPresent_EmptyLeavesDstUntouched(t *testing.T) {
	out := []int{99}
	if err := unmarshalIfPresent(nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != 99 {
		t.Errorf("empty raw should leave dst untouched: got %v", out)
	}
}
