package jobs

import (
	"encoding/json"
	"errors"
)

const CustomerGuestResultKey = "customer_substrate_guest"

// CustomerGuestBinding is the receipt of native guest admission, carried beside
// StackKit input. It contains no Proxmox credentials or provider VM handles.
type CustomerGuestBinding struct {
	LeaseID     string `json:"lease_id"`
	ServerID    string `json:"server_id"`
	OperationID string `json:"operation_id"`
}

func customerGuestCheckpoint(job *Job) (*CustomerGuestBinding, error) {
	snapshot := job.Snapshot()
	raw, ok := snapshot.Result[CustomerGuestResultKey]
	if !ok {
		raw, ok = snapshot.Payload[CustomerGuestResultKey]
	}
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var binding CustomerGuestBinding
	if json.Unmarshal(encoded, &binding) != nil || binding.LeaseID == "" || binding.ServerID == "" || binding.OperationID == "" {
		return nil, errors.New("customer guest admission receipt unavailable")
	}
	return &binding, nil
}
