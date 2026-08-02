//go:build !darwin

package main

import "fmt"

func repairCellularDHCP() (string, string, bool, error) {
	return "", "", false, fmt.Errorf("DHCP 自愈仅支持 macOS")
}
