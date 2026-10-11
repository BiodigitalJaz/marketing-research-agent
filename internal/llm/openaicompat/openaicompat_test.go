package openaicompat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/contract"
)

const usage = `"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}`

func completion(message, finish string) string {
	return `{"id":"cc_1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":` + message + `,"finish_reason":"` + finish + `"}],` + usage + `}`
}

func chunk(delta, finish string) string {
	f := "null"
	if finish != "" {
		f = `"` + finish + `"`
	}
	return `{"id":"cc_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}`
}

// Run the contract against a local-server profile (vLLM-style reasoning via
// chat template) and an OpenAI profile (reasoning_effort).
func TestContractLocal(t *testing.T) {
	contract.Run(t, func(base string) llm.Provider {
		return New(Config{ID: "local", BaseURL: base + "/v1", Local: true, ReasoningStyle: ReasoningChatTemplate, MaxRetries: -1})
	}, fixtures(ReasoningChatTemplate))
}

func TestContractOpenAI(t *testing.T) {
	contract.Run(t, func(base string) llm.Provider {
		return New(Config{ID: "openai", BaseURL: base + "/v1", APIKey: "sk-test", MaxRetries: -1})
	}, fixtures(ReasoningEffort))
}

func fixtures(style string) contract.Fixtures {
	return func(t *testing.T, scenario string) http.Handler {
		P := contract.Path
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("path = %s", r.URL.Path)
			}
			b := contract.DecodeBody(t, r)
			switch scenario {
			case contract.Text:
				if P(b, "messages", 0, "role") != "system" || P(b, "messages", 0, "content") != "Be brief." || P(b, "max_tokens") != 100.0 {
					t.Errorf("request = %v", b)
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":"Hi there."}`, "stop"))
			case contract.ParallelTools:
				if P(b, "tools", 0, "type") != "function" || P(b, "tools", 0, "function", "name") != "get_weather" ||
					P(b, "tools", 0, "function", "parameters", "required", 0) != "city" {
					t.Errorf("tools = %v", P(b, "tools"))
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":null,"tool_calls":[
					{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
					{"id":"call_b","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"}}]}`, "tool_calls"))
			case contract.ToolResults:
				msgs, _ := P(b, "messages").([]any)
				if len(msgs) != 4 || P(b, "messages", 1, "tool_calls", 1, "id") != "call_2" ||
					P(b, "messages", 2, "role") != "tool" || P(b, "messages", 2, "tool_call_id") != "call_1" ||
					P(b, "messages", 3, "tool_call_id") != "call_2" {
					t.Errorf("messages = %v", msgs)
				}
				if c, _ := P(b, "messages", 3, "content").(string); !strings.HasPrefix(c, "Error:") {
					t.Errorf("error tool result not marked: %q", c)
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":"Paris is 18C."}`, "stop"))
			case contract.Thinking:
				if style == ReasoningChatTemplate {
					if P(b, "chat_template_kwargs", "enable_thinking") != true {
						t.Errorf("chat_template_kwargs = %v", P(b, "chat_template_kwargs"))
					}
				} else if P(b, "reasoning_effort") != "high" {
					t.Errorf("reasoning_effort = %v", P(b, "reasoning_effort"))
				}
				msgs, _ := P(b, "messages").([]any)
				if len(msgs) == 1 {
					contract.WriteJSON(w, completion(`{"role":"assistant","content":null,"reasoning_content":"Need the weather tool.",
						"tool_calls":[{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}`, "tool_calls"))
					return
				}
				// Chat Completions has no thinking field: the history carries the
				// tool call and result, and no reasoning text leaks into content.
				if P(b, "messages", 1, "tool_calls", 0, "id") != "call_a" || P(b, "messages", 1, "content") != nil {
					t.Errorf("assistant turn = %v", P(b, "messages", 1))
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":"18C in Paris."}`, "stop"))
			case contract.Vision:
				url, _ := P(b, "messages", 0, "content", 0, "image_url", "url").(string)
				if P(b, "messages", 0, "content", 0, "type") != "image_url" || !strings.HasPrefix(url, "data:image/png;base64,") {
					t.Errorf("image part = %v", P(b, "messages", 0, "content", 0))
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":"A chart."}`, "stop"))
			case contract.Structured:
				if P(b, "response_format", "type") != "json_schema" || P(b, "response_format", "json_schema", "name") != "answer" ||
					P(b, "response_format", "json_schema", "strict") != true {
					t.Errorf("response_format = %v", P(b, "response_format"))
				}
				contract.WriteJSON(w, completion(`{"role":"assistant","content":"{\"answer\":\"42\"}"}`, "stop"))
			case contract.StreamText:
				if P(b, "stream") != true || P(b, "stream_options", "include_usage") != true {
					t.Errorf("stream flags = %v %v", P(b, "stream"), P(b, "stream_options"))
				}
				contract.WriteSSE(w, [][2]string{
					{"", chunk(`{"role":"assistant","content":""}`, "")},
					{"", chunk(`{"content":"Hi "}`, "")},
					{"", chunk(`{"content":"there."}`, "")},
					{"", chunk(`{}`, "stop")},
					{"", `{"id":"cc_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],` + usage + `}`},
					{"", "[DONE]"},
				})
			case contract.StreamTools:
				contract.WriteSSE(w, [][2]string{
					{"", chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}`, "")},
					{"", chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}`, "")},
					{"", chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}`, "")},
					{"", chunk(`{}`, "tool_calls")},
					{"", "[DONE]"},
				})
			case contract.RateLimited:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`))
			case contract.Refusal:
				contract.WriteJSON(w, completion(`{"role":"assistant","content":null,"refusal":"I can't help with that."}`, "stop"))
			}
		})
	}
}

func TestMaxTokensParamDefault(t *testing.T) {
	if New(Config{BaseURL: "https://api.openai.com/v1"}).cfg.MaxTokensParam != "max_completion_tokens" {
		t.Error("OpenAI should default to max_completion_tokens")
	}
	if New(Config{BaseURL: "http://192.168.0.10:1234/v1"}).cfg.MaxTokensParam != "max_tokens" {
		t.Error("local servers should default to max_tokens")
	}
}

func TestLMStudioStyleErrorIsReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Context size has been exceeded."}`))
	}))
	defer srv.Close()
	p := New(Config{ID: "local", BaseURL: srv.URL + "/v1", MaxRetries: -1})
	_, err := p.Chat(t.Context(), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "Context size has been exceeded") {
		t.Fatalf("want readable 400, got %v", err)
	}
}
