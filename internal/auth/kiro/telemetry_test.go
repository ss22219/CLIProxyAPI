package kiro

import (
	"testing"
	"time"
)

func TestParseJSONStringOrRaw(t *testing.T) {
	if got := parseJSONStringOrRaw(`"us-east-1:identity"`); got != "us-east-1:identity" {
		t.Fatalf("quoted value = %q", got)
	}
	if got := parseJSONStringOrRaw(`raw-value`); got != "raw-value" {
		t.Fatalf("raw value = %q", got)
	}
}

func TestParseCognitoExpiration(t *testing.T) {
	got, err := parseCognitoExpiration("2026-05-09T04:53:31Z")
	if err != nil {
		t.Fatalf("parse expiration: %v", err)
	}
	if got.UTC().Format(time.RFC3339) != "2026-05-09T04:53:31Z" {
		t.Fatalf("expiration = %s", got.UTC().Format(time.RFC3339))
	}
	if _, err = parseCognitoExpiration(""); err == nil {
		t.Fatal("expected empty expiration error")
	}
}
