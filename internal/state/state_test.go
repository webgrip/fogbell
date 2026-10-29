package state

import (
	"encoding/json"
	"testing"
)

func TestReadsWhatEarlierWritersWrote(t *testing.T) {
	raw := `{"group_key": "k", "state": "firing", "count": 3.0, "flaps": 2, "started_at": 1793088900.5, "group_labels": "{\"alertname\": \"X\"}",
		"objects": "[{\"name\": \"ns/p0\", \"labels\": {\"namespace\": \"ns\", \"pod\": \"p0\"}}]", "children": "{\"pods\": 2}", "noise_votes": "1"}`
	var st Incident
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if st.Count != 3 || st.Firings() != 2 || st.NoiseVotes != 1 || st.LabelSet()["alertname"] != "X" {
		t.Fatal(st)
	}
	if objs := st.ObjectList(); len(objs) != 1 || objs[0].Labels["pod"] != "p0" {
		t.Fatal(objs)
	}
	if st.ChildMap()["pods"] != 2 {
		t.Fatal(st.ChildMap())
	}
	if (&Incident{}).Firings() != 1 {
		t.Fatal("a missing flaps field is one firing")
	}
}
