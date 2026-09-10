package herdrwatch_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tranceh2/shep/internal/herdrwatch"
)

func TestProtocol_StateReqJSON(t *testing.T) {
	req := herdrwatch.StateReq{
		SessionKey: "test-session-key",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal StateReq failed: %v", err)
	}

	expectedJSON := `{"session_key":"test-session-key"}`
	if string(data) != expectedJSON {
		t.Errorf("Marshal StateReq got %s, want %s", string(data), expectedJSON)
	}

	var decoded herdrwatch.StateReq
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal StateReq failed: %v", err)
	}
	if decoded.SessionKey != req.SessionKey {
		t.Errorf("SessionKey mismatch: got %q, want %q", decoded.SessionKey, req.SessionKey)
	}
}

func TestProtocol_StateRespJSON(t *testing.T) {
	t.Run("ready response with MRU", func(t *testing.T) {
		resp := herdrwatch.StateResp{
			Ready: true,
			Epoch: 42,
			MRU:   []string{"ws-1", "ws-2"},
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal StateResp failed: %v", err)
		}

		var decoded herdrwatch.StateResp
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal StateResp failed: %v", err)
		}

		if !decoded.Ready || decoded.Epoch != 42 || !reflect.DeepEqual(decoded.MRU, resp.MRU) {
			t.Errorf("Decoded response mismatch: got %+v, want %+v", decoded, resp)
		}
		if decoded.ClassifiedError != "" {
			t.Errorf("Expected empty ClassifiedError, got %q", decoded.ClassifiedError)
		}
	})

	t.Run("empty MRU serializes as empty array not null", func(t *testing.T) {
		resp := herdrwatch.StateResp{
			Ready: false,
			Epoch: 0,
			MRU:   []string{},
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal StateResp failed: %v", err)
		}

		expectedJSON := `{"ready":false,"epoch":0,"mru":[]}`
		if string(data) != expectedJSON {
			t.Errorf("Marshal StateResp got %s, want %s", string(data), expectedJSON)
		}
	})

	t.Run("classified error response", func(t *testing.T) {
		resp := herdrwatch.StateResp{
			Ready:           false,
			Epoch:           1,
			MRU:             []string{},
			ClassifiedError: "history store error",
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal StateResp failed: %v", err)
		}

		var decoded herdrwatch.StateResp
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal StateResp failed: %v", err)
		}

		if decoded.ClassifiedError != "history store error" {
			t.Errorf("ClassifiedError got %q, want %q", decoded.ClassifiedError, "history store error")
		}
	})
}
