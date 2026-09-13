package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// outputText is the one piece of this package's logic that's meaningfully
// testable without a real Bedrock endpoint -- everything else in this file
// is a thin, request-shaping wrapper around the AWS SDK's own Converse/
// ConverseStream calls, which need live AWS to exercise for real (same
// reasoning internal/entity/awsbatch's real client and internal/objectstore/
// s3store already use: excluded from the unit coverage gate, not force-
// tested against a faked wire protocol disproportionate to what it'd prove).

func TestOutputText_ExtractsFirstTextBlock(t *testing.T) {
	out := &types.ConverseOutputMemberMessage{
		Value: types.Message{
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "the answer"},
			},
		},
	}
	got, err := outputText(out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "the answer" {
		t.Errorf("got %q, want %q", got, "the answer")
	}
}

func TestOutputText_SkipsNonTextBlocksToFindText(t *testing.T) {
	out := &types.ConverseOutputMemberMessage{
		Value: types.Message{
			Content: []types.ContentBlock{
				&types.ContentBlockMemberToolUse{},
				&types.ContentBlockMemberText{Value: "the real answer"},
			},
		},
	}
	got, err := outputText(out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "the real answer" {
		t.Errorf("got %q, want %q", got, "the real answer")
	}
}

func TestOutputText_NoTextContent_ReturnsEmptyNoError(t *testing.T) {
	out := &types.ConverseOutputMemberMessage{
		Value: types.Message{Content: []types.ContentBlock{&types.ContentBlockMemberToolUse{}}},
	}
	got, err := outputText(out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (caller treats this as the empty-response error case)", got)
	}
}

// types.UnknownUnionMember is the SDK's own real fixture for "the wire sent
// a union variant this SDK version doesn't recognize" -- a genuine
// ConverseOutput value (its isConverseOutput method is unexported, so
// nothing outside the types package could implement the interface any
// other way), used here to prove outputText errors on an unexpected
// variant instead of panicking or silently returning nothing
// indistinguishable from "no text content", the case above.
func TestOutputText_UnexpectedVariant_ReturnsError(t *testing.T) {
	_, err := outputText(&types.UnknownUnionMember{Tag: "somethingNew"})
	if err == nil {
		t.Fatal("expected an error for an unexpected ConverseOutput variant, got nil")
	}
}

func TestAws32_ReturnsPointerToValue(t *testing.T) {
	p := aws32(4096)
	if p == nil || *p != 4096 {
		t.Errorf("aws32(4096) = %v, want pointer to 4096", p)
	}
}
