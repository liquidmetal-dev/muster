package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestUnitUsesDefaultConfigPath(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/systemd/musterd.service")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, want := range []string{
		"ExecStart=/usr/bin/musterd -config " + defaultConfigPath,
		"ConditionPathExists=" + defaultConfigPath,
		"User=muster",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("musterd.service has no line %q", want)
		}
	}
}
