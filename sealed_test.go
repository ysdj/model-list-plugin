package main

import "testing"

func TestSealedPayloadReadsTheVersionedMarker(t *testing.T) {
	// "__sealed__:v1:" + base64("v10" + two bytes of ciphertext)
	payload, err := sealedPayload("__sealed__:v1:djEwAQI=")
	if err != nil {
		t.Fatalf("payload not read: %v", err)
	}
	if string(payload) != "v10\x01\x02" {
		t.Fatalf("unexpected payload %q", payload)
	}
	if _, err := sealedPayload("__sealed__:v1:not base64!"); err == nil {
		t.Fatal("a value that is not base64 was accepted")
	}
}

func TestSealedValueIsNeverSentWhenItCannotBeOpened(t *testing.T) {
	if _, ok := desktopGatewayFrom(nil); ok {
		t.Fatal("no directory must yield no configuration")
	}
}
