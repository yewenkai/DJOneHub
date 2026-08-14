//go:build !darwin

package main

import "errors"

func discoverEgressRuntime() (egressRuntimeStatus, []string, error) {
	return egressRuntimeStatus{}, nil, errors.New("出口策略仅支持 macOS")
}

func discoverEgressApplications() ([]egressInstalledApplication, error) {
	return nil, errors.New("应用选择仅支持 macOS")
}

func applyEgressPlatform(egressPolicyStore, egressPreview) (egressAppliedState, error) {
	return egressAppliedState{}, errors.New("出口策略仅支持 macOS")
}

func restoreEgressPlatform(egressAppliedState) error {
	return errors.New("出口策略仅支持 macOS")
}
