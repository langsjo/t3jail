package guestabi

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Every key in the JSON must map to a struct field; otherwise a value the Nix
// side uses would silently be ignored by Go.
func TestNoUnknownKeys(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var abi ABI
	if err := dec.Decode(&abi); err != nil {
		t.Fatal(err)
	}
}

func TestAddressesConsistent(t *testing.T) {
	abi := Get()
	if !abi.GatewayPrefix4().Contains(abi.Guest4) {
		t.Errorf("guest4 %v not in %v", abi.Guest4, abi.GatewayPrefix4())
	}
	if !abi.GatewayPrefix6().Contains(abi.Guest6) {
		t.Errorf("guest6 %v not in %v", abi.Guest6, abi.GatewayPrefix6())
	}
	if !abi.Gateway4.Is4() || !abi.Gateway6.Is6() {
		t.Errorf("gateway families wrong: %v %v", abi.Gateway4, abi.Gateway6)
	}
}
