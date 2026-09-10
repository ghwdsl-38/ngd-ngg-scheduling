package main

import "testing"

func TestInterfacesDefaultToAuto(t *testing.T) {
	t.Setenv("LLDP_INTERFACES", "")
	command := newCommand()
	flag := command.Flags().Lookup("interfaces")
	if flag == nil {
		t.Fatal("interfaces flag is not registered")
	}
	if flag.DefValue != "auto" {
		t.Fatalf("interfaces default=%q, want auto", flag.DefValue)
	}
}

func TestIntervalDefaultsToOneShot(t *testing.T) {
	command := newCommand()
	flag := command.Flags().Lookup("interval")
	if flag == nil {
		t.Fatal("interval flag is not registered")
	}
	if flag.DefValue != "0s" {
		t.Fatalf("interval default=%q, want 0s for one-shot mode", flag.DefValue)
	}
}
