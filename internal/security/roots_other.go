//go:build !linux

package security

func platformBeneath(string, string) error { return nil }
