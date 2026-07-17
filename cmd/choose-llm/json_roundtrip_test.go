package main

import "encoding/json"

// jsonRoundtripPickOutput serialises o and decodes into dst.
func jsonRoundtripPickOutput(o pickOutput, dst interface{}) error {
	bb, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return json.Unmarshal(bb, dst)
}
