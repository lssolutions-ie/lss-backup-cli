package hwinfo

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestCollect(t *testing.T) {
	info := Collect()
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))

	if info.OS == "" {
		t.Error("OS should not be empty")
	}
	if info.CPUs == 0 {
		t.Error("CPUs should not be 0")
	}
	if info.LANIP == "" {
		t.Error("LAN IP should not be empty")
	}
}
