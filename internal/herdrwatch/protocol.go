package herdrwatch

import "encoding/json"

// StateReq represents a state request sent over the control socket.
type StateReq struct {
	SessionKey string `json:"session_key,omitempty"`
}

// StateResp represents the state response returned by the control socket.
type StateResp struct {
	Ready           bool     `json:"ready"`
	Epoch           int64    `json:"epoch"`
	MRU             []string `json:"mru"`
	ClassifiedError string   `json:"classified_error,omitempty"`
}

type stateRespAlias StateResp

// MarshalJSON ensures MRU is never serialized as null in JSON.
func (r StateResp) MarshalJSON() ([]byte, error) {
	if r.MRU == nil {
		r.MRU = []string{}
	}
	return json.Marshal((stateRespAlias)(r))
}
