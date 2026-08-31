package pgstore

import "testing"

func TestMarshalOrNil_EmptyReturnsNil(t *testing.T) {
	raw, err := marshalOrNil[int](nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("nil slice: got %v, want nil (SQL NULL)", raw)
	}

	raw, err = marshalOrNil([]int{})
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("empty slice: got %v, want nil (SQL NULL)", raw)
	}
}

// TestMarshalOrNil_ReturnsString locks in that marshalOrNil's non-nil
// result is a string, not []byte — see marshalOrNil's doc comment for why
// that distinction matters under the API's simple-protocol pooled
// connection (a []byte parameter renders as a bytea hex-literal, which a
// ::jsonb cast can't parse as JSON; a string renders as a text literal,
// which it can).
func TestMarshalOrNil_ReturnsString(t *testing.T) {
	raw, err := marshalOrNil([]int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := raw.(string)
	if !ok {
		t.Fatalf("got %T, want string", raw)
	}
	if s != "[1,2,3]" {
		t.Errorf("got %q, want %q", s, "[1,2,3]")
	}
}

func TestUnmarshalIfPresent_RoundtripsMarshalOrNilsOutput(t *testing.T) {
	raw, err := marshalOrNil([]int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	// Scanning a jsonb column always yields []byte regardless of how it was
	// written — this is what the value looks like coming back from
	// Postgres, not from marshalOrNil directly.
	var out []int
	if err := unmarshalIfPresent([]byte(raw.(string)), &out); err != nil {
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
