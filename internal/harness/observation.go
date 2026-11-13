package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// SupervisionObservation bounds result bodies, never command inputs, tool
// arguments or changed paths. The original event still goes to the native client.
func SupervisionObservation(raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, errors.New("malformed supervision observation JSON")
	}
	if len(raw) <= 24<<10 {
		return raw, nil
	}
	var event map[string]json.RawMessage
	var params, item map[string]json.RawMessage
	_ = json.Unmarshal(raw, &event)
	_ = json.Unmarshal(event["params"], &params)
	_ = json.Unmarshal(params["item"], &item)
	if item == nil {
		return nil, errors.New("oversized supervision observation has no item")
	}
	for _, key := range []string{"aggregatedOutput", "result"} {
		if body := item[key]; len(body) > 4096 {
			hash := sha256.Sum256(body)
			item[key], _ = json.Marshal(map[string]any{"truncated": true, "bytes": len(body), "sha256": hex.EncodeToString(hash[:]), "preview": string(body[:4096])})
		}
	}
	params["item"], _ = json.Marshal(item)
	event["params"], _ = json.Marshal(params)
	bounded, _ := json.Marshal(event)
	if len(bounded) > 24<<10 {
		return nil, errors.New("supervision action metadata exceeds 24 KiB after bounding results; command inputs and file changes were not truncated")
	}
	return bounded, nil
}
