package main

import (
	"testing"

	cli "github.com/urfave/cli/v2"
)

func TestServeCommandExposesRunnerRoutingFlags(t *testing.T) {
	var serve *cli.Command
	for _, command := range commands() {
		if command.Name == "serve" {
			serve = command
			break
		}
	}
	if serve == nil {
		t.Fatal("serve command is missing")
	}
	flags := make(map[string]cli.Flag, len(serve.Flags))
	for _, flag := range serve.Flags {
		flags[flag.Names()[0]] = flag
	}
	for _, name := range []string{"runner-label", "runner-tag", "runner-group"} {
		if flags[name] == nil {
			t.Fatalf("serve flag %q is missing", name)
		}
	}
	group, ok := flags["runner-group"].(*cli.StringFlag)
	if !ok || group.Value != "default" {
		t.Fatalf("runner-group = %#v, want default", flags["runner-group"])
	}
}
