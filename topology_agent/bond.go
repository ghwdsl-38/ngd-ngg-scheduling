package main

import bonddiscovery "demo.ngg/topology-agent/pkg/bond"

type interfaceSelection = bonddiscovery.InterfaceSelection

func selectLLDPInterfaces(sysClassNet string, explicit map[string]struct{}) (map[string]interfaceSelection, error) {
	return bonddiscovery.SelectInterfaces(sysClassNet, explicit)
}

func selectLLDPInterfacesAt(sysClassNet, procBonding string, explicit map[string]struct{}) (map[string]interfaceSelection, error) {
	return bonddiscovery.SelectInterfacesAt(sysClassNet, procBonding, explicit)
}

func sortedSelections(values map[string]interfaceSelection) []interfaceSelection {
	return bonddiscovery.Sorted(values)
}
