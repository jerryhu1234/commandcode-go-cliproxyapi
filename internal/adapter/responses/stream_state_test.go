package responses

import "testing"

func TestStreamSnapshotNativeResponses(t *testing.T) {
	sc := NewStreamConverter("openai-response")
	sc.Feed([]byte(": ping\n\n"))
	if sc.StreamSnapshot().Progress != 0 {
		t.Fatal(sc.StreamSnapshot())
	}
	sc.Feed([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"))
	sc.Feed([]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\"}\n\n"))
	if sc.StreamSnapshot().TerminalSeen {
		t.Fatal("item done is not terminal")
	}
	sc.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	s := sc.StreamSnapshot()
	if !s.Started || !s.DoneSeen || !s.TerminalSeen || s.TerminalKind != "completed" {
		t.Fatal(s)
	}
}

func TestResponsesDuplicateStartsDoNotAdvance(t *testing.T) {
	sc := NewStreamConverter("openai-response")
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"
	sc.Feed([]byte(created))
	p := sc.StreamSnapshot().Progress
	sc.Feed([]byte(created))
	if sc.StreamSnapshot().Progress != p {
		t.Fatalf("duplicate created advanced: %+v", sc.StreamSnapshot())
	}
	added := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"m\"}}\n\n"
	sc.Feed([]byte(added))
	p = sc.StreamSnapshot().Progress
	sc.Feed([]byte(added))
	if sc.StreamSnapshot().Progress != p {
		t.Fatalf("duplicate item advanced")
	}
}
