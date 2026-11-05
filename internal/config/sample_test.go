package config

import "testing"

func TestSampleConfigValid(t *testing.T) {
	c, err := Load("../../docs/config.yaml.sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(c, nil); err != nil {
		t.Fatal(err)
	}
}
