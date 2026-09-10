package main

import "testing"

func TestCaptureDefaults(t *testing.T) {
	command := newCommand()
	if command.Flags().Lookup("timeout").DefValue != "2m0s" || command.Flags().Lookup("count").DefValue != "0" {
		t.Fatal("capture must default to a full 120-second window")
	}
}

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
