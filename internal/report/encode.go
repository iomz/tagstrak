package report

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

type wireIdentity struct {
	Authority  string            `json:"authority"`
	KeyType    string            `json:"keyType"`
	Components map[string]string `json:"components"`
}

type wireEvent struct {
	ID              string       `json:"id"`
	Source          string       `json:"source"`
	ObservedAt      string       `json:"observedAt"`
	Subscriptions   []string     `json:"subscriptions"`
	Scheme          string       `json:"scheme"`
	EPC             string       `json:"epc"`
	Identity        wireIdentity `json:"identity"`
	PureIdentityURI string       `json:"pureIdentityURI,omitempty"`
}

// encodeEvent renders one event in the Schema wire format.
func encodeEvent(e Event) ([]byte, error) {
	subscriptions := e.Subscriptions
	if subscriptions == nil {
		subscriptions = []string{}
	}
	components := e.Identity.Components
	if components == nil {
		components = map[string]string{}
	}
	return json.Marshal(wireEvent{
		ID:            e.ID,
		Source:        e.Source,
		ObservedAt:    e.ObservedAt.UTC().Format(time.RFC3339Nano),
		Subscriptions: subscriptions,
		Scheme:        e.Scheme,
		EPC:           hex.EncodeToString(e.EPC),
		Identity: wireIdentity{
			Authority:  e.Identity.Authority,
			KeyType:    e.Identity.KeyType,
			Components: components,
		},
		PureIdentityURI: e.PureIdentityURI,
	})
}

// batchEnvelope is the encoded size of a batch with no events, used to keep
// whole batches within the byte budget.
func batchEnvelope(id string) int {
	return len(encodeBatch(id, nil))
}

// encodeBatch joins already-encoded events into a batch body.
func encodeBatch(id string, events [][]byte) []byte {
	idJSON, _ := json.Marshal(id) // marshalling a string cannot fail
	size := len(`{"schema":"`+Schema+`","batch":,"events":[]}`) + len(idJSON) + len(events)
	for _, e := range events {
		size += len(e)
	}
	body := make([]byte, 0, size)
	body = append(body, `{"schema":"`+Schema+`","batch":`...)
	body = append(body, idJSON...)
	body = append(body, `,"events":[`...)
	for i, e := range events {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, e...)
	}
	return append(body, "]}"...)
}
