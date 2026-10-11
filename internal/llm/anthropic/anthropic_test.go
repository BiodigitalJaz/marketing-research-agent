package anthropic

import (
	"errors"
	"net/http"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/contract"
)

const usage = `"usage":{"input_tokens":12,"output_tokens":5}`

func message(content, stop string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":` + content + `,"stop_reason":"` + stop + `",` + usage + `}`
}

func TestContract(t *testing.T) {
	newProvider := func(base string) llm.Provider {
		return New(Config{ID: "claude", APIKey: "test-key", BaseURL: base, MaxRetries: -1})
	}
	contract.Run(t, newProvider, fixtures)
}

func fixtures(t *testing.T, scenario string) http.Handler {
	P := contract.Path
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("missing api key header")
		}
		b := contract.DecodeBody(t, r)
		switch scenario {
		case contract.Text:
			if P(b, "system", 0, "text") != "Be brief." || P(b, "max_tokens") != 100.0 || P(b, "messages", 0, "role") != "user" {
				t.Errorf("request = %v", b)
			}
			contract.WriteJSON(w, message(`[{"type":"text","text":"Hi there."}]`, "end_turn"))
		case contract.ParallelTools:
			if P(b, "tools", 0, "name") != "get_weather" || P(b, "tools", 0, "input_schema", "required", 0) != "city" || P(b, "tool_choice") != nil {
				t.Errorf("tools = %v tool_choice = %v", P(b, "tools"), P(b, "tool_choice"))
			}
			contract.WriteJSON(w, message(`[
				{"type":"text","text":"Checking both."},
				{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}},
				{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{"city":"Rome"}}]`, "tool_use"))
		case contract.ToolResults:
			msgs, _ := P(b, "messages").([]any)
			if len(msgs) != 3 {
				t.Fatalf("want 3 messages (tool results merged into one user turn), got %d: %v", len(msgs), msgs)
			}
			if P(b, "messages", 1, "content", 1, "type") != "tool_use" || P(b, "messages", 1, "content", 1, "id") != "call_2" {
				t.Errorf("assistant turn = %v", P(b, "messages", 1))
			}
			if P(b, "messages", 2, "content", 0, "tool_use_id") != "call_1" || P(b, "messages", 2, "content", 1, "is_error") != true {
				t.Errorf("tool results = %v", P(b, "messages", 2))
			}
			contract.WriteJSON(w, message(`[{"type":"text","text":"Paris is 18C."}]`, "end_turn"))
		case contract.Thinking:
			if P(b, "thinking", "type") != "adaptive" || P(b, "thinking", "display") != "summarized" || P(b, "output_config", "effort") != "high" {
				t.Errorf("thinking config = %v %v", P(b, "thinking"), P(b, "output_config"))
			}
			msgs, _ := P(b, "messages").([]any)
			if len(msgs) == 1 {
				contract.WriteJSON(w, message(`[
					{"type":"thinking","thinking":"Need the weather tool.","signature":"sig-abc"},
					{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]`, "tool_use"))
				return
			}
			// Turn two: the thinking block must come back first and unchanged.
			if P(b, "messages", 1, "content", 0, "type") != "thinking" || P(b, "messages", 1, "content", 0, "signature") != "sig-abc" ||
				P(b, "messages", 1, "content", 0, "thinking") != "Need the weather tool." {
				t.Errorf("thinking not round-tripped: %v", P(b, "messages", 1))
			}
			contract.WriteJSON(w, message(`[{"type":"text","text":"18C in Paris."}]`, "end_turn"))
		case contract.Vision:
			if P(b, "messages", 0, "content", 0, "type") != "image" || P(b, "messages", 0, "content", 0, "source", "type") != "base64" ||
				P(b, "messages", 0, "content", 0, "source", "media_type") != "image/png" {
				t.Errorf("image block = %v", P(b, "messages", 0, "content", 0))
			}
			contract.WriteJSON(w, message(`[{"type":"text","text":"A chart."}]`, "end_turn"))
		case contract.Structured:
			if P(b, "output_config", "format", "type") != "json_schema" || P(b, "output_config", "format", "schema", "required", 0) != "answer" {
				t.Errorf("output_config = %v", P(b, "output_config"))
			}
			contract.WriteJSON(w, message(`[{"type":"text","text":"{\"answer\":\"42\"}"}]`, "end_turn"))
		case contract.StreamText:
			if P(b, "stream") != true {
				t.Errorf("stream flag missing")
			}
			contract.WriteSSE(w, [][2]string{
				{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":0}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there."}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`},
				{"message_stop", `{"type":"message_stop"}`},
			})
		case contract.StreamTools:
			contract.WriteSSE(w, [][2]string{
				{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":0}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
				{"message_stop", `{"type":"message_stop"}`},
			})
		case contract.RateLimited:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
		case contract.Refusal:
			contract.WriteJSON(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"x"},`+usage+`}`)
		}
	})
}

func TestNoKey(t *testing.T) {
	p := New(Config{ID: "claude"})
	_, err := p.Chat(t.Context(), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	var apiErr *llm.APIError
	if err == nil || !errors.As(err, &apiErr) || !apiErr.Failover() {
		t.Fatalf("want failover-able no-key error, got %v", err)
	}
}
